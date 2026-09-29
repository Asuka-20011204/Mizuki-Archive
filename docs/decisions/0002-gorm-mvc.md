# ADR 0002：Go MVC 分层与 GORM 持久化

- 日期：2026-09-29
- 状态：已接受（用户指定）

## 背景

V1 首个切片初稿把 HTTP、文件与业务规则堆在单个 Go 文件，并直接使用 `database/sql`。这种写法不利于按正式项目持续迭代；用户明确要求标准 MVC 和 GORM。

## 决定

Vue 3 负责 View；`internal/controller` 负责 Gin 路由、参数、会话中间件和响应；`internal/service` 负责资料与会话业务；`internal/model` 定义领域数据；`internal/repository` 使用 GORM + MySQL 并提供可替换接口。`cmd/server` 负责依赖接线。数据库结构使用显式 SQL 迁移，不使用 AutoMigrate 作为生产迁移工具。

## 权衡

- 分层有利于测试和职责清晰，但拒绝空壳接口/无意义的 DTO 复制；只在需要替换持久化和测试时抽象。
- GORM 降低基础 CRUD 的重复代码，但查询仍需审查 SQL、索引、分页和扫描行为；严禁把未经校验的输入拼入原生 SQL。
- 当前只有幂等初始建表；后续变更必须增加迁移版本跟踪、失败恢复与备份策略，不能长期依赖启动时 `CREATE TABLE IF NOT EXISTS`。
