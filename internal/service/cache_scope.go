package service

import (
	"context"

	"mizuki-archive/internal/repository"
)

// userCachePrefix 为 Redis 业务键添加服务端用户命名空间；缺少身份时不复用任何用户缓存。
func userCachePrefix(ctx context.Context) string {
	if userID, ok := repository.UserIDFromContext(ctx); ok {
		return "user:" + userID + ":"
	}
	return "system:"
}
