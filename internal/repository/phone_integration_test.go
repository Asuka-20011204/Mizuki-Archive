package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"mizuki-archive/internal/model"
)

// TestPhoneMySQLIdentityAndChallenge 验证独立测试库中的手机号唯一性、重发冷却和并发一次性消费。
func TestPhoneMySQLIdentityAndChallenge(t *testing.T) {
	database, store := openProcessingIsolationDatabase(t)
	ctx := context.Background()
	// 模拟 DDL 已完成但迁移版本尚未登记时崩溃，重新启动不得重复建列或索引。
	if err := database.Exec("DELETE FROM schema_migrations WHERE version = ?", 12).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("resume phone migration: %v", err)
	}
	phone := "+8613800138000"
	user, err := store.CreatePhoneUser(ctx, phone, "phoneuser", []byte("testhash"))
	if err != nil || user.Phone != phone || user.Email != "" {
		t.Fatalf("phone user=%#v err=%v", user, err)
	}
	if _, err := store.CreatePhoneUser(ctx, phone, "phoneuser", []byte("testhash")); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate phone: %v", err)
	}
	found, err := store.GetUserByPhone(ctx, phone)
	if err != nil || found.ID != user.ID {
		t.Fatalf("lookup phone: %#v %v", found, err)
	}
	now := time.Now().UTC()
	challenge := model.PhoneChallenge{Phone: phone, Purpose: "register", Digest: "digest-one", ExpiresAt: now.Add(10 * time.Minute), NextRequestAt: now.Add(time.Minute)}
	reserved, err := store.ReservePhoneChallenge(ctx, challenge, now)
	if err != nil || !reserved {
		t.Fatalf("reserve: %v %v", reserved, err)
	}
	reserved, err = store.ReservePhoneChallenge(ctx, challenge, now)
	if err != nil || reserved {
		t.Fatalf("cooldown: %v %v", reserved, err)
	}
	var wait sync.WaitGroup
	accepted := make(chan bool, 2)
	for range 2 {
		wait.Add(1)
		// 两条连接并发消费同一行，数据库行锁必须确保至多一次成功。
		go func() {
			defer wait.Done()
			ok, consumeErr := store.ConsumePhoneChallenge(ctx, phone, "register", "digest-one", now)
			if consumeErr != nil {
				t.Errorf("consume: %v", consumeErr)
			}
			accepted <- ok
		}()
	}
	wait.Wait()
	close(accepted)
	successes := 0
	for ok := range accepted {
		if ok {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful consumptions=%d", successes)
	}
}
