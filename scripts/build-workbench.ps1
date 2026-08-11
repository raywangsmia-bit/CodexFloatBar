param(
    [string]$OutputPath = "",
    [switch]$SkipTests,
    [switch]$TestOnly
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
if (-not $OutputPath) {
    $OutputPath = Join-Path $projectRoot "bin\CodexFloatingBar.Workbench.exe"
}
$env:GOCACHE = Join-Path $projectRoot ".cache\go-build"
$env:GOMODCACHE = Join-Path $projectRoot ".cache\go-mod"
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"

$workbenchSources = @(
    "main_workbench.go"
    "runtime_paths_windows.go"
    "workbench_server.go"
    "edge_job.go"
    "edge_job_windows.go"
    "bundle.go"
    "version.go"
    "presentation_contract_windows.go"
    "surface_compositor_windows.go"
    "directwrite_text_mask_windows.go"
    "text_mask_windows.go"
    "win32_windows.go"
    "geometry.go"
    "integer_math.go"
)
$workbenchTestSources = $workbenchSources + @(
    "workbench_server_test.go"
    "edge_job_test.go"
    "edge_job_windows_test.go"
)

Push-Location $projectRoot
try {
    if (-not $SkipTests) {
        go test -tags workbench @workbenchTestSources
        if ($LASTEXITCODE -ne 0) {
            throw "workbench tests failed with exit code $LASTEXITCODE"
        }
        go vet -tags workbench @workbenchSources
        if ($LASTEXITCODE -ne 0) {
            throw "workbench vet failed with exit code $LASTEXITCODE"
        }
    }

    if (-not $TestOnly) {
        $OutputPath = [IO.Path]::GetFullPath($OutputPath)
        $outputDirectory = Split-Path -Parent $OutputPath
        New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null
        go build -tags workbench -trimpath -o $OutputPath @workbenchSources
        if ($LASTEXITCODE -ne 0) {
            throw "workbench build failed with exit code $LASTEXITCODE"
        }
    }
} finally {
    Pop-Location
}

[ordered]@{
    executable = if ($TestOnly) { $null } else { $OutputPath }
    buildTag = "workbench"
    testsRun = -not $SkipTests
    testOnly = [bool]$TestOnly
} | ConvertTo-Json
