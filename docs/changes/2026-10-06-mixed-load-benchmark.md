# 持续混合资料压测工具

## 目的

为独立的 `mizuki-bench` Compose 项目提供可重复的 TXT/PDF/图片混合负载驱动和每秒资源采样，形成面试可复查的本机证据；本记录不把单机结果换算为公网容量或 SLO。

## 范围与取舍

- `cmd/mixed-load` 使用独立 Cookie Jar 登录一个压测账号，按到达速率轮换 `sample.txt`、`sample.pdf`、`sample.png`，并并发发送列表、关键词搜索和下载请求。
- 输出 `http.csv`、`tasks.csv`、`summary.json`。HTTP 汇总包含请求数、请求吞吐、p50/p95/p99、5xx、超时以及预期/非预期 429；任务记录保留任务、资料和派生产物 ID及服务端时间戳，并记录实际成功创建任务数/速率。
- 任务轮询使用有界队列和固定轮询 Worker，避免慢 OCR 让客户端创建无限 goroutine；负载窗口和排空窗口分开计算。
- `scripts/sample-bench.ps1` 只采集指定 Compose 项目的容器，输出 Docker CPU/cgroup 内存、容器主进程 `VmRSS`、容器状态、MySQL `Threads_connected` 和 `processing_jobs` 状态；同时保存启动时的队列基线。
- Worker 停止、崩溃、重启和“恢复到积压清零”的时间仍由操作者在确认项目名后手动执行，避免压测程序误操作其他项目；`container-state.csv` 可用于对齐停止/恢复时间。

## 验证结果

- 已通过 `go test ./cmd/mixed-load`、`go vet ./cmd/mixed-load`、`go run ./cmd/mixed-load -h`。
- 已通过 PowerShell AST 解析和 `pwsh -NoProfile -File .\scripts\sample-bench.ps1 -?`。
- 已通过 `git diff --check`；已有 Mailpit 证书脚本的工作区修改保留。
- `mizuki-bench` 当前已通过 `/readyz`，三个合成 fixture 已存在；真实短时压测尚未在本轮执行，因为当前终端没有 `MIZUKI_BENCH_PASSWORD`。

## 运行记录模板

运行前记录机器、镜像版本、文件字节数、账号数、Worker 数、到达速率、持续时间和结果目录。先启动采样器，再启动负载驱动；逐档保存原始目录，不覆盖前一档。

```powershell
$env:MIZUKI_BENCH_PASSWORD = '只在本机终端设置，不要写入聊天或结果文件'
$run = Join-Path $env:TEMP 'mizuki-bench-results\run-01'
go run ./cmd/mixed-load -base-url http://localhost:18082 -username benchowner `
  -fixture-dir (Join-Path $env:TEMP 'mizuki-bench-fixtures') -duration 5m `
  -arrival-rate 0.2 -read-workers 4 -output-dir $run
```

采样器的 `-DurationSeconds` 应覆盖负载窗口、Worker 故障演练和排空时间。将 `summary.json`、三个 CSV、采样 CSV 和故障时间线一起归档到仓库外。

## 遗留风险

- 队列采样是整个隔离数据库的状态；`queue-baseline.json` 用于说明启动前已有任务，任务成功率应以本次 `tasks.csv` 的任务 ID 为准。
- `pid1_rss_kib` 是每个容器主进程的 RSS，不等于容器内所有进程总 RSS；Docker cgroup 内存仍需同时查看。
- 单机、单数据库和本地文件卷只能证明当前配置下的行为，不能推出公网 QPS、跨节点高可用或容量上限。
- 未完成 OCR 依赖环境、持续多档压测和 Worker 优雅停止/崩溃两种恢复演练前，不得把对应结果写成已验证能力。
