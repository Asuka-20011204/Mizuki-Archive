# 本地启动与浏览器实测（Windows PowerShell）

以下命令从项目的仓库根目录开始。基础资料管理准备 **三个 PowerShell 终端**：Docker/数据库、Go API、Vue 开发服务器；如果要执行文本提取或缩略图，再增加第四个 Worker 终端。Go API 和 Worker 会自动读取根目录 `.env`，系统环境变量优先于文件中的同名值。不要把密码、哈希或个人文件提交到 Git；示例值要改成本机独有值。

## 启动模式总览

| 目标 | 必须启动 | 不需要启动 |
| --- | --- | --- |
| 上传、搜索、预览、下载 | MySQL、Go API、Vue | Worker、RabbitMQ、Redis、SMTP |
| 文本提取、图片缩略图 | 上一行全部 + `go run ./cmd/worker` | RabbitMQ、Redis、SMTP |
| RabbitMQ 任务模式 | MySQL、RabbitMQ、API、Worker | 真实 SMTP；Redis 仍是可选 |
| 邮箱注册/登录 | MySQL、API、Vue + 真实 SMTP，或本地 Mailpit | RabbitMQ、Redis |
| 手机号注册/登录 | 当前版本暂无可运行的短信发送器 | 不能通过启动某个现有容器启用 |

默认任务投递模式是 `database`，所以 `docker compose up -d mysql` 不会自动启动 RabbitMQ、Redis 或 Mailpit，也不会让它们成为基本功能的前置依赖。

## 可选 V3/V4 服务

默认 `docker compose up -d mysql` 只启动 MySQL。启用 RabbitMQ 前，在 `.env` 设置独立的 `RABBITMQ_USER`、`RABBITMQ_PASSWORD`、`RABBITMQ_URL`，然后运行 `docker compose --profile v3 up -d rabbitmq`。API 与 Worker 都必须读取 `PROCESSING_DELIVERY_MODE=rabbit`；只运行一个任务模式的 Worker。回退时先停止 RabbitMQ Worker，将 API 和 Worker 改为 `database` 后重启，Outbox 数据仍保留在 MySQL。

启用 Redis 前，在 `.env` 设置非空 `REDIS_PASSWORD` 和一致的 `REDIS_URL`，运行 `docker compose --profile v4 up -d redis`。默认主机端口是 `6381`（容器内 `6379`），如需调整 `REDIS_HOST_PORT` 必须同步 URL。Redis 停止后缓存回源 MySQL，但跨进程限流不再保证。不要把 `.env`、真实个人资料或容器卷提交 Git。

如果三个可选容器都要启动，可在配置完成后执行：

```powershell
docker compose --profile v3 --profile v4 --profile email up -d rabbitmq redis mailpit
docker compose --profile v3 --profile v4 --profile email ps
```

这条命令不会替代 API、Worker 和 Vue 的启动；它只启动容器。真实 SMTP 是外部服务，不会由 Docker 自动创建，Mailpit 仅用于本机捕获测试邮件。

浏览器仍打开 `http://localhost:5173/`：登录、上传可处理资料、进入详情并手动发起任务，观察状态、派生产物查看/下载和关键词搜索。RabbitMQ/Redis 后台能力不能仅靠页面成功展示证明断线、重复消息和故障降级已经验收。

## 1. 准备 MySQL

启动 Docker Desktop，确认 `docker info` 成功。在仓库根目录执行：

```powershell
if (-not (Test-Path .env)) { Copy-Item .env.example .env }
notepad .env
docker compose up -d mysql
docker compose ps
```

将 `.env` 中的 `MYSQL_PASSWORD`、`MYSQL_ROOT_PASSWORD` 换成不同的强随机密码；默认数据库名和普通用户名都是 `archive`，Docker 主机端口为 `3307`（容器内部端口仍为 `3306`）。如需调整主机端口，修改 `MYSQL_HOST_PORT`，并同步修改 API 的 `MYSQL_DSN`。等待 `docker compose ps` 显示 `healthy`。**不要运行 `docker compose down -v`**，它会删除数据库卷。`.env` 已忽略，但提交前仍要检查暂存区。

## 2. 启动 Go API

在仓库根目录的 API 终端先执行 `go run ./cmd/hash-password`。输入你选择的管理员明文密码（至少 12 字节；终端不回显），将输出的 bcrypt 哈希用单引号包住后粘贴到 `.env` 的 `APP_ADMIN_PASSWORD_HASH`，例如 `APP_ADMIN_PASSWORD_HASH='$2a$10$...'`。bcrypt 哈希包含 `$`，不能裸写。浏览器登录时使用的是**原来的明文密码**，不是这段哈希。

然后直接启动服务：

```powershell
go run ./cmd/server
```

`.env` 中的 `MYSQL_DSN` 端口必须与 `MYSQL_HOST_PORT` 一致，密码也需与数据库初始化时使用的密码一致；特殊字符可能需要按 Go MySQL DSN 格式处理，初学时可用随机字母数字密码。若要使用其他配置文件，可在启动前设置 `$env:MIZUKI_ENV_FILE`；默认 `.env` 缺失时服务仍会回退到系统环境变量。API 显示 `archive API listening on 127.0.0.1:8080` 后，在新终端用 `Invoke-WebRequest http://127.0.0.1:8080/healthz` 可检查状态码 `204`。不要在截图或公开日志中展示配置值。

### 可选：启用邮箱注册/登录

`/api/auth/capabilities` 另返回 `phone_verification`；目前无真实短信发送器，值固定为 `false`，登录页不会展示手机入口。没有手机注册环境开关，不能通过设置虚构凭据启用模拟短信；详见 [ADR 0008](decisions/0008-mainland-phone-foundation.md)。

先准备支持 STARTTLS（587）或隐式 TLS（465）的 SMTP 服务，在 `.env` 完整填写 `SMTP_HOST`、`SMTP_PORT`、`SMTP_USERNAME`、`SMTP_PASSWORD`、`SMTP_FROM` 和至少 32 字节的随机 `EMAIL_CODE_SECRET`，然后重启 API。只配置一部分会让服务启动失败，避免前端显示不可用入口；不配置则保留兼容密码登录。验证码不会写入日志或数据库明文。重启后可先执行 `Invoke-WebRequest http://127.0.0.1:8080/api/auth/capabilities`，确认响应中的 `email_verification` 为 `true`；浏览器登录页会显示“邮箱登录/邮箱注册”入口，验证码只能消费一次，退出后原会话立即失效。若 SMTP 暂未准备好，使用兼容密码登录，不要在日志或页面中手工填写验证码。

开发阶段没有外部 SMTP 时，可以使用仓库提供的 Mailpit 捕获环境。该 profile 只接受测试凭据，并使用仓库外的本机自签名证书强制 STARTTLS；证书只用于环回验收，不能复制到生产。请在**启动 API 的同一个 PowerShell 终端**执行下面的覆盖配置，这些进程环境变量会优先于 `.env` 中可能存在的真实 SMTP 配置：

```powershell
.\scripts\generate-mailpit-cert.ps1
$env:MAILPIT_TLS_DIR = (Resolve-Path '..\mizuki-mailpit-tls').Path
docker compose --profile email up -d mailpit
$env:SMTP_HOST = '127.0.0.1'
$env:SMTP_PORT = '1025'
$env:SMTP_USERNAME = 'dev'
$env:SMTP_PASSWORD = 'dev'
$env:SMTP_FROM = 'archive@example.test'
$env:SMTP_CA_FILE = (Resolve-Path '..\mizuki-mailpit-tls\mailpit.crt').Path
$env:EMAIL_CODE_SECRET = 'replace-with-at-least-32-random-bytes'
go run ./cmd/server
```

登录页请求验证码后，在 `http://localhost:8025/` 打开 Mailpit 邮件，复制 6 位验证码回浏览器完成注册或登录。Mailpit 只用于本机捕获，不代表真实外部邮箱投递；真实 SMTP 仍须使用 587/465 和 TLS 配置。

## 3. 启动 Vue 并登录

第三个终端执行：

```powershell
cd web
npm ci
npm run dev
```

只打开 **`http://localhost:5173/`**；不要改用 `127.0.0.1:5173`，因为 API 校验写请求的 `Origin`。未配置 SMTP 时可用 `owner`（或你设置的 `APP_ADMIN_USERNAME`）和第 2 步生成哈希时输入的明文密码；配置 SMTP 后也可以使用邮箱注册创建独立资料空间，再用邮箱验证码登录。登录后可用一份不含真实个人信息的 `.txt` 或 `.md` 验证上传、搜索、详情、下载和退出；不要上传简历或私人资料作为公开演示数据。若你检出的版本在窄屏找不到退出按钮，先在桌面宽屏使用并检查最新的界面变更是否已合入。

## 常见问题与停止

| 现象 | 优先检查 |
| --- | --- |
| 数据库连接失败 | Docker Desktop 是否启动、容器是否健康、`.env` 与 `MYSQL_DSN` 用户名/密码/端口是否一致。 |
| `403 请求来源未获授权` | 地址栏是否是 `localhost:5173`，`APP_ORIGIN` 是否完全相同。 |
| `401` 或登录失败 | 账号与明文密码是否对应哈希；更改环境变量后要重启 Go 服务。 |
| 前端打不开/502 | Vite 是否显示 `Local` 地址，Go 服务是否仍运行，8080/5173 是否被其他程序占用。 |

结束时在 Go 和 Vite 终端分别按 `Ctrl+C`；数据库可用 `docker compose stop mysql` 停止，卷保留供下次使用。测试环境和真实资料分开；需要清除自己的数据前先做好备份。

## 4. 启动处理 Worker

V2 任务不会由 API 进程偷偷执行，必须单独启动 Worker 终端：

```powershell
# 在仓库根目录，使用与 API 相同的 .env
 go run ./cmd/worker
```

登录浏览器后上传不含敏感信息的 `.txt`、`.md` 或文本型 `.pdf`，打开详情，点击“手动提取文本”。图片资料则点击“手动生成缩略图”。详情中的状态会从“等待处理”变为“处理中”，Worker 成功后分别显示“下载提取文本”或缩略图预览/“下载缩略图”。文本任务成功后，回到资料库搜索提取正文中的关键词，应能找到原资料。

如果不启动 Worker，任务会稳定保留为“等待处理”，这不是前端卡死；如果任务失败，详情会显示脱敏失败摘要。重复点击同一资料的提取按钮不会重复创建同一来源哈希的未完成或成功任务。

V5 可在 `.env` 设置 `PROCESSING_WORKERS=2`（每个 Worker 进程 1–4 个槽位）、`PROCESSING_MAX_OUTSTANDING=100`（全局未完成任务上限 1–1000）。RabbitMQ 模式下 `RABBITMQ_PREFETCH` 须在 1 和 `PROCESSING_WORKERS` 之间。改动配置后重启对应进程。容量满时手动任务接口返回 `429 queue_full` 和 `Retry-After: 5`；重复任务仍返回已存在的任务。不要同时运行数据库与 RabbitMQ 模式的 Worker 来绕开限制。部署多台 Worker 时各进程槽位会相加，须控制总并发。

## 5. V2 手工验收清单

- 使用 PDF、TXT、Markdown 各验证一次手动任务；确认原文件下载仍正常。
- 成功后下载派生 `.extracted.txt`，检查内容和详情中的文件指纹。
- 用派生文本中的独特词搜索，确认结果命中原资料；搜索没有命中时检查任务是否成功及 Worker 是否指向同一 `APP_DATA_DIR`。
- 使用 PNG、JPEG 或 WebP 图片触发一次缩略图任务；确认缩略图能在详情中预览并下载，原文件下载仍正常。
- 测试完成后删除合成资料，并检查 `data/derived` 中没有遗留临时 `pending-*` 文件。
