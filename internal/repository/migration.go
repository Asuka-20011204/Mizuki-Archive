package repository

import _ "embed"

// 把初始建表语句打包进服务端，启动时无需依赖当前工作目录中的 SQL 文件。
//
//go:embed migrations/001_init.sql
var initialMigration string

//go:embed migrations/002_manual_tags.sql
var manualTagsMigration string

//go:embed migrations/003_soft_delete.sql
var softDeleteMigration string

//go:embed migrations/004_processing_jobs.sql
var processingJobsMigration string

//go:embed migrations/005_processing_outbox.sql
var processingOutboxMigration string

//go:embed migrations/006_processing_capacity.sql
var processingCapacityMigration string

//go:embed migrations/007_multi_user_identity.sql
var multiUserIdentityMigration string

//go:embed migrations/008_multi_user_compatibility.sql
var multiUserCompatibilityMigration string

//go:embed migrations/009_multi_user_identity_indexes.sql
var multiUserIdentityIndexesMigration string

// 外部资源卡片独立于上传文件，不允许其参与文件处理任务。
//
//go:embed migrations/010_external_resources.sql
var externalResourcesMigration string

// 收件箱整理状态独立于外部链接可用性；中断后可重试建列。
//
//go:embed migrations/011_inbox.sql
var inboxMigration string

// 大陆手机号账号与邮箱账号使用不同唯一索引和挑战表，迁移在 API 上线前执行。
//
//go:embed migrations/012_mainland_phone_identity.sql
var mainlandPhoneIdentityMigration string

// 重复提醒为现有文件哈希和外部位置摘要建立用户范围索引，不改变原件或卡片内容。
//
//go:embed migrations/013_duplicate_hints.sql
var duplicateHintsMigration string
