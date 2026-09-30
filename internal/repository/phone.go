package repository

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"mizuki-archive/internal/model"
)

// ensurePhoneIdentitySchema 分别检查可空手机号列和唯一索引，兼容中断后的迁移重试。
func ensurePhoneIdentitySchema(connection *gorm.DB) error {
	rows, err := connection.Raw(`SELECT column_type, character_set_name, collation_name, is_nullable FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'users' AND column_name = 'phone'`).Rows()
	if err != nil {
		return fmt.Errorf("check users.phone: %w", err)
	}
	exists := rows.Next()
	if exists {
		var columnType, charset, collation, nullable string
		if err := rows.Scan(&columnType, &charset, &collation, &nullable); err != nil {
			rows.Close()
			return fmt.Errorf("read users.phone: %w", err)
		}
		if !strings.EqualFold(columnType, "varchar(14)") || !strings.EqualFold(charset, "ascii") || !strings.EqualFold(collation, "ascii_bin") || !strings.EqualFold(nullable, "YES") {
			rows.Close()
			return errors.New("incompatible users.phone column")
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate users.phone: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close users.phone query: %w", err)
	}
	if !exists {
		if err := connection.Exec("ALTER TABLE users ADD COLUMN phone VARCHAR(14) CHARACTER SET ascii COLLATE ascii_bin NULL").Error; err != nil {
			return fmt.Errorf("add users.phone: %w", err)
		}
	}
	indexRows, err := connection.Raw(`SELECT column_name, non_unique FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'users' AND index_name = 'uq_users_phone'`).Rows()
	if err != nil {
		return fmt.Errorf("check users.phone index: %w", err)
	}
	indexExists := indexRows.Next()
	if indexExists {
		var column string
		var nonUnique int
		if err := indexRows.Scan(&column, &nonUnique); err != nil {
			indexRows.Close()
			return fmt.Errorf("read users.phone index: %w", err)
		}
		if column != "phone" || nonUnique != 0 || indexRows.Next() {
			indexRows.Close()
			return errors.New("incompatible users.phone unique index")
		}
	}
	if err := indexRows.Err(); err != nil {
		indexRows.Close()
		return fmt.Errorf("iterate users.phone index: %w", err)
	}
	if err := indexRows.Close(); err != nil {
		return fmt.Errorf("close users.phone index query: %w", err)
	}
	if !indexExists {
		if err := connection.Exec("ALTER TABLE users ADD UNIQUE KEY uq_users_phone (phone)").Error; err != nil {
			return fmt.Errorf("add users.phone index: %w", err)
		}
	}
	return nil
}

// CreatePhoneUser 使用数据库唯一索引限制同号多次注册，并将手机号设为展示用户名。
func (store *MySQL) CreatePhoneUser(ctx context.Context, phone string) (model.User, error) {
	id, err := newUserID()
	if err != nil {
		return model.User{}, err
	}
	row := userRow{ID: id, Username: phone, Phone: &phone, CreatedAt: time.Now().UTC()}
	if err := store.db.WithContext(ctx).Table("users").Create(&row).Error; err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return model.User{}, ErrUserExists
		}
		return model.User{}, fmt.Errorf("create phone user: %w", err)
	}
	return userFromRow(row), nil
}

// GetUserByPhone 按统一存储的 +86 格式查找用户，不返回跨用户资料。
func (store *MySQL) GetUserByPhone(ctx context.Context, phone string) (model.User, error) {
	var row userRow
	err := store.db.WithContext(ctx).Table("users").Where("phone = ?", phone).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("get phone user: %w", err)
	}
	return userFromRow(row), nil
}

// phoneChallengeRow 只映射摘要、尝试次数和时限，不持久化短信明文。
type phoneChallengeRow struct {
	Phone         string    `gorm:"column:phone;primaryKey"`
	Purpose       string    `gorm:"column:purpose;primaryKey"`
	Digest        string    `gorm:"column:digest"`
	Attempts      uint8     `gorm:"column:attempts"`
	ExpiresAt     time.Time `gorm:"column:expires_at"`
	NextRequestAt time.Time `gorm:"column:next_request_at"`
}

// ReservePhoneChallenge 利用数据库原子冲突更新实现多实例的单号码重发冷却。
func (store *MySQL) ReservePhoneChallenge(ctx context.Context, value model.PhoneChallenge, now time.Time) (bool, error) {
	result := store.db.WithContext(ctx).Exec(`INSERT INTO phone_challenges
		(phone, purpose, digest, attempts, expires_at, next_request_at) VALUES (?, ?, ?, 0, ?, ?)
		ON DUPLICATE KEY UPDATE
		digest = IF(next_request_at <= ?, VALUES(digest), digest),
		attempts = IF(next_request_at <= ?, 0, attempts),
		expires_at = IF(next_request_at <= ?, VALUES(expires_at), expires_at),
		next_request_at = IF(next_request_at <= ?, VALUES(next_request_at), next_request_at)`,
		value.Phone, value.Purpose, value.Digest, value.ExpiresAt, value.NextRequestAt, now, now, now, now)
	if result.Error != nil {
		return false, fmt.Errorf("reserve phone challenge: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

// ConsumePhoneChallenge 使用行锁限制错误尝试，成功后删除挑战以阻止并发重放。
func (store *MySQL) ConsumePhoneChallenge(ctx context.Context, phone, purpose, digest string, now time.Time) (bool, error) {
	accepted := false
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row phoneChallengeRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("phone_challenges").Where("phone = ? AND purpose = ?", phone, purpose).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !row.ExpiresAt.After(now) || row.Attempts >= 5 {
			return tx.Table("phone_challenges").Where("phone = ? AND purpose = ?", phone, purpose).Delete(&phoneChallengeRow{}).Error
		}
		if subtle.ConstantTimeCompare([]byte(row.Digest), []byte(digest)) == 1 {
			if err := tx.Table("phone_challenges").Where("phone = ? AND purpose = ?", phone, purpose).Delete(&phoneChallengeRow{}).Error; err != nil {
				return err
			}
			accepted = true
			return nil
		}
		return tx.Table("phone_challenges").Where("phone = ? AND purpose = ?", phone, purpose).Update("attempts", row.Attempts+1).Error
	})
	if err != nil {
		return false, fmt.Errorf("consume phone challenge: %w", err)
	}
	return accepted, nil
}

// DeletePhoneChallenge 仅在发送失败且摘要仍匹配时删除，避免删除并发新挑战。
func (store *MySQL) DeletePhoneChallenge(ctx context.Context, phone, purpose, digest string) error {
	if err := store.db.WithContext(ctx).Table("phone_challenges").Where("phone = ? AND purpose = ? AND digest = ?", phone, purpose, digest).Delete(&phoneChallengeRow{}).Error; err != nil {
		return fmt.Errorf("delete phone challenge: %w", err)
	}
	return nil
}
