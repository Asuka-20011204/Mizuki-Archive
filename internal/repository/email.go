package repository

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"mizuki-archive/internal/model"
)

// userRow 隔离 GORM 用户字段，密码哈希不会被 JSON 模型意外暴露。
type userRow struct {
	ID           string    `gorm:"column:id;primaryKey"`
	Username     string    `gorm:"column:username"`
	Email        *string   `gorm:"column:email"`
	PasswordHash []byte    `gorm:"column:password_hash"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

// userFromRow 将数据库用户转换为跨层模型，并把可空旧邮箱映射为空字符串。
func userFromRow(row userRow) model.User {
	email := ""
	if row.Email != nil {
		email = *row.Email
	}
	return model.User{ID: row.ID, Username: row.Username, Email: email, PasswordHash: append([]byte(nil), row.PasswordHash...), CreatedAt: row.CreatedAt}
}

// newUserID 生成不携带邮箱或用户名含义的用户主键。
func newUserID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate user ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

// EnsureAdminUser 将旧环境变量管理员纳入用户表，并返回其稳定归属 ID。
func (store *MySQL) EnsureAdminUser(ctx context.Context, username string, passwordHash []byte) (model.User, error) {
	var row userRow
	err := store.db.WithContext(ctx).Table("users").Where("username = ?", username).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		id, idErr := newUserID()
		if idErr != nil {
			return model.User{}, idErr
		}
		// 旧管理员暂时没有验证邮箱，使用保留域名占位；真实邮箱注册用户走 CreateUser。
		email := username + "@local.invalid"
		row = userRow{ID: id, Username: username, Email: &email, PasswordHash: append([]byte(nil), passwordHash...), CreatedAt: time.Now().UTC()}
		if createErr := store.db.WithContext(ctx).Table("users").Create(&row).Error; createErr != nil {
			return model.User{}, fmt.Errorf("create admin user: %w", createErr)
		}
	} else if err != nil {
		return model.User{}, fmt.Errorf("get admin user: %w", err)
	}
	return userFromRow(row), nil
}

// CreateUser 创建一个已通过邮箱验证码验证的新用户；重复邮箱由唯一约束拒绝。
func (store *MySQL) CreateUser(ctx context.Context, email string) (model.User, error) {
	id, err := newUserID()
	if err != nil {
		return model.User{}, err
	}
	row := userRow{ID: id, Username: email, Email: &email, CreatedAt: time.Now().UTC()}
	if err := store.db.WithContext(ctx).Table("users").Create(&row).Error; err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return model.User{}, ErrUserExists
		}
		return model.User{}, fmt.Errorf("create user: %w", err)
	}
	return userFromRow(row), nil
}

// GetUserByUsername 只读取已由迁移命令创建的管理员身份，运行时 API 不再尝试写入用户表。
func (store *MySQL) GetUserByUsername(ctx context.Context, username string) (model.User, error) {
	var row userRow
	err := store.db.WithContext(ctx).Table("users").Where("username = ?", username).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("get user by username: %w", err)
	}
	return userFromRow(row), nil
}

// GetUserByEmail 按规范化邮箱读取用户，缺失时统一返回 ErrNotFound。
func (store *MySQL) GetUserByEmail(ctx context.Context, email string) (model.User, error) {
	var row userRow
	err := store.db.WithContext(ctx).Table("users").Where("email = ?", email).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("get user by email: %w", err)
	}
	return userFromRow(row), nil
}

// GetUserByID 按服务端会话归属读取用户展示信息，缺失时不返回任何默认账号。
func (store *MySQL) GetUserByID(ctx context.Context, id string) (model.User, error) {
	var row userRow
	err := store.db.WithContext(ctx).Table("users").Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("get user by ID: %w", err)
	}
	return userFromRow(row), nil
}

// SaveUserSession 保存带用户归属的会话摘要；原始 Cookie 永远不写入数据库。
func (store *MySQL) SaveUserSession(ctx context.Context, userID, hash string, expires time.Time) error {
	row := sessionRow{TokenHash: hash, UserID: userID, ExpiresAt: expires}
	if err := store.db.WithContext(ctx).Table("sessions").Create(&row).Error; err != nil {
		return fmt.Errorf("insert user session: %w", err)
	}
	return nil
}

// GetSessionUser 同时校验会话截止时间并返回归属用户，过期或旧无归属会话均不可访问私有资源。
func (store *MySQL) GetSessionUser(ctx context.Context, hash string) (string, bool, error) {
	var row sessionRow
	err := store.db.WithContext(ctx).Table("sessions").Where("token_hash = ? AND user_id IS NOT NULL AND expires_at > ?", hash, time.Now().UTC()).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get session user: %w", err)
	}
	return row.UserID, true, nil
}

// FinalizeOwnership 把旧管理员资料和旧会话迁移到管理员用户，并在回填后收紧资料归属约束。
func (store *MySQL) FinalizeOwnership(ctx context.Context, userID string) error {
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE resources SET user_id = ? WHERE user_id IS NULL", userID).Error; err != nil {
			return fmt.Errorf("backfill resource owners: %w", err)
		}
		if err := tx.Exec("UPDATE sessions SET user_id = ? WHERE user_id IS NULL", userID).Error; err != nil {
			return fmt.Errorf("backfill session owners: %w", err)
		}
		if err := tx.Exec("ALTER TABLE resources MODIFY user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL").Error; err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return fmt.Errorf("require resource owners: %w", err)
		}
		return nil
	})
}

// emailChallengeRow 只映射验证码摘要和时间窗口，明文验证码不进入数据库。
type emailChallengeRow struct {
	Email         string    `gorm:"column:email;primaryKey"`
	Purpose       string    `gorm:"column:purpose;primaryKey"`
	Digest        string    `gorm:"column:digest"`
	Attempts      uint8     `gorm:"column:attempts"`
	ExpiresAt     time.Time `gorm:"column:expires_at"`
	NextRequestAt time.Time `gorm:"column:next_request_at"`
}

// ReserveEmailChallenge 原子检查重发冷却期；并发请求不会绕过同一邮箱/用途的限频。
func (store *MySQL) ReserveEmailChallenge(ctx context.Context, challenge model.EmailChallenge, now time.Time) (bool, error) {
	result := store.db.WithContext(ctx).Exec(`INSERT INTO email_challenges
		(email, purpose, digest, attempts, expires_at, next_request_at) VALUES (?, ?, ?, 0, ?, ?)
		ON DUPLICATE KEY UPDATE
		digest = IF(next_request_at <= ?, VALUES(digest), digest),
		attempts = IF(next_request_at <= ?, 0, attempts),
		expires_at = IF(next_request_at <= ?, VALUES(expires_at), expires_at),
		next_request_at = IF(next_request_at <= ?, VALUES(next_request_at), next_request_at)`,
		challenge.Email, challenge.Purpose, challenge.Digest, challenge.ExpiresAt, challenge.NextRequestAt,
		now, now, now, now)
	if result.Error != nil {
		return false, fmt.Errorf("reserve email challenge: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

// ConsumeEmailChallenge 在行锁内检查截止时间和尝试次数，成功后删除，保证并发最多消费一次。
func (store *MySQL) ConsumeEmailChallenge(ctx context.Context, email, purpose, digest string, now time.Time) (bool, error) {
	accepted := false
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row emailChallengeRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("email_challenges").Where("email = ? AND purpose = ?", email, purpose).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !row.ExpiresAt.After(now) || row.Attempts >= 5 {
			return tx.Table("email_challenges").Where("email = ? AND purpose = ?", email, purpose).Delete(&emailChallengeRow{}).Error
		}
		if subtle.ConstantTimeCompare([]byte(row.Digest), []byte(digest)) == 1 {
			if err := tx.Table("email_challenges").Where("email = ? AND purpose = ?", email, purpose).Delete(&emailChallengeRow{}).Error; err != nil {
				return err
			}
			accepted = true
			return nil
		}
		return tx.Table("email_challenges").Where("email = ? AND purpose = ?", email, purpose).Update("attempts", row.Attempts+1).Error
	})
	if err != nil {
		return false, fmt.Errorf("consume email challenge: %w", err)
	}
	return accepted, nil
}

// DeleteEmailChallenge 只删除对应摘要的未发送挑战，避免发送失败清掉并发产生的新验证码。
func (store *MySQL) DeleteEmailChallenge(ctx context.Context, email, purpose, digest string) error {
	if err := store.db.WithContext(ctx).Table("email_challenges").Where("email = ? AND purpose = ? AND digest = ?", email, purpose, digest).Delete(&emailChallengeRow{}).Error; err != nil {
		return fmt.Errorf("delete email challenge: %w", err)
	}
	return nil
}
