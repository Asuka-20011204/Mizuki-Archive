# 项目目录导览

> 这是当前代码的导航图，不是未来功能清单。V1 资料管理闭环已通过 Go 测试、构建和隔离 MySQL 迁移/持久化验证。

## 先找 `main`

- **API 服务入口：** `cmd/server/main.go` 中的 `func main()`。执行 `go run ./cmd/server`，它读取环境变量、连接 MySQL、装配 Repository/Service/Controller，并启动 HTTP 服务；本地默认允许自动迁移，生产编排由独立 `migrate` 程序先完成迁移。
- **任务 Worker 入口：** `cmd/worker/main.go` 中的 `func main()`；数据库或 RabbitMQ 模式由环境配置决定。
- **数据库迁移入口：** `cmd/migrate/main.go` 中的 `func main()`；使用 `MIGRATION_DSN` 执行版本化 SQL、初始化管理员并回填旧数据归属，完成后退出，不接收 HTTP 请求。
- **密码辅助命令：** `cmd/hash-password/main.go` 中另一个 `func main()`。执行 `go run ./cmd/hash-password` 只生成管理员密码的 bcrypt 哈希，**不会启动服务**。
- **浏览器入口：** `web/src/main.ts` 创建 Vue 应用并挂载 `App.vue`，不是 Go 的 `main` 函数。

Go 允许每个 `cmd/子目录` 各自作为一个可执行程序；因此根目录没有 `main.go`。运行命令时选择具体子目录即可。

## 当前目录树

```text
Mizuki Archive/
├── cmd/
│   ├── server/main.go                 # API 程序入口与依赖装配
│   ├── worker/main.go                 # 单进程持久任务 Worker 入口
│   ├── hash-password/main.go          # 交互式生成密码哈希
│   └── migrate/main.go                # 使用独立账号执行生产数据库迁移
├── internal/
│   ├── model/resource.go              # 资料模型、用户归属与列表筛选条件
│   ├── model/external_resource.go     # 站外资源卡片模型（不含原件）
│   ├── model/duplicate.go             # 纯文本链接规范化与同账号重复提示模型
│   ├── model/search.go                # 组合筛选、视图和分来源搜索结果模型
│   ├── model/user.go                  # 多用户身份模型
│   ├── model/phone.go                 # 大陆手机号一次性挑战模型
│   ├── model/inbox.go                 # 批量整理条目的来源与 ID
│   ├── model/processing.go            # 任务与派生产物模型
│   ├── processing/text.go             # PDF/TXT/Markdown 文本处理器
│   ├── processing/thumbnail.go        # PNG/JPEG/WebP 缩略图处理器与像素边界
│   ├── cache/redis.go                 # Redis 短缓存、最近访问和共享限流
│   ├── notification/smtp.go           # 强制 TLS 的验证码邮件发送适配器
│   ├── queue/rabbitmq.go              # 持久消息、发布确认和消费 ACK
│   ├── controller/
│   │   ├── router.go                  # Gin 路由、Origin 校验、统一错误格式
│   │   ├── auth_controller.go         # 登录、会话中间件、退出
│   │   ├── phone_auth_controller.go   # 手机验证码 HTTP 边界，按能力注册路由
│   │   ├── resource_controller.go     # 资料上传、查询、标签、预览、删除和下载
│   │   ├── external_resource_controller.go # 站外卡片 CRUD HTTP 边界
│   │   ├── inbox_controller.go         # 待整理列表与状态更新 HTTP 边界
│   │   ├── search_controller.go       # 私有统一搜索与参数校验 HTTP 边界
│   │   ├── saved_search_controller.go # 私有检索视图 CRUD HTTP 边界
│   │   └── processing_controller.go   # 任务创建、任务查看和派生文件下载
│   ├── service/
│   │   ├── auth.go                    # 兼容密码校验、用户会话和身份上下文
│   │   ├── email_auth.go              # 邮箱验证码注册/登录
│   │   ├── phone_auth.go              # 大陆手机号验证码业务；发送器未接入
│   │   ├── resource.go                # 文件校验、存储、元数据流程
│   │   ├── external_resource.go       # 卡片校验、人工状态与身份约束
│   │   ├── inbox.go                   # 整理状态、分页与用户身份约束
│   │   ├── search.go                  # 搜索输入限制、来源分组与分页
│   │   ├── saved_search.go            # 视图名称、条件和随机 ID 校验
│   │   └── processing.go              # 任务幂等、Worker 执行和派生文件
│   └── repository/
│       ├── repository.go              # 资料/会话/处理任务持久化接口
│       ├── mysql_gorm.go              # GORM 的 MySQL 资料实现
│       ├── external_resource.go       # 按用户限定的卡片与标签事务
│       ├── inbox.go                   # 两类待整理查询、可重试迁移与用户范围更新
│       ├── processing.go              # GORM 的任务与派生产物实现
│       ├── outbox.go                  # 事务 Outbox 及孤儿任务补偿
│       ├── migration.go               # 内嵌版本化 SQL 迁移
│       ├── email.go                   # 用户、验证码与会话归属持久化
│       ├── phone.go                   # 手机号唯一身份、挑战和可重试迁移准备
│       ├── duplicate.go               # 同账号查重、用户范围索引和旧卡片回填
│       ├── search.go                  # 文件与卡片的用户范围关键词查询
│       ├── saved_search.go            # 视图归属、上限和唯一名称持久化
│       └── migrations/
│           ├── 001_init.sql           # 资料与会话表
│           ├── 002_manual_tags.sql    # 标签与资料关联
│           ├── 003_soft_delete.sql    # 软删除字段与索引
│           ├── 004_processing_jobs.sql # 持久任务与派生产物
│           ├── 005_processing_outbox.sql # 事件表与旧任务回填
│           ├── 006_processing_capacity.sql # 全局任务容量锁
│           ├── 007_multi_user_identity.sql # 用户、验证码、资料/会话归属
│           ├── 008_multi_user_compatibility.sql # 兼容已应用旧版本迁移
│           ├── 009_multi_user_identity_indexes.sql # 可重试的用户范围索引迁移
│           ├── 010_external_resources.sql # 卡片与独立标签表
│           ├── 011_inbox.sql          # 整理状态迁移登记，DDL 在迁移锁内按需执行
│           ├── 012_mainland_phone_identity.sql # 手机挑战表；用户列和索引可重试创建
│           ├── 013_duplicate_hints.sql # 查重迁移登记；DDL 和回填由准备函数执行
│           └── 014_saved_search_views.sql # 每用户保存组合检索条件
├── web/                               # Vue 3 + TypeScript View
│   ├── src/SearchPanel.vue            # 组合筛选、保存视图与来源分页
│   ├── src/search-filter.ts           # 前端筛选预校验（服务端最终校验）
│   ├── src/InboxPanel.vue             # 统一查看两类待整理条目
│   ├── src/ExternalResourcePanel.vue  # 独立卡片录入、搜索和人工维护
│   ├── src/share-parser.ts            # 浏览器内解析粘贴文本，不发网络请求
│   ├── tests/share-parser.test.mjs    # Node 内置测试运行器校验解析与危险输入
│   └── tests/search-filter.test.mjs   # 检索条件长度、空条件及控制字符校验
├── docs/                              # 产品、架构、安全、设计和变更记录
├── compose.yaml                       # MySQL 与可选 RabbitMQ/Redis 容器
├── compose.deploy.yaml                # V6 独立单机编排：Web/API/Worker/MySQL
├── compose.tls.yaml                   # V14 可选 TLS 入口覆盖（证书只读挂载）
├── compose.secrets.yaml               # V14 可选 Docker Secrets 覆盖
├── Dockerfile                         # Go API、Worker 与 migrate 的非 root 镜像
├── web/Dockerfile、web/nginx*.conf     # Vue 静态构建、HTTP/TLS 同源反向代理和入口限流
├── .env.example                       # 仅示例变量，真实 .env 不入库
├── scripts/generate-mailpit-cert.ps1   # 生成仓库外的本地 SMTP 测试证书
├── scripts/scan-images.ps1             # Trivy 镜像漏洞门禁
└── go.mod / go.sum                    # Go 模块与依赖校验
```

`web/node_modules/`、`web/dist/`、`data/`、`.env` 和 Docker 的数据库卷是依赖或运行时产物，不是需要阅读或提交的业务源码。`AGENTS.md` 是本机协作规则，已由 `.gitignore` 排除，不进入 Git 提交历史。

## 一条请求怎么走

```text
浏览器 App.vue → api.ts → Gin router.go
                          ├── auth_controller.go → service/auth.go/email_auth.go → repository/email.go → MySQL users/sessions
                          └── resource_controller.go → service/resource.go
                                                        ├── 本地 data/files（文件原件）
                                                        └── repository/mysql_gorm.go → MySQL resources（按会话 user_id 过滤）
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
| 独立 Docker 部署/恢复 | `compose.deploy.yaml`、`compose.tls.yaml`、`compose.secrets.yaml`、`Dockerfile`、`web/nginx*.conf` | `docs/deployment.md`、`docs/security.md` |
| 邮箱本地验收 | `compose.yaml` 的 `mailpit` profile、`internal/notification/smtp.go` | `docs/local-development.md`、`docs/roadmap.md` |
| V7 固定负载复现 | `internal/service/processing_benchmark_integration_test.go`、`internal/controller/performance_integration_test.go` | `docs/changes/2026-09-29-v7-fixed-workload.md` |

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
