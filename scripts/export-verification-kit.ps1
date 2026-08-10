param(
    [string]$OutputDirectory = "",
    [string]$InstallerPath = "",
    [Parameter(Mandatory = $true)]
    [string]$PreviousInstallerPath,
    [string]$WpfExecutablePath = "",
    [string]$MetadataPath = ""
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot "release-common.ps1")
if (-not $MetadataPath) {
    $MetadataPath = Join-Path $projectRoot "resources\release-metadata.psd1"
}
$metadata = Get-NativeReleaseMetadata -Path $MetadataPath
if (-not $InstallerPath) {
    $InstallerPath = Join-Path `
        $projectRoot `
        "release\next-beta-$($metadata.Version)\$($metadata.AppId)-$($metadata.Version)-Setup.exe"
}
if (-not $WpfExecutablePath) {
    $WpfExecutablePath = Join-Path `
        (Split-Path -Parent (Split-Path -Parent $projectRoot)) `
        "release-test\CodexFloatingBar-v0.1.3-win-x64.exe"
}
if (-not $OutputDirectory) {
    $OutputDirectory = Join-Path $projectRoot "release"
}

$InstallerPath = [IO.Path]::GetFullPath($InstallerPath)
$PreviousInstallerPath = [IO.Path]::GetFullPath($PreviousInstallerPath)
$WpfExecutablePath = [IO.Path]::GetFullPath($WpfExecutablePath)
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
foreach ($path in @($InstallerPath, $PreviousInstallerPath, $WpfExecutablePath)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "verification artifact was not found: $path"
    }
}

$kitName = "$($metadata.AppId)-$($metadata.Version)-verification-kit"
$kitRoot = Join-Path $OutputDirectory $kitName
$zipPath = Join-Path $OutputDirectory "$kitName.zip"
$zipChecksumPath = "$zipPath.sha256"
foreach ($path in @($kitRoot, $zipPath, $zipChecksumPath)) {
    if (Test-Path -LiteralPath $path) {
        throw "refusing to replace an existing verification-kit output: $path"
    }
}

$artifactRoot = Join-Path $kitRoot "artifacts"
$scriptRoot = Join-Path $kitRoot "scripts"
$resourceRoot = Join-Path $kitRoot "resources"
New-Item `
    -ItemType Directory `
    -Path $artifactRoot, $scriptRoot, $resourceRoot `
    -Force | Out-Null

Copy-Item `
    -LiteralPath $InstallerPath `
    -Destination (Join-Path $artifactRoot (Split-Path -Leaf $InstallerPath))
Copy-Item `
    -LiteralPath $PreviousInstallerPath `
    -Destination (Join-Path $artifactRoot "$($metadata.AppId)-previous-Setup.exe")
Copy-Item `
    -LiteralPath $WpfExecutablePath `
    -Destination (Join-Path $artifactRoot "CodexFloatingBar-v0.1.3-win-x64.exe")
foreach ($scriptName in @(
    "release-common.ps1",
    "run-clean-verification.ps1",
    "test-installer.ps1",
    "test-wpf-rollback.ps1"
)) {
    Copy-Item `
        -LiteralPath (Join-Path $PSScriptRoot $scriptName) `
        -Destination (Join-Path $scriptRoot $scriptName)
}
Copy-Item `
    -LiteralPath $MetadataPath `
    -Destination (Join-Path $resourceRoot "release-metadata.psd1")

$readme = @"
# CodexFloatingBar Next clean Windows verification kit

This kit verifies $($metadata.AppId) $($metadata.Version) without Go, Node, NSIS,
the .NET SDK, or the project source tree. Run it only in a disposable clean
Windows x64 user profile. The preflight refuses existing Next/WPF registry keys,
startup values, processes, or LocalAppData directories.

From PowerShell:

    .\scripts\run-clean-verification.ps1

The command performs a previous-version install, an in-place upgrade, occupied
install/uninstall exit-code checks, normal uninstall cleanup, and a WPF v0.1.3
rollback launch. Reports are written below runtime-data/. No stress, idle
performance, or native self-test is executed.
"@
[IO.File]::WriteAllText(
    (Join-Path $kitRoot "README.md"),
    $readme,
    [Text.UTF8Encoding]::new($false)
)

$checksumsPath = Join-Path $kitRoot "SHA256SUMS.txt"
$files = @(Get-ChildItem -LiteralPath $kitRoot -Recurse -File | Sort-Object FullName)
$checksumLines = foreach ($file in $files) {
    $relativePath = [IO.Path]::GetRelativePath($kitRoot, $file.FullName).Replace("\", "/")
    $hash = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    "{0}  {1}" -f $hash, $relativePath
}
[IO.File]::WriteAllLines(
    $checksumsPath,
    $checksumLines,
    [Text.UTF8Encoding]::new($false)
)

Compress-Archive -LiteralPath $kitRoot -DestinationPath $zipPath -CompressionLevel Optimal
$zipHash = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
[IO.File]::WriteAllText(
    $zipChecksumPath,
    "$zipHash  $(Split-Path -Leaf $zipPath)$([Environment]::NewLine)",
    [Text.UTF8Encoding]::new($false)
)

[ordered]@{
    passed = $true
    kit = $kitRoot
    zip = $zipPath
    zipSha256 = $zipHash
    checksums = $checksumsPath
} | ConvertTo-Json
