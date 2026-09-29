param(
    [string]$ComposeEnvFile = '',
    [string]$DBRepository = '',
    [switch]$Build
)

$ErrorActionPreference = 'Stop'

# scan-images 使用 Trivy 扫描部署编排引用的镜像，并在发现高危或严重漏洞时失败。
function Invoke-Compose {
    param([string[]]$Arguments)

    if ([string]::IsNullOrWhiteSpace($ComposeEnvFile)) {
        & docker compose -f compose.deploy.yaml @Arguments
    } else {
        & docker compose --env-file $ComposeEnvFile -f compose.deploy.yaml @Arguments
    }
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose failed with exit code $LASTEXITCODE"
    }
}

if (-not (Get-Command trivy -ErrorAction SilentlyContinue)) {
    throw 'Trivy is required; install it before running this gate.'
}

if ($Build) {
    Invoke-Compose -Arguments @('build', 'api', 'web')
}

$images = if ([string]::IsNullOrWhiteSpace($ComposeEnvFile)) {
    & docker compose -f compose.deploy.yaml config --images
} else {
    & docker compose --env-file $ComposeEnvFile -f compose.deploy.yaml config --images
}
if ($LASTEXITCODE -ne 0) {
    throw "docker compose config failed with exit code $LASTEXITCODE"
}

$uniqueImages = $images | Where-Object { $_ -and $_.Trim() } | Sort-Object -Unique
if ($uniqueImages.Count -eq 0) {
    throw 'No images were found in compose.deploy.yaml.'
}

foreach ($image in $uniqueImages) {
    Write-Output "Scanning $image"
    $trivyArguments = @('image', '--scanners', 'vuln', '--severity', 'HIGH,CRITICAL', '--ignore-unfixed', '--exit-code', '1', '--format', 'table')
    if (-not [string]::IsNullOrWhiteSpace($DBRepository)) {
        $trivyArguments += @('--db-repository', $DBRepository)
    }
    $trivyArguments += $image
    & trivy @trivyArguments
    if ($LASTEXITCODE -ne 0) {
        throw "Trivy scan failed or found high/critical vulnerabilities in $image; inspect the Trivy output above"
    }
}
