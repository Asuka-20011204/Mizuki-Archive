# V13 入口限流与本地邮箱验收环境

## 目的

继续推进路线图中生产化加固和 V8 邮箱验证码闭环：在 Nginx 边缘先削峰，再由 API/Redis 执行业务级限制；同时提供本地 SMTP 捕获服务，让浏览器注册、登录和退出流程可以不依赖真实邮箱提供商重复验收。

## 范围

- Nginx 对密码登录、邮箱验证码接口和 API 入口增加按客户端地址的限流区间。
- Nginx 覆盖 `X-Real-IP`；API 仅在受控独立编排中通过 `APP_TRUST_PROXY_HEADERS=true` 使用该地址，直连模式继续拒绝伪造转发头。
- `compose.yaml` 增加可选 `email` profile 和 Mailpit 服务，暴露本机 SMTP `1025` 与邮件查看页 `8025`；测试证书由脚本生成到仓库外并只读挂载。
- SMTP 适配器支持可选 `SMTP_CA_FILE`；默认仍使用系统信任链，Mailpit 只通过显式 CA 文件加入本地自签名证书。
- 增加客户端地址解析单测、Compose 配置验证和 Nginx 容器语法验证说明。
- 同步路线图、架构、安全、部署、本地启动、目录和 README 文档。

## 设计取舍

- 边缘限流不是权限判断，也不替代验证码重发冷却、一次性消费和 Redis 共享限流；多层限制共同降低突发成本。
- 默认不信任 `X-Real-IP`，避免应用直连时被请求头伪造；只有 API 不暴露宿主端口且前置 Nginx 覆盖该头时才开启。
- Mailpit 是开发捕获器，profile 只在环回地址开放、接受测试凭据并强制 STARTTLS，不是生产邮件服务，也不证明真实邮箱提供商投递成功；生产仍要求受信任证书和强制 TLS 的 SMTP 配置。
- 本轮不接入手机号短信，避免在没有服务商、成本和合规边界时伪造“已支持短信”。

## 验证结果

- `go test ./...` 通过。
- `go vet ./...` 通过。
- `npm run build` 通过，包含 Vue 类型检查和 Vite 生产构建。
- `docker compose -f compose.yaml --profile email config --quiet` 通过。
- `docker compose -f compose.deploy.yaml config --quiet` 通过。
- Web 镜像构建成功，容器内 `nginx -t` 通过。
- 客户端地址单测覆盖直连忽略伪造头、受控代理读取合法头和非法头回退。

## 浏览器与邮件验收追加

- 使用隔离 API `8082`、Vite `5175` 和本地 Mailpit `1025/8025`，没有替换用户原有服务。
- 浏览器打开 `http://localhost:5175/#login`，邮箱注册页请求验证码；Mailpit 通过 STARTTLS 收到测试邮箱 `codex-v13-user2@example.test` 的验证码邮件。
- 使用该验证码完成注册，`/api/me` 返回测试用户；退出返回 `204`；再次请求登录验证码并登录返回 `200`，`/api/me` 再次返回同一测试用户。
- 测试使用合成邮箱，不包含个人资料；隔离 API、Vite 和 Mailpit 已停止，Mailpit 证书保留在仓库外目录并被 `.gitignore` 保护。

## 遗留风险

- 当前入口仍是 HTTP 本地环回绑定；TLS 证书挂载、HTTPS 重定向、密钥托管和镜像漏洞扫描尚未完成。
- Nginx 限流参数是单机基线，尚未在带 TLS 的持续混合负载下校准，也不等于跨节点入口集群。
- 真实外部 SMTP 浏览器闭环仍需用户提供测试邮箱和 SMTP 凭据后验收；Mailpit 仅证明本地链路。

## 下一步

真实外部 SMTP 浏览器回归仍需用户提供测试邮箱和 SMTP 凭据；之后根据目标部署环境选择证书/密钥管理方案，补齐 HTTPS 入口与持续混合负载测试。
