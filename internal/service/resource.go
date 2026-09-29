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

// MaxFileBytes 是单文件真实内容上限（50 MiB），HTTP 层还需给 multipart 包装留余量。
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

// NewResources 验证存储依赖并准备受控目录；创建失败时拒绝启动文件服务。
func NewResources(store repository.Store, dataDir string) (*Resources, error) {
	if store == nil || dataDir == "" {
		return nil, errors.New("invalid resources configuration")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	return &Resources{store: store, dataDir: dataDir}, nil
}

// fileType 同时比对扩展名与文件头；返回业务类型、可信 MIME 和是否允许上传。
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

// safeFilename 只提取用于展示的安全文件名；磁盘路径始终由服务端随机 ID 决定。
func safeFilename(name string) (string, bool) {
	// 先统一 Windows 与 Unix 路径分隔符，避免带反斜杠的文件名绕过路径清理。
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
	// 临时文件放在目标目录，避免跨文件系统移动时丢失原子重命名的前提。
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
		// DB 失败时尽力删除刚落盘的文件；进程崩溃窗口仍需后续孤儿文件巡检。
		os.Remove(finalPath)
		return model.Resource{}, fmt.Errorf("save resource: %w", err)
	}
	return resource, nil
}

// List 将已校验的筛选和分页条件交给持久层，不在 Service 重复拼接 SQL。
func (resources *Resources) List(ctx context.Context, query model.ListQuery) ([]model.Resource, error) {
	return resources.store.ListResources(ctx, query)
}

// validResourceID 只接受服务端生成的 16 字节十六进制 ID，避免无效输入进入持久层。
func validResourceID(id string) bool {
	if len(id) != 32 {
		return false
	}
	if _, err := hex.DecodeString(id); err != nil {
		return false
	}
	return true
}

// Get 先检查资源 ID 格式，再将合法 ID 交给持久层查询。
func (resources *Resources) Get(ctx context.Context, id string) (model.Resource, error) {
	if !validResourceID(id) {
		return model.Resource{}, repository.ErrNotFound
	}
	return resources.store.GetResource(ctx, id)
}

// SetFavorite 显式设置资料收藏状态；缺失或非法 ID 返回同一种未找到错误，重复设置保持幂等。
func (resources *Resources) SetFavorite(ctx context.Context, id string, favorite bool) (model.Resource, error) {
	if !validResourceID(id) {
		return model.Resource{}, repository.ErrNotFound
	}
	return resources.store.SetFavorite(ctx, id, favorite)
}

// Open 仅按受控存储键打开原件；数据库记录异常时返回文件不可用。
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
