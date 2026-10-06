param(
    [Parameter(Mandatory = $true)]
    [string]$ComposeEnvFile,
    [string]$ProjectName = 'mizuki-bench',
    [string]$ComposeFile = 'compose.deploy.yaml',
    [string]$ComposeOverrideFile = '',
    [string]$OutputDirectory = (Join-Path $env:TEMP ('mizuki-bench-results-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))),
    [int]$DurationSeconds = 300,
    [int]$IntervalSeconds = 1
)

$ErrorActionPreference = 'Stop'

if ($DurationSeconds -le 0) {
    throw 'DurationSeconds 必须大于 0'
}
if ($IntervalSeconds -le 0) {
    throw 'IntervalSeconds 必须大于 0'
}

# write-row 追加一行 CSV，并在首次写入时创建列名。
function Write-Row {
    param(
        [string]$Path,
        [string[]]$Headers,
        [string[]]$Values
    )

    if (-not (Test-Path -LiteralPath $Path)) {
        ($Headers -join ',') | Set-Content -LiteralPath $Path -Encoding utf8
    }
    (($Values | ForEach-Object { '"' + ($_.Replace('"', '""')) + '"' }) -join ',') |
        Add-Content -LiteralPath $Path -Encoding utf8
}

# read-queue-counts 查询任务状态数量；查询失败会终止采样，避免产生不完整报告。
function Read-QueueCounts {
    $composeFiles = @('-f', $ComposeFile)
    if ($ComposeOverrideFile) {
        $composeFiles += @('-f', $ComposeOverrideFile)
    }
    $result = & docker compose -p $ProjectName --env-file $ComposeEnvFile @composeFiles exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -N -B "$MYSQL_DATABASE" -e "SELECT status, COUNT(*) FROM processing_jobs GROUP BY status ORDER BY status; SELECT VARIABLE_VALUE FROM performance_schema.global_status WHERE VARIABLE_NAME = ''Threads_connected'';"'
    if ($LASTEXITCODE -ne 0) {
        throw '读取 processing_jobs 状态失败'
    }
    $counts = @{'pending' = 0; 'processing' = 0; 'succeeded' = 0; 'failed' = 0; 'mysql_threads_connected' = 0}
    foreach ($line in $result) {
        $parts = $line -split "`t"
        if ($parts.Count -eq 2 -and $counts.ContainsKey($parts[0])) {
            $counts[$parts[0]] = [int64]$parts[1]
        } elseif ($parts.Count -eq 1 -and $line -match '^\d+$') {
            $counts['mysql_threads_connected'] = [int64]$line
        }
    }
    return $counts
}

# read-container-rss 读取容器主进程的 VmRSS；失败时留空，避免把 cgroup 内存伪装成 RSS。
function Read-ContainerRssKiB {
    param([string]$Container)

    $value = & docker exec $Container sh -c "awk '/^VmRSS:/ {print `$2}' /proc/1/status" 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $value) {
        return ''
    }
    return ([string]$value).Trim()
}

# main 每秒保存容器 CPU/内存和持久任务状态，输出只属于指定压测项目。
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
$statsPath = Join-Path $OutputDirectory 'docker-stats.csv'
$queuePath = Join-Path $OutputDirectory 'queue-stats.csv'
$statePath = Join-Path $OutputDirectory 'container-state.csv'
$projectContainers = @(& docker ps -a --filter "label=com.docker.compose.project=$ProjectName" --format '{{.Names}}')
if ($LASTEXITCODE -ne 0 -or $projectContainers.Count -eq 0) {
    throw "找不到 Compose 项目 $ProjectName 的容器"
}
$baseline = Read-QueueCounts
$baseline | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $OutputDirectory 'queue-baseline.json') -Encoding utf8
$deadline = (Get-Date).AddSeconds($DurationSeconds)

while ((Get-Date) -lt $deadline) {
    $timestamp = [DateTime]::UtcNow.ToString('o')
    foreach ($container in $projectContainers) {
        $state = & docker inspect --format '{{.State.Status}}|{{.State.Running}}|{{.State.StartedAt}}|{{.State.FinishedAt}}' $container
        if ($LASTEXITCODE -eq 0 -and $state) {
            $stateParts = $state -split '\|', 4
            Write-Row -Path $statePath `
                -Headers @('timestamp', 'container', 'status', 'running', 'started_at', 'finished_at') `
                -Values @($timestamp, $container, $stateParts[0], $stateParts[1], $stateParts[2], $stateParts[3])
        }
    }
    $stats = & docker stats --no-stream --format '{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}'
    if ($LASTEXITCODE -ne 0) {
        throw 'docker stats 执行失败'
    }
    foreach ($line in $stats) {
        $parts = $line -split '\|', 4
        if ($parts.Count -eq 4 -and $projectContainers -contains $parts[0]) {
            $rssKiB = Read-ContainerRssKiB -Container $parts[0]
            Write-Row -Path $statsPath `
                -Headers @('timestamp', 'container', 'cpu_percent', 'memory_usage', 'memory_percent', 'pid1_rss_kib') `
                -Values @($timestamp, $parts[0], $parts[1], $parts[2], $parts[3], $rssKiB)
        }
    }

    $counts = Read-QueueCounts
    Write-Row -Path $queuePath `
        -Headers @('timestamp', 'pending', 'processing', 'succeeded', 'failed', 'mysql_threads_connected') `
        -Values @($timestamp, $counts['pending'], $counts['processing'], $counts['succeeded'], $counts['failed'], $counts['mysql_threads_connected'])

    Start-Sleep -Seconds $IntervalSeconds
}

Write-Output "采样完成：$OutputDirectory"
