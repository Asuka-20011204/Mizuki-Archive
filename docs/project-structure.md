# 项目目录导览

> 这是当前代码的导航图，不是未来功能清单。V1 资料管理闭环已通过 Go 测试、构建和隔离 MySQL 迁移/持久化验证。

## 先找 `main`

- **API 服务入口：** `cmd/server/main.go` 中的 `func main()`。执行 `go run ./cmd/server`，它读取环境变量、连接 MySQL、运行当前初始迁移、创建 Repository/Service/Controller，并启动 HTTP 服务。
- **任务 Worker 入口：** `cmd/worker/main.go` 中的 `func main()`；数据库或 RabbitMQ 模式由环境配置决定。
- **密码辅助命令：** `cmd/hash-password/main.go` 中另一个 `func main()`。执行 `go run ./cmd/hash-password` 只生成管理员密码的 bcrypt 哈希，**不会启动服务**。
- **浏览器入口：** `web/src/main.ts` 创建 Vue 应用并挂载 `App.vue`，不是 Go 的 `main` 函数。

Go 允许每个 `cmd/子目录` 各自作为一个可执行程序；因此根目录没有 `main.go`。运行命令时选择具体子目录即可。

## 当前目录树

```text
Mizuki Archive/
├── cmd/
│   ├── server/main.go                 # API 程序入口与依赖装配
│   ├── worker/main.go                 # 单进程持久任务 Worker 入口
│   └── hash-password/main.go          # 交互式生成密码哈希
├── internal/
│   ├── model/resource.go              # 资料模型与列表筛选条件
│   ├── model/processing.go            # 任务与派生产物模型
│   ├── processing/text.go             # PDF/TXT/Markdown 文本处理器
│   ├── processing/thumbnail.go        # PNG/JPEG/WebP 缩略图处理器与像素边界
│   ├── cache/redis.go                 # Redis 短缓存、最近访问和共享限流
│   ├── queue/rabbitmq.go              # 持久消息、发布确认和消费 ACK
│   ├── controller/
│   │   ├── router.go                  # Gin 路由、Origin 校验、统一错误格式
│   │   ├── auth_controller.go         # 登录、会话中间件、退出
│   │   ├── resource_controller.go     # 资料上传、查询、标签、预览、删除和下载
│   │   └── processing_controller.go   # 任务创建、任务查看和派生文件下载
│   ├── service/
│   │   ├── auth.go                    # 密码校验和可撤销会话
│   │   ├── resource.go                # 文件校验、存储、元数据流程
│   │   └── processing.go              # 任务幂等、Worker 执行和派生文件
│   └── repository/
│       ├── repository.go              # 资料/会话/处理任务持久化接口
│       ├── mysql_gorm.go              # GORM 的 MySQL 资料实现
│       ├── processing.go              # GORM 的任务与派生产物实现
│       ├── outbox.go                  # 事务 Outbox 及孤儿任务补偿
│       ├── migration.go               # 内嵌版本化 SQL 迁移
│       └── migrations/
│           ├── 001_init.sql           # 资料与会话表
│           ├── 002_manual_tags.sql    # 标签与资料关联
│           ├── 003_soft_delete.sql    # 软删除字段与索引
│           ├── 004_processing_jobs.sql # 持久任务与派生产物
│           ├── 005_processing_outbox.sql # 事件表与旧任务回填
│           └── 006_processing_capacity.sql # 全局任务容量锁
├── web/                               # Vue 3 + TypeScript View
├── docs/                              # 产品、架构、安全、设计和变更记录
├── compose.yaml                       # MySQL 与可选 RabbitMQ/Redis 容器
├── .env.example                       # 仅示例变量，真实 .env 不入库
└── go.mod / go.sum                    # Go 模块与依赖校验
```

`web/node_modules/`、`web/dist/`、`data/`、`.env` 和 Docker 的数据库卷是依赖或运行时产物，不是需要阅读或提交的业务源码。

## 一条请求怎么走

```text
浏览器 App.vue → api.ts → Gin router.go
                          ├── auth_controller.go → service/auth.go → repository/mysql_gorm.go → MySQL sessions
                          └── resource_controller.go → service/resource.go
                                                        ├── 本地 data/files（文件原件）
                                                        └── repository/mysql_gorm.go → MySQL resources
```

**以上传为例：** `web/src/App.vue` 接受文件，`web/src/api.ts` 发送 `POST /api/resources`；`router.go` 的私有路由先检查会话，`resource_controller.go` 解析 multipart 和 HTTP 错误，`service/resource.go` 校验格式/大小、流式落盘、生成 ID 和哈希，再通过 `repository/mysql_gorm.go` 写入资料元数据。数据库写入失败时 Service 尝试删除已移动的文件。由于文件系统和 MySQL 不是同一个事务，异常崩溃后的孤儿文件巡检仍是后续工作；备份与恢复必须同时覆盖两者，具体步骤见 [备份恢复演练](backup-restore.md)。

**以登录为例：** `auth_controller.go` 限制请求体和尝试次数，`service/auth.go` 验证密码并生成随机令牌，Repository 只保存令牌哈希；浏览器通过 HttpOnly Cookie 携带原令牌。`router.go` 对需要认证的路由检查会话和请求来源。

## 开发时从哪里改

| 想改什么 | 先读哪里 | 通常还要同步 |
| --- | --- | --- |
| 页面布局与文案 | `web/src/App.vue`、`web/src/styles/` | `docs/design.md` |
| 新增一个资料 HTTP 接口 | `internal/controller/router.go`、`resource_controller.go` | Service 测试、`docs/architecture.md` |
| 文件格式/上传限制 | `internal/service/resource.go` | Controller 测试、`docs/security.md` |
| 数据库字段/查询 | `internal/model/resource.go`、`internal/repository/mysql_gorm.go`、`migrations/` | 迁移/回滚设计、`docs/architecture.md`、`docs/backup-restore.md` |
| 本地启动配置 | `cmd/server/main.go`、`.env.example`、`compose.yaml` | `README.md` |

不建议在 `main()` 中写业务逻辑，也不应在 Controller 中直接写 GORM 查询。中文注释重点解释安全边界、失败补偿和架构取舍；简单赋值不逐行复述。

## V2 处理请求路径

```text
详情页 → api.ts → processing_controller.go
                  → service/processing.go → repository/processing.go → MySQL processing_jobs
                                                                            ↓
cmd/worker → service/processing.go → processing/text.go / thumbnail.go → data/files 原件
                                             ↓
                                     data/derived 派生文本/缩略图 + derived_assets 检索索引
```

`cmd/server` 与 `cmd/worker` 是两个独立的 Go 程序入口，但共享 Model、Service、Repository 和迁移；它们必须指向同一个 MySQL 数据库和 `APP_DATA_DIR`。
