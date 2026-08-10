param(
    [ValidateRange(1, 100)]
    [int]$KeepReleases = 2,
    [string]$SigningCertificateThumbprint = "",
    [string]$TimestampUrl = "http://timestamp.digicert.com",
    [switch]$SkipTests,
    [switch]$KeepValidationCaches,
    [switch]$WhatIfCleanup
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot "release-common.ps1")

$metadataPath = Join-Path $projectRoot "resources\release-metadata.psd1"
$metadata = Get-NativeReleaseMetadata -Path $metadataPath
$outputDirectory = Join-Path $projectRoot "release\next-beta-$($metadata.Version)"

& (Join-Path $PSScriptRoot "test-release-static.ps1") `
    -MetadataPath $metadataPath

$buildArguments = @{
    OutputDirectory = $outputDirectory
    MetadataPath = $metadataPath
    TimestampUrl = $TimestampUrl
}
if ($SigningCertificateThumbprint) {
    $buildArguments.SigningCertificateThumbprint = $SigningCertificateThumbprint
}
if ($SkipTests) {
    $buildArguments.SkipTests = $true
}
& (Join-Path $PSScriptRoot "build-release.ps1") @buildArguments

& (Join-Path $PSScriptRoot "verify-release.ps1") `
    -OutputDirectory $outputDirectory `
    -MetadataPath $metadataPath

$cleanupArguments = @{
    KeepReleases = $KeepReleases
    ProtectDirectory = $outputDirectory
}
if (-not $KeepValidationCaches) {
    $cleanupArguments.IncludeValidationCaches = $true
}
if ($WhatIfCleanup) {
    $cleanupArguments.WhatIf = $true
}
& (Join-Path $PSScriptRoot "cleanup-releases.ps1") @cleanupArguments

[ordered]@{
    passed = $true
    appId = $metadata.AppId
    channel = $metadata.Channel
    version = $metadata.Version
    versionQuad = $metadata.VersionQuad
    outputDirectory = $outputDirectory
    signingRequested = [bool]$SigningCertificateThumbprint
    testsSkipped = [bool]$SkipTests
    cleanupPreview = [bool]$WhatIfCleanup
    keptReleaseCount = $KeepReleases
} | ConvertTo-Json
