# V7 图片处理并发门禁（2026-10-01）

## 目的

V7 的固定负载显示，3000×3000 图片并发处理时 Go 堆峰值显著高于单 Worker。先限制高内存图片的并行解码，避免把 `PROCESSING_WORKERS=2` 误解为可以同时安全处理两张大图；容器 RSS、CPU、数据库连接数和跨进程恢复时间仍留作下一次实测。

## 本轮范围

- 数据库模式的第一个 Worker 槽位可领取所有任务，其余槽位使用类型过滤，只领取非 `generate_thumbnail` 任务。
- RabbitMQ 模式在按任务 ID 领取数据库租约前取得进程内图片槽位；槽位繁忙时确认旧消息并延迟发布同一任务，不增加业务任务尝试次数，也不把未确认消息压回队首占满 prefetch。
- 多槽位数据库 Worker 若其任务仓储没有实现类型过滤能力会启动失败，而不是静默退回到允许图片并行的通用领取路径。
- 新增 Service 单元测试，覆盖四槽位中只有一个图片可领取，以及 RabbitMQ 第二条图片消息不会抢占数据库租约。
- 新增可选 MySQL 集成测试，验证真实 GORM 查询在行锁前跳过图片任务。

## 取舍与边界

图片槽位是**每个 Worker 进程**的内存保护，不是跨进程或跨节点的全局限流。数据库模式把图片留给一个专用领取槽位，文本槽位不会因等待图片而持有租约；RabbitMQ 模式利用已有 ACK/NACK 与重排机制，避免在 Service 内部阻塞消息消费者。

当前仍不改变 4000 万像素和 12000 单边尺寸上限，也不把 Go 堆采样换算成容器 RSS。多实例部署时必须根据实际镜像内存上限重新测量，不能仅依据本轮单元测试调整 Worker 数量。

## 验证

- `go test ./internal/service ./internal/repository ./cmd/worker -count=1`：通过；没有设置隔离 MySQL DSN 时，MySQL 集成测试按约定跳过。
- `go test ./internal/service -run 'TestRunPoolLimitsImageEligibleClaims|TestRabbitImageGateBeforeClaim' -count=1`：通过。
- `go test ./internal/repository -run '^TestMySQLTextClaimSkipsImage$' -count=1`：测试逻辑已编译；未设置 `MIZUKI_TEST_MYSQL_DSN` 时按约定跳过。
- `gofmt` 与 `git diff --check`：通过。

## 遗留风险与下一步

- 还没有在目标 Docker 镜像内采集 RSS/CPU，也没有完成 Worker 进程重启后的恢复耗时测量。
- 多个 Worker 进程仍可能同时处理图片；上线前需要在目标机器执行混合 PDF/图片/文本负载，记录峰值、连接数、失败率和恢复窗口。
- OCR、真实浏览器回归及其他路线图未完成项不因本轮门禁而自动标记完成。
