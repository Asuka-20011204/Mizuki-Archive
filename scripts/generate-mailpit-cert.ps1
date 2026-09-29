$ErrorActionPreference = 'Stop'

# 生成仅供本机 Mailpit 使用的短期自签名证书；输出目录默认位于仓库外，避免证书进入 Git。
$outputDirectory = if ($args.Count -gt 0) {
    [IO.Path]::GetFullPath($args[0])
} else {
    Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'mizuki-mailpit-tls'
}

New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
$certificatePath = Join-Path $outputDirectory 'mailpit.crt'
$keyPath = Join-Path $outputDirectory 'mailpit.key'

# SAN 同时覆盖 localhost 和 127.0.0.1，匹配本地 API 的 SMTP 主机配置。
& openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 7 `
    -keyout $keyPath -out $certificatePath `
    -subj '/CN=localhost' `
    -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1'
if ($LASTEXITCODE -ne 0) {
    throw 'openssl failed to generate the Mailpit certificate'
}

Write-Output "Mailpit TLS files generated in $outputDirectory"
