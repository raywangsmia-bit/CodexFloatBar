param(
    [string]$ExecutablePath = "",
    [string]$OutputDirectory = ""
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
if (-not $ExecutablePath) {
    $ExecutablePath = Join-Path $projectRoot "bin\codexfloatingbar-native-p0.exe"
}
if (-not $OutputDirectory) {
    $OutputDirectory = Join-Path $projectRoot "runtime-data"
}

$ExecutablePath = [IO.Path]::GetFullPath($ExecutablePath)
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$reportPath = Join-Path $OutputDirectory "native-selftest.json"
$logPath = Join-Path $OutputDirectory "native-selftest.log"
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
Remove-Item -LiteralPath $reportPath, $logPath -Force -ErrorAction SilentlyContinue

$arguments = @(
    "--self-test-output=`"$reportPath`"",
    "--log-file=`"$logPath`""
)
$process = Start-Process `
    -FilePath $ExecutablePath `
    -ArgumentList $arguments `
    -WorkingDirectory (Split-Path -Parent $projectRoot) `
    -WindowStyle Hidden `
    -PassThru `
    -Wait

if (-not (Test-Path -LiteralPath $reportPath -PathType Leaf)) {
    throw "native self-test exited without writing $reportPath"
}
$report = Get-Content -LiteralPath $reportPath -Raw -Encoding UTF8 | ConvertFrom-Json
$report | ConvertTo-Json -Depth 8
if ($process.ExitCode -ne 0 -or -not $report.passed) {
    throw "native self-test failed with exit code $($process.ExitCode); see $reportPath"
}
