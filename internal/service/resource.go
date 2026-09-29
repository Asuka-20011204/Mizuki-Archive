package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

const MaxFileBytes int64 = 50 << 20

var (
	ErrInvalidFilename = errors.New("invalid filename")
	ErrUnsupportedFile = errors.New("unsupported file")
	ErrFileTooLarge    = errors.New("file too large")
	ErrFileUnavailable = errors.New("file unavailable")
)

// Resources 负责资料的文件校验、受控存储与元数据登记，不依赖 HTTP 请求对象。
type Resources struct {
	store   repository.Store
	dataDir string
}

func NewResources(store repository.Store, dataDir string) (*Resources, error) {
	if store == nil || dataDir == "" {
		return nil, errors.New("invalid resources configuration")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	return &Resources{store: store, dataDir: dataDir}, nil
}

func fileType(filename string, header []byte) (string, string, bool) {
	// 扩展名和文件头必须同时匹配，不能仅相信浏览器传来的 Content-Type。
	extension := strings.ToLower(filepath.Ext(filename))
	detected := http.DetectContentType(header)
	switch {
	case extension == ".pdf" && detected == "application/pdf":
		return "pdf", "application/pdf", true
	case (extension == ".png" && detected == "image/png") || (extension == ".jpg" && detected == "image/jpeg") || (extension == ".jpeg" && detected == "image/jpeg") || (extension == ".webp" && detected == "image/webp"):
		return "image", detected, true
	case (extension == ".md" || extension == ".txt") && strings.HasPrefix(detected, "text/plain") && utf8.Valid(header):
		if extension == ".md" {
			return "markdown", "text/plain; charset=utf-8", true
		}
		return "text", "text/plain; charset=utf-8", true
	default:
		return "", "", false
	}
}

func safeFilename(name string) (string, bool) {
	// 只保留展示用文件名；实际存储路径由随机 ID 生成，避免目录穿越。
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "" || utf8.RuneCountInString(name) > 180 {
		return "", false
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return "", false
		}
	}
	return name, true
}

// Upload 流式写临时文件并计算哈希，再登记元数据；失败时清理临时或已移动的文件。
func (resources *Resources) Upload(ctx context.Context, filename string, source io.Reader) (model.Resource, error) {
	name, valid := safeFilename(filename)
	if !valid {
		return model.Resource{}, ErrInvalidFilename
	}
	header := make([]byte, 512)
	// 先读取少量文件头辨别真实类型，随后把这段字节接回流中，确保内容和哈希都不丢失。
	headerSize, err := io.ReadFull(source, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return model.Resource{}, fmt.Errorf("read header: %w", err)
	}
	header = header[:headerSize]
	kind, contentType, valid := fileType(name, header)
	if !valid {
		return model.Resource{}, ErrUnsupportedFile
	}
	temporary, err := os.CreateTemp(resources.dataDir, "pending-*")
	if err != nil {
		return model.Resource{}, fmt.Errorf("create temporary file: %w", err)
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	digest := sha256.New()
	// 多读 1 字节才能区分“恰好达到上限”和“超过上限”，不把整份文件载入内存。
	written, err := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(io.MultiReader(bytes.NewReader(header), source), MaxFileBytes+1))
	if err != nil {
		return model.Resource{}, fmt.Errorf("write upload: %w", err)
	}
	if written > MaxFileBytes {
		return model.Resource{}, ErrFileTooLarge
	}
	if err := temporary.Close(); err != nil {
		return model.Resource{}, fmt.Errorf("close upload: %w", err)
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return model.Resource{}, fmt.Errorf("generate resource ID: %w", err)
	}
	id := hex.EncodeToString(idBytes)
	// 文件系统与 MySQL 不能共用事务：先移动，再在数据库失败时补偿删除。
	finalPath := filepath.Join(resources.dataDir, id)
	if err := os.Rename(temporary.Name(), finalPath); err != nil {
		return model.Resource{}, fmt.Errorf("commit upload: %w", err)
	}
	resource := model.Resource{ID: id, Name: name, OriginalName: name, Kind: kind, MIME: contentType, Size: written, SHA256: hex.EncodeToString(digest.Sum(nil)), StorageKey: id, CreatedAt: time.Now().UTC()}
	if err := resources.store.SaveResource(ctx, resource); err != nil {
		os.Remove(finalPath)
		return model.Resource{}, fmt.Errorf("save resource: %w", err)
	}
	return resource, nil
}

func (resources *Resources) List(ctx context.Context, query model.ListQuery) ([]model.Resource, error) {
	return resources.store.ListResources(ctx, query)
}

func (resources *Resources) Get(ctx context.Context, id string) (model.Resource, error) {
	// 资源 ID 来自随机字节的十六进制编码，查询前先拒绝其他路径或异常输入。
	if len(id) != 32 {
		return model.Resource{}, repository.ErrNotFound
	}
	if _, err := hex.DecodeString(id); err != nil {
		return model.Resource{}, repository.ErrNotFound
	}
	return resources.store.GetResource(ctx, id)
}

func (resources *Resources) Open(resource model.Resource) (*os.File, error) {
	// 存储键必须与服务端生成的 ID 一致，绝不使用上传者提供的文件名拼接路径。
	if resource.StorageKey != resource.ID {
		return nil, ErrFileUnavailable
	}
	file, err := os.Open(filepath.Join(resources.dataDir, resource.ID))
	if err != nil {
		return nil, fmt.Errorf("open stored file: %w", err)
	}
	return file, nil
}
