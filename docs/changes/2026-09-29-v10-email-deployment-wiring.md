# V10 邮箱验证码部署接线

- 日期：2026-09-29
- 目的：让已实现的邮箱验证码注册/登录能力可以进入独立 Docker 部署，同时避免把 SMTP 凭据传给 Worker。

## 变更

- `compose.deploy.yaml` 将基础应用环境与 API 专属 SMTP 环境分开。
- API 可读取 `SMTP_HOST`、`SMTP_PORT`、`SMTP_USERNAME`、`SMTP_PASSWORD`、`SMTP_FROM` 和 `EMAIL_CODE_SECRET`。
- Worker 继续只接收任务处理所需配置，不接收邮箱凭据和验证码密钥。
- `docs/deployment.md` 补充部署环境变量、凭据边界和邮箱启用说明。

## 验证

- 使用仓库外的本地 `.env` 执行 `docker compose --env-file .env -f compose.deploy.yaml config --quiet`，Compose 配置展开通过。
- 本轮未启动真实部署容器，也未使用真实 SMTP 凭据；浏览器邮件注册闭环仍待专用测试邮箱环境验证。
- 变更只涉及编排和文档，没有业务代码改动，因此未重复运行 Go/前端测试。

## 遗留风险与下一步

- 需要配置专用 SMTP 测试环境完成浏览器注册、验证码消费、退出、再次登录和两个账号资料隔离。
- 部署编排仍是单机演练，API/Worker 多副本及依赖集群高可用仍需后续验收。

