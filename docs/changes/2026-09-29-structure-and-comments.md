# 2026-09-29 · 目录导览与中文注释

## 目标与用户影响

让项目作者快速定位两个 Go `main()`、Vue 入口及 MVC 每一层；让安全与文件一致性的关键代码有中文解释，不必靠猜文件名。

## 变更范围与取舍

- 将原 `internal/controller/http.go` 按路由、认证、资料接口拆为 `router.go`、`auth_controller.go`、`resource_controller.go`；不改变已设计的 HTTP 行为。
- 将 GORM 实现命名为 `mysql_gorm.go`，与 `repository.go` 的接口区分。
- 在入口、Model、Controller、Service、Repository 和前端请求/交互处加中文注释，重点解释来源校验、会话摘要、文件校验与失败补偿，而非逐行复述代码。
- 新增 `docs/project-structure.md`；同步 README、AGENTS 和架构文档。将 MVC 分类、可读性、易用性、安全、中文 Git 提交、变更记录设为项目长期约束。
- 不新增业务功能或数据库表，不创建 Git 提交。

## 验证结果

`gofmt -w cmd internal`、`go test ./...`、`go vet ./...`、`cd web; npm run build` 和 Markdown 本地链接检查均通过。两个 `main()` 仍分别位于 `cmd/server/main.go` 与 `cmd/hash-password/main.go`。MySQL 实库端到端仍未验证，不能把静态分层当成已完成的运行验证。

## 遗留与下一步

Vue 页面目前集中于 `App.vue`，`web/src/style.css` 也仍是高密度样式文件；后续修改界面时优先整理格式和按功能拆组件/样式，避免继续堆叠长行，也避免提前产生空目录。后续仍需 MySQL 集成测试、迁移版本跟踪与覆盖率改进。Git 仓库尚无提交，本轮未擅自提交或推送。
