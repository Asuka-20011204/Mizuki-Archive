package repository

import _ "embed"

// 把初始建表语句打包进服务端，启动时无需依赖当前工作目录中的 SQL 文件。
//
//go:embed migrations/001_init.sql
var initialMigration string
