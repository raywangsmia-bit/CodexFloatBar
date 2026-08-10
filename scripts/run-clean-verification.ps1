param(
    [string]$VerificationRoot = ""
)

$ErrorActionPreference = "Stop"
$kitRoot = Split-Path -Parent $PSScriptRoot
$runtimeDataRoot = [IO.Path]::GetFullPath((Join-Path $kitRoot "runtime-data"))
$metadataPath = Join-Path $kitRoot "resources\release-metadata.psd1"
$metadata = Import-PowerShellDataFile -LiteralPath $metadataPath
if (-not $VerificationRoot) {
    $runName = "clean-verification-{0}-{1}" -f `
        (Get-Date -Format "yyyyMMdd-HHmmss"), `
        $PID
    $VerificationRoot = Join-Path $runtimeDataRoot $runName
}

$VerificationRoot = [IO.Path]::GetFullPath($VerificationRoot)
$runtimePrefix = $runtimeDataRoot.TrimEnd([IO.Path]::DirectorySeparatorChar) + `
    [IO.Path]::DirectorySeparatorChar
if (-not $VerificationRoot.StartsWith(
    $runtimePrefix,
    [StringComparison]::OrdinalIgnoreCase
)) {
    throw "verification root must stay inside $runtimeDataRoot"
}
if (Test-Path -LiteralPath $VerificationRoot) {
    throw "verification root already exists: $VerificationRoot"
}

function Get-OptionalRegistryValue {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,
        [Parameter(Mandatory = $true)]
        [string]$Name
    )

    try {
        $item = Get-ItemProperty -LiteralPath $Path -ErrorAction Stop
    } catch [System.Management.Automation.ItemNotFoundException] {
        return $null
    }
    $property = $item.PSObject.Properties[$Name]
    if ($null -eq $property) {
        return $null
    }
    return $property.Value
}

function Assert-KitChecksums {
    $checksumsPath = Join-Path $kitRoot "SHA256SUMS.txt"
    foreach ($line in Get-Content -LiteralPath $checksumsPath -Encoding UTF8) {
        if ($line -notmatch '^([0-9a-f]{64})  (.+)$') {
            throw "invalid checksum line: $line"
        }
        $expected = $Matches[1]
        $relativePath = $Matches[2]
        $segments = @($relativePath -split "/")
        if ([IO.Path]::IsPathRooted($relativePath) -or $segments -contains "..") {
            throw "unsafe checksum path: $relativePath"
        }
        $path = Join-Path $kitRoot $relativePath.Replace(
            "/",
            [IO.Path]::DirectorySeparatorChar
        )
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "verification kit file is missing: $relativePath"
        }
        $actual = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $expected) {
            throw "verification kit checksum mismatch: $relativePath"
        }
    }
}

function Assert-CleanUserState {
    $nextRegistryPath = "HKCU:\Software\$($metadata.AppId)"
    $uninstallRegistryPath = `
        "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\$($metadata.UninstallKey)"
    foreach ($path in @($nextRegistryPath, $uninstallRegistryPath)) {
        if (Test-Path -LiteralPath $path) {
            throw "clean verification requires an absent registry key: $path"
        }
    }

    $runRegistryPath = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
    foreach ($name in @($metadata.StartupValueName, "CodexFloatingBar")) {
        if ($null -ne (Get-OptionalRegistryValue -Path $runRegistryPath -Name $name)) {
            throw "clean verification requires an absent startup value: $name"
        }
    }

    $localAppData = [Environment]::GetFolderPath("LocalApplicationData")
    foreach ($name in @($metadata.AppId, "CodexFloatingBar")) {
        $dataPath = Join-Path $localAppData $name
        if (Test-Path -LiteralPath $dataPath) {
            throw "clean verification requires an absent application data path: $dataPath"
        }
    }

    $processes = @(Get-Process -ErrorAction SilentlyContinue | Where-Object {
        $_.ProcessName -like "CodexFloatingBar.Next*" -or
        $_.ProcessName -like "CodexFloatingBar-v0.1.3*"
    })
    if ($processes.Count -ne 0) {
        throw "clean verification requires all CodexFloatingBar processes to be stopped"
    }
}

$artifactRoot = Join-Path $kitRoot "artifacts"
$finalInstaller = Join-Path `
    $artifactRoot `
    ("{0}-{1}-Setup.exe" -f $metadata.AppId, $metadata.Version)
$previousInstaller = Join-Path $artifactRoot "$($metadata.AppId)-previous-Setup.exe"
$wpfExecutable = Join-Path $artifactRoot "CodexFloatingBar-v0.1.3-win-x64.exe"
$installerReportRoot = Join-Path $VerificationRoot "installer"
$wpfReportRoot = Join-Path $VerificationRoot "wpf"
$shortInstallRoot = Join-Path `
    ([IO.Path]::GetTempPath()) `
    ("codexfloatingbar-install-test-{0}" -f [guid]::NewGuid().ToString("N"))
$summaryPath = Join-Path $VerificationRoot "summary.json"
$result = [ordered]@{
    passed = $false
    os = [Environment]::OSVersion.VersionString
    architecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    kitChecksumsValid = $false
    cleanPreflightPassed = $false
    installerReport = Join-Path $installerReportRoot "verification.json"
    wpfReport = Join-Path $wpfReportRoot "verification.json"
    errors = @()
}

New-Item -ItemType Directory -Path $VerificationRoot -Force | Out-Null
try {
    Assert-KitChecksums
    $result.kitChecksumsValid = $true
    Assert-CleanUserState
    $result.cleanPreflightPassed = $true

    & (Join-Path $PSScriptRoot "test-installer.ps1") `
        -InstallerPath $finalInstaller `
        -PreviousInstallerPath $previousInstaller `
        -VerificationRoot $installerReportRoot `
        -InstallationDirectory $shortInstallRoot `
        -MetadataPath $metadataPath
    if ($LASTEXITCODE -ne 0) {
        throw "installer verification failed with exit code $LASTEXITCODE"
    }

    & (Join-Path $PSScriptRoot "test-wpf-rollback.ps1") `
        -WpfExecutablePath $wpfExecutable `
        -VerificationRoot $wpfReportRoot
    if ($LASTEXITCODE -ne 0) {
        throw "WPF rollback verification failed with exit code $LASTEXITCODE"
    }

    $installerReport = Get-Content `
        -LiteralPath $result.installerReport `
        -Raw `
        -Encoding UTF8 | ConvertFrom-Json
    $wpfReport = Get-Content `
        -LiteralPath $result.wpfReport `
        -Raw `
        -Encoding UTF8 | ConvertFrom-Json
    $result.passed = $installerReport.passed -and $wpfReport.passed
} catch {
    $result.errors += $_.Exception.Message
} finally {
    $result | ConvertTo-Json -Depth 6 | Set-Content `
        -LiteralPath $summaryPath `
        -Encoding UTF8
}

$result | ConvertTo-Json -Depth 6
if (-not $result.passed) {
    exit 1
}
exit 0
