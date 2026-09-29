# V14 TLS、Secrets 与镜像扫描门禁

- 日期：2026-09-29
- 目的：在不改变 `docs/roadmap.md:60` 已验收能力和默认 HTTP 开发环境的前提下，补齐下一步生产化加固边界。
- 范围：可选 TLS Web 配置、Docker Secrets 文件注入、镜像漏洞扫描脚本、部署/架构/安全/目录文档同步。

## 设计取舍

- TLS 使用独立的 `compose.tls.yaml` 覆盖和完整 Nginx 配置，证书与私钥只读挂载；基础 `compose.deploy.yaml` 仍保持本机 HTTP 回归路径，避免开发环境突然要求证书。
- Secrets 使用 `NAME_FILE` 约定。非空环境变量与文件同时存在时拒绝启动，防止轮换后仍误读旧值；文件末尾换行会被去除，但秘密内容不会写入错误信息。
- 漏洞扫描采用 Trivy 发布门禁，扫描 `compose.deploy.yaml` 解析出的镜像，只对 HIGH/CRITICAL 未修复项失败。脚本存在不等于扫描已通过。
- 本轮不把 TLS 覆盖写成跨节点高可用，也不实现手机号短信；邮箱验证码代码继续使用现有 SMTP 配置，真实外部 SMTP 验收仍单独记录。

## 验证

- `go test ./...`：通过。
- `gofmt`：已执行。
- `docker compose ... config --quiet`：使用临时非敏感变量验证基础、TLS 和 Secrets 组合语法通过；真实部署仍须使用仓库外变量和证书文件。
- Web 镜像构建后挂载临时自签名证书执行 `nginx -t -c /etc/nginx/nginx.tls.conf`：通过。
- Trivy 0.74.0：使用 GHCR 数据库源扫描；升级 Go 依赖后 API 镜像为 0 个 HIGH/CRITICAL，Web 切换 Alpine 3.24 后为 0 个 HIGH/CRITICAL。官方 `mysql:8.4` 仍报告 `gosu` 标准库以及 MySQL Shell Python 依赖的 HIGH/CRITICAL 结果，整体门禁仍失败。

## 遗留风险

- TLS 自签名证书仅适合本地验收；公网需要受信任证书、证书轮换和外部入口策略。
- Secrets 覆盖依赖 Docker Compose 版本支持的 `!reset` 合并标签；部署前必须执行配置解析检查。
- 真实 SMTP、多账号浏览器注册/登录/资料隔离仍需专用测试邮箱，不能用 Mailpit 结果替代。

## 下一步

- 评估官方 MySQL 镜像的供应链修复路径；在没有明确例外和上游修复前，不把整体镜像漏洞门禁标记为通过。
- 使用真实 SMTP 测试账号完成浏览器注册、登录、退出和两账号资料隔离；不提交账号和验证码。
- 再评估持续混合负载和跨节点高可用，不将本机 Compose 演练扩写成生产承诺。
