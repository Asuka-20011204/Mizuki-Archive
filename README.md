# Mizuki Archive

面向个人资料场景的多用户数字信息管理与处理系统（V1–V7 各阶段的本地切片与验证已记录）。把分散的 PDF、图片、Markdown、TXT 等资料收进同一个资料库，支持整理、检索、查看，并按需生成文本或图片派生产物。它**不是**网盘复刻，也**不是**为了展示并发而造的通用任务平台。

> 当前状态：V1 资料管理、V2 手动处理、V3 队列、V4 缓存、V5 背压、V6 本机隔离部署/恢复和 V7 固定合成负载均有验收记录；生产部署已完成迁移账号与运行账号分离，TLS、网关限流、密钥托管和镜像漏洞扫描仍未完成。V7 数据不代表公网生产容量；详见 [量化记录](docs/changes/2026-09-29-v7-fixed-workload.md)。

## 产品主线

**管 → 找 → 处理**：上传资料并记录元信息；按关键词、类型、标签找到资料；按需生成预览、提取文本并查看处理历史。它是“个人数字信息管理”领域的多用户系统：每个注册用户都有独立资料空间，首位用户只是项目作者本人；高并发、高可用是项目级工程目标，不把本地压测结果冒充公网容量。

具体场景、边界与验收标准见 [产品定义](docs/product.md)。

## 工程方向

- 后端：Go + Gin + MySQL，单体 API；后续处理任务使用独立 Worker。Go 按 Model、Controller、Service、Repository 分层。
- 前端：Vue 3 + TypeScript 作为 View；优先做好资料列表、详情、搜索和任务状态，不追求复杂后台模板。
- 文件先放本地受控目录，数据库保存元数据；Redis、RabbitMQ 和 Worker 已按阶段引入，用于支撑共享状态、异步处理、并发控制和后续多实例扩展，不把它们设为 V1 基础资料闭环的前置条件。
- 本地 Docker 隔离部署与固定合成数据集压测已验收；真实混合负载、生产加固和可选 Kubernetes 实验仍需另行验证，任何性能或规模数据必须实测。

架构与取舍见 [技术方案](docs/architecture.md)，分期见 [路线图](docs/roadmap.md)。

## 协作入口

- 本地 `AGENTS.md`：不提交 Git 的个人编码 Agent 协作约束；共享工程规范以本文档和 `docs/development.md` 为准。
- [项目目录导览](docs/project-structure.md)：两个 Go `main()`、各层职责和请求调用路径。
- [开发与 Git 规范](docs/development.md)：中文提交、分支、质量门禁、文档同步。
- [本地启动与实测](docs/local-development.md)：按终端分步启动 MySQL、Go、Vue，并在浏览器登录验收。
- [独立 Docker 部署与恢复](docs/deployment.md)：V6 前后端镜像、就绪检查、备份和隔离恢复操作。
- [安全基线](docs/security.md)：私有文件、上传、鉴权、任务安全。
- [界面方向](docs/design.md)：以 Sakurairo 的封面、白色层次与动效为主要视觉参考，不直接复制主题代码和素材。
- [架构决策](docs/decisions/0001-product-and-stack.md)：已确定项与待验证项。
- [变更记录](docs/changes/README.md)：各轮实际修改、验证状态与遗留事项；后续重大修改逐轮记录。

当前身份模型支持多个独立用户：邮箱验证码注册、登录、退出和一次性消费 HTTP 闭环已验证，资料、会话、任务和 Redis 最近访问均按服务端用户归属隔离；旧 `APP_ADMIN_USERNAME`/`APP_ADMIN_PASSWORD_HASH` 仅作为迁移期兼容登录。邮箱功能需要完整 SMTP 配置，独立 Docker 部署已把 SMTP 凭据限制在 API；手机号短信尚未接入。

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

   `go run ./cmd/server` 会自动读取根目录 `.env`；系统环境变量优先于文件中的同名值。示例中的 `3307` 必须与 `.env` 的 `MYSQL_HOST_PORT` 保持一致。密码哈希命令在交互终端中隐藏输入；`.env` 中包含 `$` 的密码或哈希应使用单引号包住。开发环境服务启动时应用当前幂等初始迁移。生产环境必须使用独立迁移账号、HTTPS、受控密钥和受控备份；独立 Docker 编排的迁移步骤见 [部署文档](docs/deployment.md)。

4. 另开 PowerShell：

   ```powershell
   cd web
   npm ci
   npm run dev
   ```

   打开 `http://localhost:5173/`。不要换成 `127.0.0.1:5173`，因为浏览器请求来源必须与 `APP_ORIGIN` 完全一致。

## V1 状态与验证

V1 的资料管理闭环已经落地：兼容管理员密码登录/退出、私有资料上传、名称编辑、手动标签与标签筛选、收藏、关键词/类型筛选、分页、详情、安全预览、下载和删除。当前身份演进已加入多用户邮箱验证码注册/登录基础，资料、会话、任务和缓存按用户隔离；邮箱入口仅在 SMTP 完整配置时展示。允许 PDF、PNG/JPEG/WebP、Markdown 和 TXT，单文件上限 50 MiB；PDF 登录后以内联方式预览，Markdown/TXT 预览限制为 1 MiB 并以安全文本方式返回。删除先软删除数据库记录并解除标签，再清理原件；原件清理失败时资料保持隐藏，后续由孤儿文件巡检处理。

- `go test ./...`、`go vet ./...`、`gofmt -l .`：通过；隔离 MySQL 8.4 测试库已验证 4 个迁移、重复迁移、收藏、标签替换/筛选、名称更新、软删除和列表隐藏。
- `cd web; npm run build`：通过，包含 `vue-tsc --noEmit` 和 Vite 生产构建。
- 浏览器验收已覆盖登录、空状态、浏览器文件选择上传、搜索/类型筛选、标签、收藏、详情焦点/Escape、文本/图片预览、原件与派生产物下载、名称编辑、删除、退出和窄屏布局；操作系统原生文件对话框的视觉行为不作为验收条件。
- 已按 [备份恢复演练](docs/backup-restore.md) 完成一次隔离恢复：数据库 6 条资源、6 条任务、4 条派生产物，10 个文件的 SHA-256 和字节数全部匹配；隔离资源删除验证后已清理恢复库和目录。禁止把 `docker compose down -v` 当作备份。

V1 不包含 OCR、自动分类或跨用户共享协作；V2 已加入持久任务和单进程 Worker。V3/V4 已验收可选 RabbitMQ 异步任务和 Redis 缓存/最近查看；默认使用数据库任务模式，不依赖可选组件完成基本资料操作。面向多实例 API、独立 Worker、共享 Redis/RabbitMQ 和 MySQL 高可用的生产部署仍需继续验证。

本地资料、会话与数据库数据请自行备份；不要用真实私人文件做公开演示。
## V2 当前状态

V2 已完成处理闭环：在 PDF、TXT 或 Markdown 详情页手动创建 `extract_text` 任务，在图片详情页手动创建 `generate_thumbnail` 任务；`cmd/worker` 通过 MySQL 持久任务表领取任务，分别生成独立 UTF-8 `.txt` 派生文件或受控尺寸的 PNG 缩略图。详情页显示任务状态、失败摘要和成功产物，支持派生文件下载；缩略图支持内联预览。成功提取的正文会参与资料关键词检索，原件不会被覆盖。

启动 API 后，另开一个仓库根目录终端执行：

```powershell
go run ./cmd/worker
```

API 和 Worker 必须使用同一份 `.env`、MySQL 和 `APP_DATA_DIR`。使用 V3 RabbitMQ 模式时，还需在 `.env` 设置独立消息队列凭据，并运行 `docker compose --profile v3 up -d rabbitmq`；V4 Redis 同理需配置密码与 URL，运行 `docker compose --profile v4 up -d redis`。V5 增加有界 Worker Pool 与全局待处理任务上限，满额返回 429；配置和边界见 [本地启动](docs/local-development.md)，验证见 [V5 记录](docs/changes/2026-09-29-v5-concurrency.md)。缺少密码时可选容器拒绝启动。OCR、自动分类与跨用户共享协作尚未实现。
