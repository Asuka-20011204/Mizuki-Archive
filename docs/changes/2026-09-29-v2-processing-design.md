# 2026-09-29 · V2 持久任务与单进程 Worker 设计

## 目的

在 V1 资料管理闭环提交后，先定下下一阶段的任务模型和边界，避免为了学习 Redis/RabbitMQ 直接把基础设施塞进个人资料系统。本文只记录设计，不代表 Worker、任务表、Redis 或 RabbitMQ 已经实现。

## 本轮设计

- 先用 MySQL 持久化 `ProcessingJob`，用单进程 Go Worker 轮询和领取任务。
- 任务使用 `pending → processing → succeeded/failed` 状态机，配合租约、超时、有限重试和幂等键。
- 产物独立保存为 `DerivedAsset`，原件只读、派生文件不覆盖原件。
- API、Service、Repository、Worker 和处理器职责分开，继续遵守 MVC 分层和中文注释规则。
- 处理器只接收任务 ID/受控文件路径，不把原文件放进未来的 MQ 消息或 Redis。

## 未决事项

首个处理类型、手动/自动触发、是否立即进入检索索引、失败重试交互需要用户确认。综合个人资料管理价值，当前建议先做 PDF 文本提取，但在选择库、资源上限和固定测试集前不开始编码。

## 下一步

用户确认后先写失败案例和 Repository/Service/Worker 测试，再实现最小 `ProcessingJob` 迁移和单 Worker；完成实测后才评估 RabbitMQ，Redis 仍等待明确的缓存或限流场景。