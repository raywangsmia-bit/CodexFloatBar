param(
    [string]$OutputDirectory = "",
    [string]$MetadataPath = ""
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot "release-common.ps1")

if (-not $MetadataPath) {
    $MetadataPath = Join-Path $projectRoot "resources\release-metadata.psd1"
}
$metadata = Get-NativeReleaseMetadata -Path $MetadataPath
if (-not $OutputDirectory) {
    $OutputDirectory = Join-Path $projectRoot "release\next-beta-$($metadata.Version)"
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$releaseRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot "release"))
$releasePrefix = $releaseRoot.TrimEnd([IO.Path]::DirectorySeparatorChar) + `
    [IO.Path]::DirectorySeparatorChar
if (-not $OutputDirectory.StartsWith(
    $releasePrefix,
    [StringComparison]::OrdinalIgnoreCase
)) {
    throw "release verification directory escaped $releaseRoot"
}

$portableRoot = Join-Path $OutputDirectory $metadata.PortableDirectoryName
$executablePath = Join-Path $portableRoot $metadata.ExecutableName
$zipPath = Join-Path $OutputDirectory (
    "$($metadata.AppId)-$($metadata.Version)-$($metadata.Architecture).zip"
)
$installerPath = Join-Path $OutputDirectory (
    "$($metadata.AppId)-$($metadata.Version)-Setup.exe"
)
$checksumsPath = Join-Path $OutputDirectory "SHA256SUMS.txt"
$packageMetadataPath = Join-Path $portableRoot "release.json"
$manifestPath = Join-Path $portableRoot "ui\dist\manifest.json"
foreach ($path in @(
    $executablePath,
    $zipPath,
    $installerPath,
    $checksumsPath,
    $packageMetadataPath,
    $manifestPath
)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "release artifact was not found: $path"
    }
}

$expectedChecksums = @{}
foreach ($line in Get-Content -LiteralPath $checksumsPath) {
    if ($line -notmatch '^([0-9a-f]{64})  ([^\\/]+)$') {
        throw "invalid SHA256SUMS line: $line"
    }
    $expectedChecksums[$matches[2]] = $matches[1]
}
$artifactPaths = [ordered]@{
    $metadata.ExecutableName = $executablePath
    (Split-Path -Leaf $zipPath) = $zipPath
    (Split-Path -Leaf $installerPath) = $installerPath
}
if ($expectedChecksums.Count -ne $artifactPaths.Count) {
    throw "SHA256SUMS does not contain exactly $($artifactPaths.Count) artifacts"
}
foreach ($entry in $artifactPaths.GetEnumerator()) {
    if (-not $expectedChecksums.ContainsKey($entry.Key)) {
        throw "SHA256SUMS is missing $($entry.Key)"
    }
    $actual = (Get-FileHash -LiteralPath $entry.Value -Algorithm SHA256).
        Hash.ToLowerInvariant()
    if ($actual -cne $expectedChecksums[$entry.Key]) {
        throw "SHA-256 mismatch: $($entry.Key)"
    }
}

$packageMetadata = Get-Content -LiteralPath $packageMetadataPath -Raw |
    ConvertFrom-Json
foreach ($contract in @{
    version = $metadata.Version
    versionQuad = $metadata.VersionQuad
    channel = $metadata.Channel
    appId = $metadata.AppId
    architecture = $metadata.Architecture
    executable = $metadata.ExecutableName
}.GetEnumerator()) {
    if ([string]$packageMetadata.($contract.Key) -cne [string]$contract.Value) {
        throw "release.json field $($contract.Key) does not match release metadata"
    }
}

$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
if ([int]$manifest.schema -ne 2 -or @($manifest.surfaces).Count -eq 0) {
    throw "portable UI manifest is invalid"
}
$bundleRoot = Split-Path -Parent $manifestPath
$referencedPNGs = @(
    $manifest.surfaces |
        ForEach-Object { $_.variants } |
        ForEach-Object { [string]$_.file } |
        Sort-Object -Unique
)
$packagedPNGs = @(
    Get-ChildItem -LiteralPath $bundleRoot -Recurse -File -Filter "*.png" |
        ForEach-Object {
            [IO.Path]::GetRelativePath($bundleRoot, $_.FullName).Replace("\", "/")
        } |
        Sort-Object -Unique
)
if (Compare-Object $referencedPNGs $packagedPNGs) {
    throw "portable UI PNG files differ from manifest references"
}

$safeVersion = $metadata.Version -replace '[^A-Za-z0-9._-]', '_'
$verificationRoot = [IO.Path]::GetFullPath((
    Join-Path $projectRoot ".cache\release-$safeVersion-zip-verify"
))
$cacheRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot ".cache"))
$cachePrefix = $cacheRoot.TrimEnd([IO.Path]::DirectorySeparatorChar) + `
    [IO.Path]::DirectorySeparatorChar
if (-not $verificationRoot.StartsWith(
    $cachePrefix,
    [StringComparison]::OrdinalIgnoreCase
)) {
    throw "ZIP verification directory escaped $cacheRoot"
}
if (Test-Path -LiteralPath $verificationRoot) {
    Remove-Item -LiteralPath $verificationRoot -Recurse -Force
}
try {
    Expand-Archive -LiteralPath $zipPath -DestinationPath $verificationRoot
    $extractedRoot = Join-Path $verificationRoot $metadata.PortableDirectoryName
    $sourceFiles = @(
        Get-ChildItem -LiteralPath $portableRoot -Recurse -File |
            ForEach-Object {
                [IO.Path]::GetRelativePath($portableRoot, $_.FullName).
                    Replace("\", "/")
            } |
            Sort-Object
    )
    $extractedFiles = @(
        Get-ChildItem -LiteralPath $extractedRoot -Recurse -File |
            ForEach-Object {
                [IO.Path]::GetRelativePath($extractedRoot, $_.FullName).
                    Replace("\", "/")
            } |
            Sort-Object
    )
    if (Compare-Object $sourceFiles $extractedFiles) {
        throw "ZIP file list differs from the portable directory"
    }
    foreach ($relativePath in $sourceFiles) {
        $platformPath = $relativePath.Replace(
            "/",
            [IO.Path]::DirectorySeparatorChar
        )
        $sourceHash = (Get-FileHash `
            -LiteralPath (Join-Path $portableRoot $platformPath) `
            -Algorithm SHA256).Hash
        $extractedHash = (Get-FileHash `
            -LiteralPath (Join-Path $extractedRoot $platformPath) `
            -Algorithm SHA256).Hash
        if ($sourceHash -cne $extractedHash) {
            throw "ZIP content differs: $relativePath"
        }
    }
} finally {
    if (Test-Path -LiteralPath $verificationRoot) {
        Remove-Item -LiteralPath $verificationRoot -Recurse -Force
    }
}

$executableSignature = (Get-AuthenticodeSignature -LiteralPath $executablePath).Status
$installerSignature = (Get-AuthenticodeSignature -LiteralPath $installerPath).Status
[ordered]@{
    passed = $true
    version = $metadata.Version
    versionQuad = $metadata.VersionQuad
    outputDirectory = $OutputDirectory
    artifacts = $artifactPaths.Count
    portableFiles = $sourceFiles.Count
    surfaces = @($manifest.surfaces).Count
    variants = @($referencedPNGs).Count
    assetGenerations = @(
        $referencedPNGs |
            ForEach-Object { ($_ -split '/')[1] } |
            Sort-Object -Unique
    )
    executableSignature = [string]$executableSignature
    installerSignature = [string]$installerSignature
    checksums = $checksumsPath
} | ConvertTo-Json
