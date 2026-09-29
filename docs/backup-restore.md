# V1 备份与恢复演练

## 目标

Mizuki Archive 的资料由两部分组成：MySQL 中的元数据/会话/标签，以及 `APP_DATA_DIR` 下的原件。只备份数据库或只复制文件目录都不能保证资料可恢复。本指南用于本地演练，正式部署时应把备份存放到受控、加密且与运行主机隔离的位置。

## 备份前检查

- 使用独立的备份目录，不要把备份放进 Git 仓库；`backups/` 已按运行时产物处理。
- 确认 API 没有正在进行的迁移或大文件上传；数据库导出使用事务快照，文件复制前后需要做哈希清单。
- 不要使用 `docker compose down -v`。它会删除 Docker 数据卷，不是备份工具。

## Windows + Docker 本地备份

以下命令从仓库根目录执行；密码从本机 `.env` 读取，不要把命令输出或密码提交到文档/日志。

```powershell
Get-Content .env | ForEach-Object {
  if ($_ -match '^([^#=]+)=(.*)$') { Set-Item -Path ("Env:" + $matches[1]) -Value $matches[2] }
}
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$backupDir = Join-Path (Get-Location) "..\mizuki-backups\$stamp"
New-Item -ItemType Directory -Force -Path $backupDir | Out-Null

docker compose exec -T mysql sh -c 'exec mysqldump -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" --single-transaction --routines --triggers "$MYSQL_DATABASE"' > (Join-Path $backupDir 'archive.sql')
Copy-Item -LiteralPath 'data\files' -Destination (Join-Path $backupDir 'files') -Recurse -Force
Get-ChildItem -LiteralPath (Join-Path $backupDir 'files') -File -Recurse | Get-FileHash -Algorithm SHA256 | ConvertTo-Json | Set-Content -Encoding utf8 (Join-Path $backupDir 'files.sha256.json')
```

备份完成后至少检查 `archive.sql` 非空、文件数量符合预期，并将备份目录设置为只有本人可读。生产环境还应加密并保留多个时间点。

## 恢复到隔离环境

恢复演练必须使用新的数据库名和新的文件目录，不能覆盖当前开发库或 `data/files`。下面的 `restoreDir` 和数据库名只是示例。

```powershell
$restoreDir = Join-Path (Get-Location) '..\mizuki-restore-test\files'
New-Item -ItemType Directory -Force -Path $restoreDir | Out-Null
Copy-Item -LiteralPath '..\mizuki-backups\<时间戳>\files\*' -Destination $restoreDir -Recurse -Force

$restoreDb = 'mizuki_restore_test_<时间戳>'
docker compose exec -T mysql mysql -uarchive -p -e "CREATE DATABASE $restoreDb CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
Get-Content '..\mizuki-backups\<时间戳>\archive.sql' | docker compose exec -T mysql mysql -uarchive -p $restoreDb

$env:MYSQL_DSN = 'archive:<隔离密码>@tcp(127.0.0.1:3307)/<restoreDb>?parseTime=true&charset=utf8mb4'
$env:APP_DATA_DIR = $restoreDir
$env:APP_LISTEN_ADDR = '127.0.0.1:18080'
go run ./cmd/server
```

实际演练中不要把 `<隔离密码>` 写进脚本；可以临时设置 `$env:MYSQL_DSN`，结束后关闭该终端。若导入用户无权创建数据库，应由管理员预先创建隔离库，再使用普通账号导入。

## 校验清单

1. 访问恢复实例的 `/healthz`，确认服务启动且迁移版本未异常增加。
2. 登录恢复实例，逐条检查资料数量、展示名称、原始文件名、大小、类型、收藏和标签。
3. 对每个未删除资源读取恢复目录中的 `storage_key` 文件，计算 SHA-256，与数据库 `sha256` 字段完全一致。
4. 下载 TXT/图片/PDF 各一份，确认内容与备份前一致；检查软删除资源仍不可见。
5. 检查标签筛选、名称编辑、预览和删除行为，确认恢复后仍符合权限和安全策略。
6. 记录演练时间、备份版本、资源数量、哈希校验结果和失败项，追加到对应 `docs/changes/` 记录。

恢复完成后删除隔离数据库和恢复目录，确认没有把恢复资料误放回开发目录。正式环境恢复前必须先暂停写入、保留原环境快照并安排回滚方案。