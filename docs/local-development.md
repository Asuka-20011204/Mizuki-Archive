# 本地启动与浏览器实测（Windows PowerShell）

以下命令从项目的仓库根目录开始。准备 **三个 PowerShell 终端**：数据库命令、Go API、Vue 开发服务器；每次新开终端先切换到仓库根目录。Go API 会自动读取根目录 `.env`，系统环境变量优先于文件中的同名值。不要把密码、哈希或个人文件提交到 Git；示例值要改成本机独有值。

## 可选 V3/V4 服务

默认 `docker compose up -d mysql` 只启动 MySQL。启用 RabbitMQ 前，在 `.env` 设置独立的 `RABBITMQ_USER`、`RABBITMQ_PASSWORD`、`RABBITMQ_URL`，然后运行 `docker compose --profile v3 up -d rabbitmq`。API 与 Worker 均设置 `PROCESSING_DELIVERY_MODE=rabbit`；只运行一个任务模式的 Worker。回退时停止 RabbitMQ Worker，改为 `database` 后重启，Outbox 数据仍保留在 MySQL。

启用 Redis 前，在 `.env` 设置非空 `REDIS_PASSWORD` 和一致的 `REDIS_URL`，运行 `docker compose --profile v4 up -d redis`。默认主机端口是 `6381`（容器内 `6379`），如需调整 `REDIS_HOST_PORT` 必须同步 URL。Redis 停止后缓存回源 MySQL，但跨进程限流不再保证。不要把 `.env`、真实个人资料或容器卷提交 Git。

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

在仓库根目录的第二个终端执行 `go run ./cmd/hash-password`。输入你选择的管理员明文密码（至少 12 字节；终端不回显），将输出的 bcrypt 哈希用单引号包住后粘贴到 `.env` 的 `APP_ADMIN_PASSWORD_HASH`，例如 `APP_ADMIN_PASSWORD_HASH='$2a$10$...'`。bcrypt 哈希包含 `$`，不能裸写。浏览器登录时使用的是**原来的明文密码**，不是这段哈希。

然后直接启动服务：

```powershell
go run ./cmd/server
```

`.env` 中的 `MYSQL_DSN` 端口必须与 `MYSQL_HOST_PORT` 一致，密码也需与数据库初始化时使用的密码一致；特殊字符可能需要按 Go MySQL DSN 格式处理，初学时可用随机字母数字密码。若要使用其他配置文件，可在启动前设置 `$env:MIZUKI_ENV_FILE`；默认 `.env` 缺失时服务仍会回退到系统环境变量。API 显示 `archive API listening on 127.0.0.1:8080` 后，在新终端用 `Invoke-WebRequest http://127.0.0.1:8080/healthz` 可检查状态码 `204`。不要在截图或公开日志中展示配置值。

## 3. 启动 Vue 并登录

第三个终端执行：

```powershell
cd web
npm ci
npm run dev
```

只打开 **`http://localhost:5173/`**；不要改用 `127.0.0.1:5173`，因为 API 校验写请求的 `Origin`。账号是 `owner`（或你设置的 `APP_ADMIN_USERNAME`），密码是第 2 步生成哈希时输入的明文。登录后可用一份不含真实个人信息的 `.txt` 或 `.md` 验证上传、搜索、详情、下载和退出；不要上传简历或私人资料作为公开演示数据。若你检出的版本在窄屏找不到退出按钮，先在桌面宽屏使用并检查最新的界面变更是否已合入。

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

## 5. V2 手工验收清单

- 使用 PDF、TXT、Markdown 各验证一次手动任务；确认原文件下载仍正常。
- 成功后下载派生 `.extracted.txt`，检查内容和详情中的文件指纹。
- 用派生文本中的独特词搜索，确认结果命中原资料；搜索没有命中时检查任务是否成功及 Worker 是否指向同一 `APP_DATA_DIR`。
- 使用 PNG、JPEG 或 WebP 图片触发一次缩略图任务；确认缩略图能在详情中预览并下载，原文件下载仍正常。
- 测试完成后删除合成资料，并检查 `data/derived` 中没有遗留临时 `pending-*` 文件。
