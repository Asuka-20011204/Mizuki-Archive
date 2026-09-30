package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

const portableExportMaxBytes = 32 << 20

var ErrPortableExportIdentity = errors.New("missing verified export identity")
var ErrPortableExportLimit = errors.New("portable export is too large")

// PortableExport 只负责导出格式和大小边界，持久化读取由 Repository 限定账号。
type PortableExport struct {
	store repository.PortableExportStore
}

// NewPortableExport 拒绝缺少仓储的配置，避免开放一个永远返回空内容的下载入口。
func NewPortableExport(store repository.PortableExportStore) (*PortableExport, error) {
	if store == nil {
		return nil, errors.New("missing portable export store")
	}
	return &PortableExport{store: store}, nil
}

// Create 在有身份的请求中生成版本化 JSON；超限直接报错，绝不返回不完整清单。
func (exporter *PortableExport) Create(ctx context.Context) ([]byte, error) {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return nil, ErrPortableExportIdentity
	}
	archive, err := exporter.store.ExportSnapshot(ctx)
	if errors.Is(err, repository.ErrPortableExportLimit) {
		return nil, ErrPortableExportLimit
	}
	if err != nil {
		return nil, err
	}
	archive.Format = "mizuki-archive-portable"
	archive.Version = 1
	archive.ExportedAt = time.Now().UTC()
	if archive.Files == nil {
		archive.Files = []model.Resource{}
	}
	if archive.ExternalResources == nil {
		archive.ExternalResources = []model.ExternalResource{}
	}
	if archive.Notes == nil {
		archive.Notes = []model.ResourceNote{}
	}
	if archive.Relations == nil {
		archive.Relations = []model.PortableRelation{}
	}
	content, err := json.MarshalIndent(archive, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(content)+1 > portableExportMaxBytes {
		return nil, ErrPortableExportLimit
	}
	return append(content, '\n'), nil
}
