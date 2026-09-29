# V9 多 API 共享状态基础验收

- 日期：2026-09-29
- 目的：验证 API 保持无状态时，实例 A 签发的会话可以由实例 B 校验，且两个实例对同一用户的资料归属和列表读取保持一致。
- 变更范围：新增真实 GORM/MySQL 集成测试，构造两个独立的 Gin/API 装配实例，共享数据库会话、用户和资料状态；测试覆盖 A 实例登录、B 实例读取 `/api/me` 和 B 实例读取用户资料。
- 验证：临时 MySQL 8.4 隔离库通过 `TestMultiAPIInstancesShareMySQLSessionAndOwnership`，测试库已清理；普通 Go 测试、静态检查和前端构建继续作为提交门禁。
- 结论：共享 MySQL 会话和归属状态满足无状态 API 的基础前提；内存登录限流、文件目录共享、入口代理、数据库故障切换和跨进程 Worker 仍未由本测试证明。
- 未完成：真实多进程/多节点 API、独立 Worker 扩容、MySQL 高可用、Redis Sentinel/Cluster、RabbitMQ 集群、共享文件或对象存储、TLS 网关和持续混合负载。
- 下一步：在隔离部署编排中启动两个 API 与多个 Worker，接入共享依赖后做故障切换和混合负载测试；未完成前不宣称公网高可用。
