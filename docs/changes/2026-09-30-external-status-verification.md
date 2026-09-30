# 2026-09-30 · 外部资源状态维护核验

## 目的与范围

核对路线图 P1 “资源状态维护”是否真正具备交付链路，而非仅有下拉选项。现有 Vue 卡片表单已允许人工选择未核对、可用、待确认、链接失效、已下载，列表展示状态；服务层严格限定这些取值，仓储按归属用户更新。无需另建重复的状态体系或自动探测站外链接。

补充服务层完整状态切换/回读、HTTP 按会话更新和跨用户不可见、隔离 MySQL 状态落库与整理状态独立性断言。卡片的链接状态仅由用户维护，不把链接失效误写成文件整理状态，也不主动访问用户录入的地址。

## 验证

- `go test ./internal/service ./internal/controller -run 'TestExternalResourceFlow|TestExternalResourceHTTPIsolation|TestExternalResourceRejectsInvalidFields' -count=1`：通过。
- 在新建 `mizuki_test_status_...` 隔离 MySQL 8.4 库中运行 `go test ./internal/repository -run '^TestExternalResourceMySQLIsolation$' -count=1`：通过；临时库及权限已撤销。首次测试脚本生成的 DSN 不符合隔离库格式而失败，修正脚本后复测通过；首次临时库与残留权限已清理。该失败不是应用代码问题。
- 前端手动浏览器状态切换尚未实测，不能把自动化测试当成完整交互验收。

## 遗留风险与下一步

继续用隔离账号进行真实浏览器录入/编辑/列表状态回归；P1 的资料笔记、关联线索和可携带导出仍未实现。
