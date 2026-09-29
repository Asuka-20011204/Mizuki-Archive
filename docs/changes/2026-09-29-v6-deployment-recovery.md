# 2026-09-29 · V6 独立部署与备份恢复演练

## 目的与范围

将原有手动启动 Go/Vue 的单机流程变为可复建的 Docker 本地部署；验证 API 和 Worker 共用数据卷，并用另一组数据库与文件卷做真实恢复演练。

## 设计取舍

- 独立 `compose.deploy.yaml` 避免覆盖 `compose.yaml` 的开发数据库；默认使用数据库任务模式，Web 仅绑定本机 18080，API/Worker/MySQL 不开放宿主端口。
- Go 多阶段构建为 API/Worker 生成同源二进制；运行容器非 root、只读根目录、最小 Linux capability、内存和进程数上限。前端编译后由非特权 Nginx 同源代理 `/api/` 与 `/readyz`。
- `/healthz` 只看 HTTP 进程，`/readyz` 在两秒内检查数据库连接，失败只返回 503，不回显 DSN 或内部错误。
- 备份前冻结写入，MySQL 快照与文件卷同时备份；恢复需隔离项目、校验卷归属后用维护容器修正文件属主。不能对正式部署执行 `down -v`。

## 本轮真实验证

- `docker compose -f compose.deploy.yaml config --quiet`、前后端 `docker compose build` 成功。
- 独立 `mizuki-archive-v6-smoke` 项目四容器运行：页面 200、存活与就绪 204、未认证资源 401；测试口令登录 200、会话 200、合成 TXT 上传 201、下载 200。
- 冻结写入后从独立项目导出 SQL（约 12 KiB）和 1 个合成原件并计算 SHA-256；在另一个 `mizuki-archive-v6-restore-smoke` 项目恢复、登录、列表、下载，文件哈希完全一致。
- 隔离恢复实例手动提交文本提取任务，Worker 完成后用提取正文关键词查到原资料。实际恢复时发现受限 API 容器无法执行 `chown`，改用验证 Docker 卷项目标签后的临时维护容器并复测通过。
- `go test ./internal/controller ./cmd/server -count=1` 已验证就绪/存活和错误脱敏；全量 Go/前端检查将在提交前再运行。

## 遗留与下一步

- 当前 MySQL 应用用户只作用于项目库，但因启动迁移仍有该库 DDL 权限；公网生产还需专门迁移角色、HTTPS/安全 Cookie、外层限流、凭据管理和镜像扫描。
- 单机本地备份演练不能证明跨主机灾难恢复或在线一致性；真实资料必须做异地加密备份和持续恢复演练。V7 将记录固定数据集与负载基线。
