param(
    [Parameter(Mandatory = $true)]
    [int]$TargetProcessId,

    [int]$DurationSeconds = 300,

    [Parameter(Mandatory = $true)]
    [string]$OutputPath
)

$ErrorActionPreference = "Stop"
$resolvedOutput = [System.IO.Path]::GetFullPath($OutputPath)
$outputDirectory = [System.IO.Path]::GetDirectoryName($resolvedOutput)
New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null

$process = Get-Process -Id $TargetProcessId
$process.Refresh()
$startedAt = Get-Date
$startedCpu = $process.CPU
$peakWorkingSet = [int64]$process.WorkingSet64
$peakPrivate = [int64]$process.PrivateMemorySize64
$samples = 0

for ($second = 0; $second -lt $DurationSeconds; $second++) {
    Start-Sleep -Seconds 1
    $process = Get-Process -Id $TargetProcessId
    $process.Refresh()
    $peakWorkingSet = [Math]::Max($peakWorkingSet, [int64]$process.WorkingSet64)
    $peakPrivate = [Math]::Max($peakPrivate, [int64]$process.PrivateMemorySize64)
    $samples++
}

$finishedAt = Get-Date
$process = Get-Process -Id $TargetProcessId
$process.Refresh()
$elapsedSeconds = ($finishedAt - $startedAt).TotalSeconds
$cpuSeconds = $process.CPU - $startedCpu
$logicalProcessors = [Environment]::ProcessorCount
$averageCpuPercent = 100 * $cpuSeconds / ($elapsedSeconds * $logicalProcessors)

$listeners = @(
    Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue |
        Where-Object OwningProcess -eq $TargetProcessId
)

[ordered]@{
    processId = $TargetProcessId
    durationSeconds = [Math]::Round($elapsedSeconds, 3)
    samples = $samples
    logicalProcessors = $logicalProcessors
    cpuSeconds = [Math]::Round($cpuSeconds, 6)
    averageCpuPercent = [Math]::Round($averageCpuPercent, 6)
    finalWorkingSetMiB = [Math]::Round($process.WorkingSet64 / 1MB, 3)
    peakWorkingSetMiB = [Math]::Round($peakWorkingSet / 1MB, 3)
    finalPrivateMiB = [Math]::Round($process.PrivateMemorySize64 / 1MB, 3)
    peakPrivateMiB = [Math]::Round($peakPrivate / 1MB, 3)
    listenerCount = $listeners.Count
    startedAt = $startedAt.ToString("yyyy-MM-dd HH:mm:ss")
    finishedAt = $finishedAt.ToString("yyyy-MM-dd HH:mm:ss")
} | ConvertTo-Json | Set-Content -LiteralPath $resolvedOutput -Encoding UTF8
