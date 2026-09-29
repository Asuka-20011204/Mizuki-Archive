# V11 多 API/Worker 本机扩展基础

- 日期：2026-09-29
- 目的：为高并发、高可用目标补齐可重复的多进程部署入口，不把单机编排误称为跨节点高可用。

## 变更

- `web/nginx.conf` 使用 Docker 内置 DNS 的短 TTL 重新解析 `api` 服务名。
- Web 对连接错误、超时和 502/503/504 允许读请求切换 API 副本。
- 不启用 `non_idempotent` 自动重试，避免登录、上传、验证码发送和任务创建重复执行。
- `docs/deployment.md` 增加 `--scale api=2 --scale worker=2` 的本机演练步骤和边界。

## 验证

- `docker compose --env-file .env -f compose.deploy.yaml config --quiet` 通过。
- 使用进程级占位 SMTP 变量渲染 Compose，确认 SMTP 环境只进入 API，不进入 Worker。
- 本轮尚未启动完整多副本部署，也未验证 MySQL/Redis/RabbitMQ 集群或跨节点故障切换。

## 遗留风险与下一步

- 需要在隔离部署项目中实际启动多个 API/Worker，模拟停止一个 API 和一个 Worker，并验证读请求、任务租约和会话继续工作。
- 需要为跨节点部署引入共享对象存储、数据库高可用、Redis Sentinel/Cluster、RabbitMQ 集群和 TLS 入口。
- 需要以混合 PDF、图片、TXT/Markdown 和多用户负载形成容量基线。

