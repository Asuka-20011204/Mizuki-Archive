# V8 邮箱认证与处理归属隔离验收

- 日期：2026-09-29
- 目的：把多用户身份从“代码基础”推进到可验收的 HTTP 邮箱注册/登录闭环，并验证任务和派生产物不会因资源 ID 或任务 ID 泄露而跨用户访问。
- 变更范围：新增邮箱认证 HTTP 测试发送器，覆盖注册验证码请求、注册、验证码一次性消费、会话身份、退出、再次登录和未知邮箱统一响应；新增真实 GORM/MySQL 任务隔离集成测试，覆盖任务读取、列表、幂等复用、完成任务后的派生产物读取和反向用户访问。
- 安全边界：邮箱验证码仍由服务端生成、摘要保存、限时并原子消费；测试发送器只记录验证码，不打印或提交真实凭据。任务读取通过 `processing_jobs -> resources.user_id` 归属链过滤，派生产物读取通过 `derived_assets -> processing_jobs -> resources.user_id` 归属链过滤。
- 验证：`go test ./internal/controller ./internal/repository` 通过；临时 MySQL 8.4 库中 `TestMySQLSetFavorite`、`TestMySQLProcessingOwnershipIsolation`、`TestMySQLFinalizeOwnershipBackfillsLegacyRows` 和 `TestMySQLEmailChallengeConcurrentConsumption` 通过，测试库已清理；临时 Redis 7.4 容器在 `6382` 端口通过最近访问用户命名空间测试，容器已清理；已有 SMTP TLS 测试继续覆盖 STARTTLS、无 TLS 拒绝和安全邮件交付。
- 未完成：真实 SMTP 凭据下的浏览器注册/登录/退出、多账号浏览器隔离，以及多 API/Worker 和 MySQL/Redis/RabbitMQ 集群级高可用演练。
- 下一步：配置专用测试 SMTP 完成浏览器闭环，再进行多实例混合负载与故障切换验证。
