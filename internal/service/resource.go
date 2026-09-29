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
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"mizuki-archive/internal/cache"
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
	ErrInvalidTag      = errors.New("invalid tag")
	ErrTooManyTags     = errors.New("too many tags")
	ErrInvalidName     = errors.New("invalid resource name")
)

const maxResourceTags = 10

// Resources 负责资料的文件校验、受控存储与元数据登记，不依赖 HTTP 请求对象。
type Resources struct {
	store   repository.Store
	dataDir string
	cache   cache.Cache
}

// NewResources 验证存储依赖并准备受控目录；创建失败时拒绝启动文件服务。
func NewResources(store repository.Store, dataDir string) (*Resources, error) {
	return NewResourcesWithCache(store, dataDir, nil)
}

// NewResourcesWithCache 创建资料服务并注入可选 Redis；缓存不可用时仍由 MySQL 完成主流程。
func NewResourcesWithCache(store repository.Store, dataDir string, resourceCache cache.Cache) (*Resources, error) {
	if store == nil || dataDir == "" {
		return nil, errors.New("invalid resources configuration")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	return &Resources{store: store, dataDir: dataDir, cache: resourceCache}, nil
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
	resource := model.Resource{ID: id, Name: name, OriginalName: name, Kind: kind, MIME: contentType, Size: written, SHA256: hex.EncodeToString(digest.Sum(nil)), StorageKey: id, Tags: []string{}, CreatedAt: time.Now().UTC()}
	if err := resources.store.SaveResource(ctx, resource); err != nil {
		// DB 失败时尽力删除刚落盘的文件；进程崩溃窗口仍需后续孤儿文件巡检。
		os.Remove(finalPath)
		return model.Resource{}, fmt.Errorf("save resource: %w", err)
	}
	resources.invalidateResourceCaches(ctx, resource.ID)
	return resource, nil
}

// List 将已校验的筛选和分页条件交给持久层，不在 Service 重复拼接 SQL。
func (resources *Resources) List(ctx context.Context, query model.ListQuery) ([]model.Resource, error) {
	if query.Tag != "" {
		normalized, err := NormalizeTag(query.Tag)
		if err != nil {
			return nil, err
		}
		query.Tag = normalized
	}
	// 删除后列表必须立即遵循 MySQL 可见性；不缓存可被并发回填的私有资料列表。
	return resources.store.ListResources(ctx, query)
}

// Recent 按 Redis Sorted Set 中的资源 ID 返回最近访问资料；详情仍回源 MySQL 验证可见性。
func (resources *Resources) Recent(ctx context.Context, limit int64) ([]model.Resource, error) {
	if resources.cache == nil {
		return []model.Resource{}, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	ids, err := resources.cache.RecentIDs(ctx, limit)
	if err != nil {
		return []model.Resource{}, nil
	}
	items := make([]model.Resource, 0, len(ids))
	for _, id := range ids {
		resource, getErr := resources.store.GetResource(ctx, id)
		if getErr == nil {
			items = append(items, resource)
		}
	}
	return items, nil
}

// NormalizeTag 清理单个标签的展示输入，并返回用于唯一性和查询的规范值。
func NormalizeTag(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || utf8.RuneCountInString(value) > 24 {
		return "", ErrInvalidTag
	}
	for _, character := range value {
		if unicode.IsControl(character) || !(unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("-_./", character)) {
			return "", ErrInvalidTag
		}
	}
	return value, nil
}

// NormalizeTags 校验标签数量、规范名称并去除同一资料内的重复标签。
func NormalizeTags(values []string) ([]string, error) {
	if len(values) > maxResourceTags {
		return nil, ErrTooManyTags
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized, err := NormalizeTag(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result, nil
}

// SetTags 校验后整体替换资料标签；显式替换让重试幂等，空数组表示清空。
func (resources *Resources) SetTags(ctx context.Context, id string, values []string) (model.Resource, error) {
	if !validResourceID(id) {
		return model.Resource{}, repository.ErrNotFound
	}
	tags, err := NormalizeTags(values)
	if err != nil {
		return model.Resource{}, err
	}
	resource, err := resources.store.ReplaceResourceTags(ctx, id, tags)
	if err == nil {
		resources.invalidateResourceCaches(ctx, id)
	}
	return resource, err
}

// ListTags 返回经过同一套规则规范化的标签建议，避免筛选输入与保存输入产生不同结果。
func (resources *Resources) ListTags(ctx context.Context, search string) ([]string, error) {
	if strings.TrimSpace(search) != "" {
		normalized, err := NormalizeTag(search)
		if err != nil {
			return nil, err
		}
		search = normalized
	}
	key := userCachePrefix(ctx) + "tags:" + search
	if resources.cache != nil {
		var cached []string
		if hit, err := resources.cache.Get(ctx, key, &cached); err == nil && hit {
			return cached, nil
		}
	}
	tags, err := resources.store.ListTags(ctx, search)
	if err != nil {
		return nil, err
	}
	if resources.cache != nil {
		_ = resources.cache.Set(ctx, key, tags, 60*time.Second)
	}
	return tags, nil
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
	// 详情可能用于打开原文件；不能从省略 StorageKey 的 JSON 缓存读取，也不能让软删除后的缓存绕过数据库可见性。
	resource, err := resources.store.GetResource(ctx, id)
	if err != nil {
		return model.Resource{}, err
	}
	if resources.cache != nil {
		_ = resources.cache.RecordRecent(ctx, id, time.Now().UTC(), 20)
	}
	return resource, nil
}

// SetFavorite 显式设置资料收藏状态；缺失或非法 ID 返回同一种未找到错误，重复设置保持幂等。
func (resources *Resources) SetFavorite(ctx context.Context, id string, favorite bool) (model.Resource, error) {
	if !validResourceID(id) {
		return model.Resource{}, repository.ErrNotFound
	}
	resource, err := resources.store.SetFavorite(ctx, id, favorite)
	if err == nil {
		resources.invalidateResourceCaches(ctx, id)
	}
	return resource, err
}

// NormalizeDisplayName 校验用户可见名称；名称不能成为路径或控制字符载体，但可以保留中文和常用文件符号。
func NormalizeDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || utf8.RuneCountInString(value) > 180 {
		return "", ErrInvalidName
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == '/' || character == '\\' {
			return "", ErrInvalidName
		}
	}
	return value, nil
}

// SetName 修改资料展示名而不改动原件和原始上传名，失败时保留数据库旧值。
func (resources *Resources) SetName(ctx context.Context, id, value string) (model.Resource, error) {
	if !validResourceID(id) {
		return model.Resource{}, repository.ErrNotFound
	}
	name, err := NormalizeDisplayName(value)
	if err != nil {
		return model.Resource{}, err
	}
	resource, err := resources.store.UpdateResourceName(ctx, id, name)
	if err == nil {
		resources.invalidateResourceCaches(ctx, id)
	}
	return resource, err
}

// Delete 先在数据库中软删除并解除标签，再清理原件；文件清理失败不会让已删除资料重新出现在列表。
func (resources *Resources) Delete(ctx context.Context, id string) error {
	if !validResourceID(id) {
		return repository.ErrNotFound
	}
	resource, err := resources.store.DeleteResource(ctx, id)
	if err != nil {
		return err
	}
	// 资料已经在 MySQL 中不可见，先失效缓存，避免文件清理失败时仍从 Redis 返回已删除资料。
	resources.invalidateResourceCaches(ctx, id)
	if resources.cache != nil {
		_ = resources.cache.RemoveRecent(ctx, id)
	}
	if err := os.Remove(filepath.Join(resources.dataDir, resource.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove deleted resource file: %w", err)
	}
	return nil
}

// invalidateResourceCaches 清理详情、列表和标签建议缓存；失效失败不改变已提交的业务结果。
func (resources *Resources) invalidateResourceCaches(ctx context.Context, id string) {
	if resources.cache == nil {
		return
	}
	prefix := userCachePrefix(ctx)
	_ = resources.cache.Delete(ctx, prefix+"resource:"+id)
	_ = resources.cache.DeleteByPrefix(ctx, prefix+"resource-list:")
	_ = resources.cache.DeleteByPrefix(ctx, prefix+"tags:")
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
