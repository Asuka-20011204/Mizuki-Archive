# 2026-09-29 · V1 基础切片与 MVC/GORM 调整

## 目标与范围

开始单人私有资料库：登录、受限文件上传、文件名/类型检索、详情和下载；后端 Go + Gin + GORM + MySQL，前端 Vue 3 + TypeScript，视觉参考用户下载的 Sakurairo 2026 年 9 月版本。本轮不实现标签、收藏编辑、预览、异步任务、Redis 或 MQ。

## 决策与取舍

- 单管理员 + 可撤销的数据库会话，HttpOnly/SameSite Cookie 与严格 Origin 检查；密码仅存 bcrypt 哈希。
- 文件保存在本机受控目录，数据库记录元数据；单文件最大 50 MB，允许 PDF、PNG/JPEG/WebP、Markdown/TXT。
- 用户要求 MVC 与 GORM 后，初稿从单文件 HTTP 实现拆分为 Model/Controller/Service/Repository，并把 Repository 从 `database/sql` 改为 GORM；保留显式初始迁移。取舍详情见 ADR 0002。
- 参考主题只用于氛围与阅读层级，不复制其 GPL 源码或资源。
- 安全复查后限制登录请求体为 8 KiB，并统一会话过期时间为 UTC；补了超限回归测试。

## 验证记录

- 先写了 HTTP 流程测试；首次运行因缺依赖失败，随后 Go 依赖代理曾出现连接/校验错误，重试后已下载基础依赖。
- `go test ./...` 与 `go vet ./...` 通过；`go test -cover ./...`：Controller 66.9%、Service 77.3%、Repository 0%，距离 80% 目标仍有差距。
- `cd web; npm run build` 通过（Vue 类型检查与 Vite 构建）；`npm audit --omit=dev --audit-level=high` 报告零条生产依赖漏洞。
- 浏览器打开了登录页并检查桌面截图；窄屏、登录后的资料库与完整用户流程未验证。前端已移除外部 Google Fonts 请求，避免私有产品加载第三方字体。
- `go test -race ./...` 未能运行：本机 CGO 关闭，且无 C 编译器；不是测试通过。
- `docker compose --env-file .env.example config --quiet` 与 Markdown 链接检查通过；基础凭据标记扫描无匹配。实际数据库未启动。
- 本机 Docker daemon 当前未运行，MySQL 真机集成尚未执行；不得标记为通过。

## 遗留风险与下一步

- 后续补 Repository 测试与覆盖率、MySQL 实库集成/端到端流程、窄屏和键盘操作检查；检查上传边界、会话过期和数据库迁移。
- V1 后续补标签、收藏编辑、预览、删除、备份恢复演练；下一轮重大变更另写记录。
