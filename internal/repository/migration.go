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
