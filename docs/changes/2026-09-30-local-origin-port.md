# 2026-09-30 · 本地登录入口与来源校验

## 目的

用户在 5175 打开本项目时遇到“请求来源未获授权”。诊断确认前端进程曾以 `--port 5175` 启动，而 API 的 `APP_ORIGIN` 是 `http://localhost:5173`；相同空登录请求从 5173 来源进入账号校验返回 401，从 5175 来源被来源校验拒绝返回 403。此错误与密码无关。

## 范围与取舍

- 固定 Vite 默认端口为 5173，并保持 `strictPort`；统一开发访问入口。
- README 和本地开发文档明确入口、其他端口的含义及变更端口时应同步重启 API。
- 保留 API 的严格单来源校验，不扩大跨站请求权限；不修改、记录或提交用户密码及 `.env`。

## 验证与风险

- 变更前已核实 5175 进程属于本项目 Vue，8080 为 Go API，5173 尚未监听。
- `http://localhost:5173/` 返回 200、经 Vite 代理的 `/api/auth/capabilities` 返回 200，API `/healthz` 返回 204；属于本项目的旧 5175 前端进程已关闭。
- 使用空请求体测试登录：5173 来源返回 401（进入账号校验），5175 来源返回 403（来源拒绝）。未使用真实密码，也未验证账户凭据是否正确。
- `npm test`（13 项）、`npm run build`、`go test ./internal/controller -run 'TestLoginRateLimit|TestLoginRejectsOversizedBody' -count=1` 均通过；`git diff --check` 通过。
- 如果用户显式传入 `--port`，仍须确保该端口与 Go API 的 `APP_ORIGIN` 一致。
