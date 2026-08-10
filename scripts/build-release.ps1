param(
    [string]$OutputDirectory = "",
    [string]$MetadataPath = "",
    [string]$Version = "",
    [string]$VersionQuad = "",
    [string]$SigningCertificateThumbprint = "",
    [string]$TimestampUrl = "http://timestamp.digicert.com",
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot "release-common.ps1")

if (-not $MetadataPath) {
    $MetadataPath = Join-Path $projectRoot "resources\release-metadata.psd1"
}
$metadata = Get-NativeReleaseMetadata `
    -Path $MetadataPath `
    -Version $Version `
    -VersionQuad $VersionQuad
Assert-NativeRuntimeIdentity -Metadata $metadata -ProjectRoot $projectRoot

if (-not $OutputDirectory) {
    $OutputDirectory = Join-Path $projectRoot "release"
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$artifactPrefix = "{0}-{1}" -f $metadata.AppId, $metadata.Version
$portableRoot = Join-Path $OutputDirectory $metadata.PortableDirectoryName
$executablePath = Join-Path $portableRoot $metadata.ExecutableName
$zipPath = Join-Path $OutputDirectory (
    "{0}-{1}.zip" -f $artifactPrefix, $metadata.Architecture
)
$installerPath = Join-Path $OutputDirectory ("{0}-Setup.exe" -f $artifactPrefix)
$checksumsPath = Join-Path $OutputDirectory "SHA256SUMS.txt"
$releasePackageMetadataPath = Join-Path $portableRoot "release.json"
$licensePath = Join-Path $projectRoot "LICENSE"
$thirdPartyNoticesPath = Join-Path $projectRoot "THIRD_PARTY_NOTICES.txt"
$resourcePath = Join-Path $projectRoot "resource_windows_amd64.syso"
$generatedResources = Join-Path $projectRoot ".cache\release-resources"
$resourcesDirectory = Join-Path $projectRoot "resources"

New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
Remove-Item `
    -LiteralPath $portableRoot, $zipPath, $installerPath, $checksumsPath `
    -Recurse `
    -Force `
    -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Path (Join-Path $portableRoot "ui\dist") -Force | Out-Null
New-Item -ItemType Directory -Path $generatedResources -Force | Out-Null
foreach ($requiredNotice in @($licensePath, $thirdPartyNoticesPath)) {
    if (-not (Test-Path -LiteralPath $requiredNotice -PathType Leaf)) {
        throw "Required distribution notice was not found: $requiredNotice"
    }
    Copy-Item -LiteralPath $requiredNotice -Destination $portableRoot -Force
}

$windresPath = Get-NativeWindresPath
$resourceResult = Write-NativeReleaseResources `
    -Metadata $metadata `
    -ResourcesDirectory $resourcesDirectory `
    -GeneratedDirectory $generatedResources `
    -OutputResourcePath $resourcePath `
    -WindresPath $windresPath

$cacheRoot = Join-Path $projectRoot ".cache\go-build"
$moduleCacheRoot = Join-Path $projectRoot ".cache\go-mod"
New-Item -ItemType Directory -Path $cacheRoot -Force | Out-Null
New-Item -ItemType Directory -Path $moduleCacheRoot -Force | Out-Null
$env:GOCACHE = $cacheRoot
$env:GOMODCACHE = $moduleCacheRoot
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
Push-Location $projectRoot
try {
    if (-not $SkipTests) {
        go test ./...
        if ($LASTEXITCODE -ne 0) {
            throw "go test failed with exit code $LASTEXITCODE"
        }
    }
    go build -trimpath -ldflags "-s -w -H=windowsgui" -o $executablePath .
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed with exit code $LASTEXITCODE"
    }
} finally {
    Pop-Location
}

$sourceBundleRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot "ui\dist"))
$sourceBundlePrefix = $sourceBundleRoot.TrimEnd([IO.Path]::DirectorySeparatorChar) + `
    [IO.Path]::DirectorySeparatorChar
$portableBundleRoot = Join-Path $portableRoot "ui\dist"
$sourceManifestPath = Join-Path $sourceBundleRoot "manifest.json"
$portableManifestPath = Join-Path $portableBundleRoot "manifest.json"
if (-not (Test-Path -LiteralPath $sourceManifestPath -PathType Leaf)) {
    throw "UI manifest was not found: $sourceManifestPath"
}
$manifest = Get-Content -LiteralPath $sourceManifestPath -Raw | ConvertFrom-Json
$variantFiles = @(
    $manifest.surfaces |
        ForEach-Object { $_.variants } |
        ForEach-Object { $_.file } |
        Sort-Object -Unique
)
if ($variantFiles.Count -eq 0) {
    throw "UI manifest does not reference any variant files"
}
Copy-Item -LiteralPath $sourceManifestPath -Destination $portableManifestPath -Force
foreach ($variantFile in $variantFiles) {
    $relativePath = [string]$variantFile
    $hasParentTraversal = @($relativePath -split "[\\/]" | Where-Object { $_ -eq ".." }).Count -gt 0
    if ([IO.Path]::IsPathRooted($relativePath) -or $hasParentTraversal) {
        throw "UI manifest contains an unsafe variant path: $relativePath"
    }

    $platformRelativePath = $relativePath.Replace("/", [IO.Path]::DirectorySeparatorChar)
    $sourcePath = [IO.Path]::GetFullPath((Join-Path $sourceBundleRoot $platformRelativePath))
    if (-not $sourcePath.StartsWith(
        $sourceBundlePrefix,
        [StringComparison]::OrdinalIgnoreCase
    )) {
        throw "UI variant resolves outside the bundle root: $relativePath"
    }
    if (-not (Test-Path -LiteralPath $sourcePath -PathType Leaf)) {
        throw "UI manifest references a missing variant: $relativePath"
    }

    $destinationPath = Join-Path $portableBundleRoot $platformRelativePath
    New-Item -ItemType Directory -Path (Split-Path -Parent $destinationPath) -Force | Out-Null
    Copy-Item -LiteralPath $sourcePath -Destination $destinationPath -Force
}

$releasePackageMetadata = [ordered]@{
    schemaVersion = $metadata.SchemaVersion
    channel = $metadata.Channel
    version = $metadata.Version
    versionQuad = $metadata.VersionQuad
    appId = $metadata.AppId
    productName = $metadata.ProductName
    publisher = $metadata.Publisher
    architecture = $metadata.Architecture
    executable = $metadata.ExecutableName
}
[IO.File]::WriteAllText(
    $releasePackageMetadataPath,
    ($releasePackageMetadata | ConvertTo-Json) + [Environment]::NewLine,
    [Text.UTF8Encoding]::new($false)
)

$signToolPath = $null
$signatures = @()
if ($SigningCertificateThumbprint) {
    $signToolPath = Get-NativeSignToolPath
    if (-not $signToolPath) {
        throw "signtool.exe is required for a signed release build"
    }
    $signatures += Invoke-NativeAuthenticodeSign `
        -FilePath $executablePath `
        -CertificateThumbprint $SigningCertificateThumbprint `
        -TimestampUrl $TimestampUrl `
        -SignToolPath $signToolPath
}

Compress-Archive `
    -LiteralPath $portableRoot `
    -DestinationPath $zipPath `
    -CompressionLevel Optimal

$artifacts = @($executablePath, $zipPath)
$makensisPath = Get-NativeMakensisPath -ProjectRoot $projectRoot
if ($makensisPath) {
    $makensisVersion = (& $makensisPath /VERSION | Select-Object -First 1).Trim()
    $nsisArguments = New-NativeNsisArguments `
        -Metadata $metadata `
        -SourceDirectory $portableRoot `
        -OutputFile $installerPath `
        -InstallerScript (Join-Path $projectRoot "installer\CodexFloatingBar.Native.nsi")
    & $makensisPath @nsisArguments
    if ($LASTEXITCODE -ne 0) {
        throw "makensis failed with exit code $LASTEXITCODE"
    }
    if ($SigningCertificateThumbprint) {
        $signatures += Invoke-NativeAuthenticodeSign `
            -FilePath $installerPath `
            -CertificateThumbprint $SigningCertificateThumbprint `
            -TimestampUrl $TimestampUrl `
            -SignToolPath $signToolPath
    }
    $artifacts += $installerPath
} else {
    $makensisVersion = $null
    Write-Warning "makensis.exe was not found; the portable package was built but NSIS was skipped"
}

$checksums = @(Write-NativeSha256Sums `
    -Artifacts $artifacts `
    -OutputPath $checksumsPath)

[ordered]@{
    appId = $metadata.AppId
    channel = $metadata.Channel
    version = $metadata.Version
    versionQuad = $metadata.VersionQuad
    executable = $executablePath
    portableZip = $zipPath
    installerBuilt = [bool]$makensisPath
    installer = if ($makensisPath) { $installerPath } else { $null }
    windres = $windresPath
    versionResource = $resourceResult.compiledResource
    makensis = $makensisPath
    makensisVersion = $makensisVersion
    signingRequested = [bool]$SigningCertificateThumbprint
    signTool = $signToolPath
    signatures = $signatures
    checksums = $checksumsPath
} | ConvertTo-Json
