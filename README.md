# Mizuki Archive

面向个人的数字信息管理与处理系统（V1 开发中）。把分散的 PDF、图片、Markdown、TXT 等资料收进同一个资料库，支持整理、检索、查看，并逐步加入耗时处理任务。它**不是**网盘复刻，也**不是**为了展示并发而造的通用任务平台。

> 当前状态：V1 开发中。以实际代码和测试为准；尚未完成的功能不得当成已实现。

## 产品主线

**管 → 找 → 处理**：上传资料并记录元信息；按关键词、类型、标签找到资料；按需生成预览、提取文本并查看处理历史。首位用户是项目作者本人，面向资料积累较多的学生、开发者和创作者，不预设多人协作或百万 QPS。

具体场景、边界与验收标准见 [产品定义](docs/product.md)。

## 工程方向

- 后端：Go + Gin + MySQL，单体 API；后续处理任务使用独立 Worker。Go 按 Model、Controller、Service、Repository 分层。
- 前端：Vue 3 + TypeScript 作为 View；优先做好资料列表、详情、搜索和任务状态，不追求复杂后台模板。
- 文件先放本地受控目录，数据库保存元数据；后续按真实瓶颈引入 Redis 和 RabbitMQ，不把它们设为 V1 前置条件。
- Docker 化、压测与可选的 Kubernetes 实验放在功能可靠之后；任何性能或规模数据必须实测。

架构与取舍见 [技术方案](docs/architecture.md)，分期见 [路线图](docs/roadmap.md)。

## 协作入口

- [AGENTS.md](AGENTS.md)：对人和编码 Agent 同样适用的项目约束。
- [项目目录导览](docs/project-structure.md)：两个 Go `main()`、各层职责和请求调用路径。
- [开发与 Git 规范](docs/development.md)：中文提交、分支、质量门禁、文档同步。
- [本地启动与实测](docs/local-development.md)：按终端分步启动 MySQL、Go、Vue，并在浏览器登录验收。
- [安全基线](docs/security.md)：私有文件、上传、鉴权、任务安全。
- [界面方向](docs/design.md)：白色、内容优先的独立视觉语言；不复制 Sakurairo 主题代码和素材。
- [架构决策](docs/decisions/0001-product-and-stack.md)：已确定项与待验证项。
- [变更记录](docs/changes/README.md)：各轮实际修改、验证状态与遗留事项；后续重大修改逐轮记录。

## 本地启动（Windows PowerShell）

前置：Go 1.25、Node.js、Docker Desktop/MySQL 8.4。请先确认 Docker Desktop 正在运行；首次启动的完整操作与故障排查见 [本地启动与实测](docs/local-development.md)。

1. 复制 `.env.example` 为本地 `.env`，只用于 Docker Compose；把 `MYSQL_DATABASE`、`MYSQL_USER`、`MYSQL_PASSWORD`、`MYSQL_ROOT_PASSWORD` 改为你自己的值。`.env` 已被 Git 忽略。**不要把真实密码或哈希写进仓库。**
2. 在仓库根目录运行 `docker compose up -d mysql`。检查 `docker compose ps`，等待数据库健康。
3. 在同一个 PowerShell 会话设置 API 配置，再运行服务：

   ```powershell
   $env:APP_ORIGIN = 'http://localhost:5173'
   $env:APP_LISTEN_ADDR = '127.0.0.1:8080'
   $env:APP_DATA_DIR = './data/files'
   $env:APP_ADMIN_USERNAME = 'owner'
   $env:APP_ADMIN_PASSWORD_HASH = (go run ./cmd/hash-password)
   $env:MYSQL_DSN = 'archive:在此填入本机数据库密码@tcp(127.0.0.1:3306)/archive'
   go run ./cmd/server
   ```

   密码哈希命令在交互终端中隐藏输入；`MYSQL_DSN` 的用户名、数据库名和密码须与本地 `.env` 一致。若使用特殊字符，请确认 DSN 格式能正确解析。服务启动时应用当前幂等初始迁移。生产环境必须使用 HTTPS、独立凭据和受控备份。

4. 另开 PowerShell：

   ```powershell
   cd web
   npm ci
   npm run dev
   ```

   打开 `http://localhost:5173/`。不要换成 `127.0.0.1:5173`，因为浏览器请求来源必须与 `APP_ORIGIN` 完全一致。

## 已实现的首个切片与验证

单管理员登录/退出、私有资料上传、文件名/类型筛选、资料详情与附件下载。允许 PDF、PNG/JPEG/WebP、Markdown 和 TXT，单文件上限 50 MiB；尚无标签编辑、收藏操作、预览、删除、Worker 或 Redis/MQ。

- `go test ./...`、`go vet ./...`：已通过；`go test -cover ./...` 中 Controller 66.9%、Service 77.3%，尚未达到 80% 目标。已用隔离的 MySQL 8.4 测试容器验证登录、上传、查询、下载和退出；尚未建立可重复运行的自动化数据库集成测试。
- `cd web; npm run build`：已通过；`npm audit --omit=dev --audit-level=high`：零条生产依赖告警。
- `go test -race ./...`：本机 CGO 未启用且无 C 编译器，尚未运行成功。开发/CI 后续补齐。
- 已在本地工作区的窄屏界面验证登录、空状态、搜索、类型筛选、详情焦点/Escape 与退出；界面改动将另行评审提交。桌面宽屏和浏览器文件选择上传仍待手工验收。API 上传与文件内容回读已通过测试容器验证。

本地资料、会话与数据库数据请自行备份；不要用真实私人文件做公开演示。
