# 2026-09-29：V2 文本处理任务闭环

## 目标与用户影响

为个人数字信息管理系统增加手动处理能力：用户可在 PDF、TXT 或 Markdown 详情页发起文本提取，查看任务状态，下载独立派生文本，并用关键词检索成功提取的正文。

## 变更范围

- 新增 `ProcessingJob`、`DerivedAsset` 模型、迁移 `004_processing_jobs.sql` 和 GORM Repository。
- 新增 `internal/processing` 文本处理器，限制 UTF-8 输出不超过 10 MiB；TXT/Markdown 原样读取，PDF 使用 `github.com/ledongthuc/pdf`。
- 新增 `service.Processing` 和 `cmd/worker` 单进程 Worker，支持租约、最大 3 次领取、失败摘要、派生文件原子落盘和幂等完成。
- 新增任务创建、任务列表、任务详情、派生文件下载接口；资料关键词检索包含成功派生正文。
- 前端详情面板支持手动提取、状态轮询、失败提示和派生文本下载；图片按钮保持禁用，缩略图未在本轮实现。
- 收口浏览器回归发现的无标签数组和文本 iframe 空白问题：无标签返回空数组，文本预览改为安全纯文本节点。

## 关键决策

- 继续使用 Go + Gin + GORM + MySQL 的 MVC 分层，不提前加入 Redis/RabbitMQ。
- API 与 Worker 分成 `cmd/server`、`cmd/worker` 两个入口，共享同一数据库和 `APP_DATA_DIR`。
- 原件永不覆盖；数据库成功状态只有在派生文件提交并写入资产元数据后才成立。
- 不实现 OCR、AI 摘要、自动处理或图片缩略图；图片处理单独评估像素和内存边界。

## 验证结果

- `go test ./...`：通过。
- `go vet ./...`：待最终提交前执行。
- `gofmt -l .`：待最终提交前执行。
- `cd web; npm run build`：通过，包含 `vue-tsc --noEmit` 和 Vite 构建。
- 真实浏览器 V1 回归：已验证登录、列表、标签、搜索、图片/文本预览、PDF 下载提示、收藏和下载；文件选择器事件受 in-app browser 限制，上传使用 API 合成资料验证。
- V2 的隔离 MySQL 任务领取竞争、迁移 004 真实执行和 PDF 样本质量：尚未完成，不能宣称已通过。

## 已知风险与下一步

- `github.com/ledongthuc/pdf` 文档明确支持范围有限，需使用脱敏 PDF 数据集验证字体、中文和加密文件表现。
- 当前关键词检索使用 MySQL `LOCATE`，适合个人资料规模，不应宣称为全文搜索或高并发方案。
- 当前单 Worker 轮询是可靠性优先的学习切片；下一步做隔离 MySQL 验收和备份恢复，再评估图片缩略图或 RabbitMQ。