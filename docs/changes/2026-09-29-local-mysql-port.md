# 本地 Docker MySQL 改用 3307 主机端口

## 目的与用户影响

本机 `MySQL93` 服务已占用 `127.0.0.1:3306`，导致 Docker Compose 无法启动项目的 MySQL 8.4 容器。将 Docker 主机端口调整为 `3307`，避免停止用户已有的本机 MySQL 服务。

## 变更范围

- Compose 使用可配置的 `MYSQL_HOST_PORT`，默认值为 `3307`。
- `.env.example` 增加 `MYSQL_HOST_PORT=3307`。
- README 与本地启动文档中的 `MYSQL_DSN` 改用 `127.0.0.1:3307`。
- Go 服务不改动；容器内部 MySQL 端口仍为 `3306`。

## 取舍

保留本机 MySQL 服务并修改 Docker 主机端口，比停止系统服务更少影响其他项目。主机端口与容器端口分离，后续可仅修改 `.env` 中的 `MYSQL_HOST_PORT`，但启动 API 时必须同步修改 `MYSQL_DSN`。

## 验证

- `docker compose config`：通过，主机端口解析为 `3307`。
- `docker compose up -d mysql`：通过，容器状态为 `Up (healthy)`，映射为 `127.0.0.1:3307->3306/tcp`。
- `docker compose exec -T mysql mysqladmin ping -h 127.0.0.1 --silent`：通过。
- `go test ./...`：通过。
- `go vet ./...`：通过。
- `docker compose --env-file .env.example config --quiet`：再次通过，解析端口为 `3307`。

## 已知风险与下一步

- 当前 `.env` 是本机忽略文件；若未从最新 `.env.example` 补充 `MYSQL_HOST_PORT`，Compose 会使用默认值 `3307`。
- Go API 启动时必须使用 `tcp(127.0.0.1:<MYSQL_HOST_PORT>)`；当前默认值为 `3307`，否则会连接到错误端口。
