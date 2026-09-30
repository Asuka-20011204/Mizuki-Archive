# 2026-09-30 · 完整本地启动流程

## 目的

补齐 README 与本地启动文档中“只启动 MySQL、API、前端”的缺口，说明 Worker、RabbitMQ、Redis、Mailpit 和真实 SMTP 的实际关系，避免用户以为所有后台服务会被 `docker compose up -d mysql` 自动启动。

## 范围与取舍

- 将本地运行拆为基础资料管理、处理 Worker、RabbitMQ 任务模式、Redis 缓存/共享限流和邮箱验证码五类场景。
- 明确默认任务投递模式为 `database`；文本提取和缩略图即使不使用 RabbitMQ，也必须启动 `cmd/worker`。
- 明确 RabbitMQ、Redis、Mailpit 是 Compose profile，真实 SMTP 是外部服务；Mailpit 只用于本机捕获测试邮件。
- 明确手机号认证当前没有接入运行中的真实短信发送器，不添加虚构的短信环境变量或启动命令。
- 修正本机忽略的 `.env` 中两行未加 `#` 的中文说明，避免 Docker Compose 解析配置失败；不改变其中任何凭据值，也不纳入 Git。

## 验证结果

- `docker compose --profile v3 --profile v4 --profile email config --quiet`：通过。
- `git diff --check -- README.md docs/local-development.md`：通过；Git 的换行转换提示不属于格式错误。
- 未启动或重启 RabbitMQ、Redis、Mailpit、API、Worker，避免在用户当前环境中产生服务状态变化。

## 遗留风险与下一步

- 真实 SMTP 是否可投递仍取决于所选邮件服务商的账号、授权码、域名验证和 TLS 配置，需要单独做浏览器注册/登录闭环验收。
- 手机号注册仍需选择短信服务商、实现发送适配器并完成成本、实名/合规、限频和真实号码验收；当前仅有认证基础代码，不能宣称手机号注册已可用。
- 公网部署继续使用独立迁移账号、HTTPS、外部 Secrets 和 [部署文档](../deployment.md)，不能直接复制本地 Mailpit 或开发 Compose 命令。
