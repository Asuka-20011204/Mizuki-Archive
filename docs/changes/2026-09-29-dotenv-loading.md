# Go API 自动读取本地 `.env`

## 目的与用户影响

启动 Go API 时不再需要在每个 PowerShell 会话重复输入多项 `$env:` 配置。服务默认读取仓库根目录 `.env`，用户只需首次生成管理员密码哈希并写入该文件，然后运行 `go run ./cmd/server`。

## 变更范围

- `cmd/server` 使用 `github.com/joho/godotenv` 加载可选 `.env` 文件。
- 已存在的系统环境变量不会被 `.env` 覆盖，便于生产环境注入配置。
- 支持通过 `MIZUKI_ENV_FILE` 指定其他配置文件；默认 `.env` 缺失时继续依赖系统环境变量。
- 增加配置加载测试，并同步更新 README 与本地启动文档。

## 取舍与安全

`.env` 适合本地开发，不应提交真实密码、哈希或个人资料。系统环境变量优先于文件配置，避免本地文件意外覆盖部署环境的受控配置；自定义配置文件路径只通过外部环境变量指定，不把路径或凭据写入源码。bcrypt 哈希包含 `$`，写入 `.env` 时必须用单引号包住，避免被 dotenv 当作变量展开。

## 验证

- `go test ./cmd/server`：通过。
- `go test ./...`：通过。
- `go vet ./...`：通过。
- `gofmt -l`：通过；`docker compose --env-file .env.example config --quiet`：通过，MySQL 主机端口解析为 `3307`。
- `web` 首次 `npm run build` 因本机未安装 `node_modules` 报 `vue-tsc` 不存在；执行 `npm ci` 后重新构建通过，依赖审计报告 0 条漏洞。

## 已知风险与下一步

- 修改 `.env` 中的数据库初始化密码不会自动修改已经创建的数据卷中的 MySQL 用户密码；数据库已有数据时应使用 SQL 修改凭据，不要直接删除卷。
