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
- [界面方向](docs/design.md)：以 Sakurairo 的封面、白色层次与动效为主要视觉参考，不直接复制主题代码和素材。
- [架构决策](docs/decisions/0001-product-and-stack.md)：已确定项与待验证项。
- [变更记录](docs/changes/README.md)：各轮实际修改、验证状态与遗留事项；后续重大修改逐轮记录。

## 本地启动（Windows PowerShell）

前置：Go 1.25、Node.js、Docker Desktop/MySQL 8.4。请先确认 Docker Desktop 正在运行；首次启动的完整操作与故障排查见 [本地启动与实测](docs/local-development.md)。

1. 复制 `.env.example` 为本地 `.env`，供 Docker Compose 和 Go API 共用；把数据库密码和管理员配置改为你自己的值。当前 Docker 主机端口为 `3307`（容器内部仍为 `3306`）；如需调整，修改 `.env` 中的 `MYSQL_HOST_PORT`。`.env` 已被 Git 忽略。**不要把真实密码或哈希写进仓库。**
2. 在仓库根目录运行 `docker compose up -d mysql`。检查 `docker compose ps`，等待数据库健康。
3. 首次使用时生成管理员密码哈希，将输出用单引号包住后复制到 `.env` 的 `APP_ADMIN_PASSWORD_HASH`（bcrypt 哈希包含 `$`，不能裸写）；然后直接启动 API：

   ```powershell
   go run ./cmd/hash-password
   # 将上一步输出的 bcrypt 哈希粘贴到 .env 的 APP_ADMIN_PASSWORD_HASH
   go run ./cmd/server
   ```

   `go run ./cmd/server` 会自动读取根目录 `.env`；系统环境变量优先于文件中的同名值。示例中的 `3307` 必须与 `.env` 的 `MYSQL_HOST_PORT` 保持一致。密码哈希命令在交互终端中隐藏输入；`.env` 中包含 `$` 的密码或哈希应使用单引号包住。服务启动时应用当前幂等初始迁移。生产环境必须使用 HTTPS、独立凭据和受控备份。

4. 另开 PowerShell：

   ```powershell
   cd web
   npm ci
   npm run dev
   ```

   打开 `http://localhost:5173/`。不要换成 `127.0.0.1:5173`，因为浏览器请求来源必须与 `APP_ORIGIN` 完全一致。

## V1 状态与验证

V1 的个人资料管理闭环已经落地：单管理员登录/退出、私有资料上传、名称编辑、手动标签与标签筛选、收藏、关键词/类型筛选、分页、详情、安全预览、下载和删除。允许 PDF、PNG/JPEG/WebP、Markdown 和 TXT，单文件上限 50 MiB；PDF 暂不在线预览，Markdown/TXT 预览限制为 1 MiB 并以安全文本方式返回。删除先软删除数据库记录并解除标签，再清理原件；原件清理失败时资料保持隐藏，后续由孤儿文件巡检处理。

- `go test ./...`、`go vet ./...`、`gofmt -l .`：通过；隔离 MySQL 8.4 测试库已验证 3 次迁移、重复迁移、收藏、标签替换/筛选、名称更新、软删除和列表隐藏。
- `cd web; npm run build`：通过，包含 `vue-tsc --noEmit` 和 Vite 生产构建。
- 浏览器验收重点覆盖登录、空状态、搜索/类型筛选、详情焦点/Escape、退出和窄屏布局；本轮标签、预览、名称编辑、删除和文件选择上传仍需按 `docs/local-development.md` 做一次真实数据手工回归。
- 备份与恢复步骤已写入 [备份恢复演练](docs/backup-restore.md)；必须同时备份 MySQL 和 `data/files`，禁止把 `docker compose down -v` 当作备份。

V1 不包含 Worker、Redis、RabbitMQ、OCR、自动分类或多人协作。下一阶段先设计可观察、可重试的持久任务和单进程 Worker，只有真实处理场景稳定后再评估消息队列。

本地资料、会话与数据库数据请自行备份；不要用真实私人文件做公开演示。
