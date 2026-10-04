# V6 独立 Docker 本地部署与恢复

此编排仅面向单机本地演练，不是公网生产模板。`compose.deploy.yaml` 使用独立项目名与独立 Docker 卷，不覆盖开发环境 `compose.yaml` 的 MySQL；Web 仅监听 `127.0.0.1:18080`，API、Worker、数据库都不映射宿主机端口。默认数据库任务模式，无需 RabbitMQ/Redis；V3/V4 仍可通过现有开发编排演练。

## 1. 准备

准备独立于 Git 仓库的部署变量文件，例如 `..\mizuki-deploy.env`，权限限制在自己；不要用真实个人资料做演示。至少设置 `MYSQL_DATABASE`、`MYSQL_USER`、`MYSQL_PASSWORD`、`MYSQL_ROOT_PASSWORD`、`MYSQL_MIGRATION_USER`、`MYSQL_MIGRATION_PASSWORD`、`APP_ADMIN_USERNAME`、`APP_ADMIN_PASSWORD_HASH`。不同组件的密码应不同，数据库密码此版需符合 Go MySQL DSN 的格式；本地演练建议随机字母数字。用 `go run ./cmd/hash-password` 交互式生成管理员哈希，不要把明文放进环境文件。哈希中的 `# V6 独立 Docker 本地部署与恢复

此编排仅面向单机本地演练，不是公网生产模板。`compose.deploy.yaml` 使用独立项目名与独立 Docker 卷，不覆盖开发环境 `compose.yaml` 的 MySQL；Web 仅监听 `127.0.0.1:18080`，API、Worker、数据库都不映射宿主机端口。默认数据库任务模式，无需 RabbitMQ/Redis；V3/V4 仍可通过现有开发编排演练。

## 1. 准备

 在 env 文件里需用**单引号**包围，不能提交 Git。若要在部署编排中启用邮箱注册/登录，再额外设置 `SMTP_HOST`、`SMTP_PORT`、`SMTP_USERNAME`、`SMTP_PASSWORD`、`SMTP_FROM` 和至少 32 字节的 `EMAIL_CODE_SECRET`；不完整配置会让 API 启动失败。

V14 支持把高敏感配置放在外部 Secret 文件中。程序对每个支持项读取 `NAME_FILE`，文件末尾允许有换行；同一项不要同时提供非空 `NAME` 和 `NAME_FILE`。使用 `compose.secrets.yaml` 时，在部署变量文件中提供 `MYSQL_DSN_SECRET_FILE`、`MIGRATION_DSN_SECRET_FILE`、`ADMIN_PASSWORD_HASH_SECRET_FILE`、`SMTP_PASSWORD_SECRET_FILE` 和 `EMAIL_CODE_SECRET_SECRET_FILE`，并将它们指向仓库外的权限受限文件。启动命令如下：

```powershell
docker compose --env-file $config -f compose.deploy.yaml -f compose.secrets.yaml config --quiet
docker compose --env-file $config -f compose.deploy.yaml -f compose.secrets.yaml up --build -d
```

基础编排仍可使用环境变量启动；Secrets 覆盖只用于明确准备好这些文件的环境，不要创建空文件来绕过配置校验。

从仓库根目录执行：

```powershell
$config = '..\mizuki-deploy.env'
docker compose --env-file $config -f compose.deploy.yaml config --quiet
docker compose --env-file $config -f compose.deploy.yaml up --build -d
docker compose --env-file $config -f compose.deploy.yaml ps
Invoke-WebRequest http://localhost:18080/healthz
Invoke-WebRequest http://localhost:18080/readyz
```

两条探针均返回 204 才检查浏览器 `http://localhost:18080/`；登录名默认由 `APP_ADMIN_USERNAME` 指定，密码是生成哈希时输入的原始明文。`/healthz` 仅检查进程；`/readyz` 额外限时检测 MySQL，不将数据库错误返回给客户端。修改 `DEPLOY_WEB_PORT` 时同时设置匹配的 `DEPLOY_APP_ORIGIN`（如 `http://localhost:18081`），否则写请求会因 Origin 不匹配被拒绝。

镜像中 Go 程序和 Nginx 均以非 root 身份运行；API/Worker 只读根文件系统，仅 `/srv/data` 命名卷和 `/tmp` 可写，并限制内存、进程数及容器权限。启动时由 `db-access-bootstrap` 创建独立迁移账号并收窄运行账号为 `SELECT/INSERT/UPDATE/DELETE`，一次性 `migrate` 容器使用迁移账号执行 DDL 和管理员归属初始化，API/Worker 不再自动迁移。Nginx 已对认证和 API 入口增加边缘限流，并覆盖 `X-Real-IP`；API 只有在此受控编排中才开启 `APP_TRUST_PROXY_HEADERS=true`。SMTP 配置只注入 API，Worker 不接收邮箱用户名、密码或验证码密钥。V14 通过可选覆盖补齐 TLS 和 Secrets 文件注入；镜像漏洞扫描需在安装 Trivy 的发布环境执行，不要将本机绑定改成公网地址直接公开。

升级至 OCR 版本时，API/migrate 使用 `api` 构建目标，Worker 使用安装 Poppler、Tesseract 和中英语言包的 `ocr-worker` 目标；Worker 的 `/tmp` 受 256 MiB 临时卷限制。正式升级 `021_ocr_metadata.sql` 前应冻结写入并成对备份数据库和原件，再运行独立迁移服务并切换 API/Worker 镜像；单机镜像测试不代表峰值资源已经达标。

## 1.1 TLS 入口覆盖

`compose.tls.yaml` 将 Web 切换到独立 TLS 配置，默认只把宿主机环回端口 `18443` 映射到容器 `8443`；HTTP 端口在覆盖中移除，容器内部的 8080 仅用于把受控入口重定向到 HTTPS。先准备仓库外的证书和私钥，再设置 `TLS_CERT_FILE`、`TLS_KEY_FILE`、`DEPLOY_TLS_PORT`，并将 `DEPLOY_APP_ORIGIN` 改为相同的 `https://` 地址：

```powershell
docker compose --env-file $config -f compose.deploy.yaml -f compose.tls.yaml config --quiet
docker compose --env-file $config -f compose.deploy.yaml -f compose.tls.yaml up --build -d
Invoke-WebRequest https://localhost:18443/readyz
```

自签名证书只用于本地验收；公网部署应由受信任证书机构或外部入口层管理证书，并将宿主机端口映射为标准 HTTPS 入口。TLS 入口仍不代表跨节点高可用。

## 1.2 镜像漏洞扫描

发布前在安装 Trivy 的机器执行 `scripts/scan-images.ps1`；脚本只扫描 `compose.deploy.yaml` 解析出的镜像，对未修复的 HIGH/CRITICAL 漏洞返回失败：

```powershell
pwsh -File .\scripts\scan-images.ps1 -ComposeEnvFile $config -DBRepository ghcr.io/aquasecurity/trivy-db -Build
```

未安装 Trivy、镜像未构建或扫描失败都应阻止发布；不要把“脚本存在”当作扫描通过。

## 2. 停机、备份

停止 Web/API/Worker 以冻结写入，但保留 MySQL 容器和所有卷。将备份放到**仓库外**，不要在 Git 或截图中展示 SQL 与原件。下述命令适用于 PowerShell 7；如使用 Windows PowerShell 5.1，需用 UTF-8 安全的导出方式避免原始 SQL 字节因管道转码损坏。

```powershell
$config = '..\mizuki-deploy.env'
$backup = Join-Path (Resolve-Path ..).Path ("mizuki-v6-backup-" + (Get-Date -Format yyyyMMdd-HHmmss))
New-Item -ItemType Directory -Path $backup | Out-Null
docker compose --env-file $config -f compose.deploy.yaml stop web worker api
docker compose --env-file $config -f compose.deploy.yaml exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -uroot --single-transaction --routines --triggers "$MYSQL_DATABASE"' > (Join-Path $backup 'archive.sql')
$container = docker compose --env-file $config -f compose.deploy.yaml ps -aq api
docker cp "${container}:/srv/data/." (Join-Path $backup 'data')
Get-ChildItem (Join-Path $backup 'data') -File -Recurse | Get-FileHash -Algorithm SHA256 | Select-Object Path,Hash | ConvertTo-Json | Set-Content -Encoding utf8 (Join-Path $backup 'data.sha256.json')
```

确认 SQL 非空、文件数量与摘要清单合理；异地加密保存完整备份后再恢复原服务：`docker compose --env-file $config -f compose.deploy.yaml up -d`。备份目录不能处于 Web 静态目录下，不能进仓库或未经授权共享。

## 3. 隔离恢复演练

用 `-p mizuki-v6-restore` **强制使用新项目、独立数据库与文件卷**；演练的 Web 端口和 Origin 同时改成 18081，不能指向现有开发或部署库。先启动隔离 MySQL、导入备份，然后复制文件，最后启动应用：

```powershell
$env:DEPLOY_WEB_PORT='18081'
$env:DEPLOY_APP_ORIGIN='http://localhost:18081'
docker compose -p mizuki-v6-restore --env-file $config -f compose.deploy.yaml up -d mysql
Get-Content (Join-Path $backup 'archive.sql') | docker compose -p mizuki-v6-restore --env-file $config -f compose.deploy.yaml exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot "$MYSQL_DATABASE"'
docker compose -p mizuki-v6-restore --env-file $config -f compose.deploy.yaml up -d api
$restoreApi = docker compose -p mizuki-v6-restore --env-file $config -f compose.deploy.yaml ps -q api
docker cp (Join-Path $backup 'data/.') "${restoreApi}:/srv/data"
$volume = 'mizuki-v6-restore_archive_data'
$labels = docker volume inspect $volume --format '{{json .Labels}}' | ConvertFrom-Json
if ($labels.'com.docker.compose.project' -ne 'mizuki-v6-restore' -or $labels.'com.docker.compose.volume' -ne 'archive_data') { throw '恢复卷不属于隔离项目' }
docker run --rm -v "${volume}:/srv/data" alpine:3.22 chown -R 10001:10001 /srv/data
docker compose -p mizuki-v6-restore --env-file $config -f compose.deploy.yaml up -d worker web
Invoke-WebRequest http://localhost:18081/readyz
```

生产容器已移除 `CAP_CHOWN`，因此不能用 `docker compose exec -u 0 api chown` 修改恢复文件。只在核对 Docker 卷项目标签后让临时维护容器处理隔离卷所有权；勿挂载正式卷执行这一步。

## 多 API/Worker 本机演练

独立部署编排保持 API 无状态，并将原件目录挂载到共享 `archive_data` 卷，因此可以在同一台机器上演练多个 API 和 Worker：

```powershell
docker compose --env-file $config -f compose.deploy.yaml up --build --scale api=2 --scale worker=2 -d
docker compose --env-file $config -f compose.deploy.yaml ps
Invoke-WebRequest http://localhost:18080/readyz
```

Web 容器通过 Docker 内置 DNS 定期解析 `api` 服务名；GET/HEAD 等读请求遇到连接错误或 502/503/504 时允许切换副本。写请求不配置 `non_idempotent` 自动重试，避免登录、上传、验证码发送或任务创建因代理重试产生重复副作用。这个演练只能证明单机多进程共享 MySQL、文件卷和会话的基础，不等于 MySQL、Redis、RabbitMQ、文件存储和入口层已经具备跨节点高可用。

停止多副本演练仍使用：

```powershell
docker compose --env-file $config -f compose.deploy.yaml down
```

对比资料条数、元数据、每份文件的 SHA-256、下载字节和派生文件/关键词检索；失败时不要删原备份。演练结束后确认目标项目名，再执行 `docker compose -p mizuki-v6-restore --env-file $config -f compose.deploy.yaml down -v` **仅删除隔离恢复的卷**。绝不可对原部署项目使用 `down -v`。删除演练环境变量：`Remove-Item Env:DEPLOY_WEB_PORT,Env:DEPLOY_APP_ORIGIN -ErrorAction SilentlyContinue`。

## 4. 回滚与界限

新版本升级前暂停写入、创建数据库+文件一致的备份并保存旧镜像。升级 `013` 链接摘要迁移时，必须先停止旧版 API 的卡片写入；新旧实例并行写入会留下 `location_key` 空值，导致重复提醒漏报。切换后在维护窗口核查同一迁移范围内 `location_key IS NULL` 的卡片并按迁移回填逻辑修复，不删除卡片数据。若只改镜像且没有不可逆的迁移，可回退旧镜像并在恢复验证后开放访问；若迁移不兼容，必须隔离恢复数据库**及同一时点的文件目录**，不能只回退程序。数据库模式 Worker 和 RabbitMQ 模式 Worker 不得同时消费同一任务。本编排中的 `mysql_data` 与 `archive_data` 是长期数据，`docker compose down` 不会删除它们，`down -v` 会删除。
