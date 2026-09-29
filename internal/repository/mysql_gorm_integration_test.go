package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

// TestMySQLSetFavorite 仅在显式提供隔离测试库时运行，验证 GORM 对重复布尔更新和缺失资料的真实行为。
func TestMySQLSetFavorite(t *testing.T) {
	dsn := os.Getenv("MIZUKI_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("设置 MIZUKI_TEST_MYSQL_DSN 指向隔离测试库后运行")
	}
	config, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(config.DBName, "mizuki_test_") {
		t.Fatal("测试 DSN 必须指向 mizuki_test_ 前缀的独立数据库")
	}
	database, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	// 清理回调只关闭本用例创建的连接，不删除测试库或其他资料。
	t.Cleanup(func() { _ = connection.Close() })
	ctx := context.Background()
	if err := NewMySQL(database).Migrate(ctx); err != nil {
		t.Fatalf("建立测试表失败: %v", err)
	}
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	// 清理回调撤销本用例插入的资料，测试表保留给后续集成测试复用。
	t.Cleanup(func() { _ = transaction.Rollback().Error })
	store := NewMySQL(transaction)
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(idBytes)
	resource := model.Resource{ID: id, Name: "example.txt", OriginalName: "example.txt", Kind: "text", MIME: "text/plain", StorageKey: id, SHA256: strings.Repeat("0", 64), CreatedAt: time.Now().UTC()}
	if err := store.SaveResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	for _, favorite := range []bool{true, true, false} {
		updated, err := store.SetFavorite(ctx, id, favorite)
		if err != nil || updated.Favorite != favorite {
			t.Fatalf("设置 favorite=%t 得到 %#v, %v", favorite, updated, err)
		}
	}
	if _, err := store.SetFavorite(ctx, strings.Repeat("f", 32), true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("缺失资料应返回 ErrNotFound，得到 %v", err)
	}
}
