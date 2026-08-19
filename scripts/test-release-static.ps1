param(
    [string]$MetadataPath = "",
    [string]$Version = "",
    [string]$VersionQuad = ""
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

$singleVersionOverrideRejected = $false
try {
    [void](Get-NativeReleaseMetadata `
        -Path $MetadataPath `
        -Version "9.9.9-beta.1")
} catch {
    $singleVersionOverrideRejected = $true
}
if (-not $singleVersionOverrideRejected) {
    throw "release metadata accepted a display-version-only override"
}

$cacheRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot ".cache"))
$validationRoot = [IO.Path]::GetFullPath((
    Join-Path $cacheRoot "release-static-validation"
))
$cachePrefix = $cacheRoot.TrimEnd([IO.Path]::DirectorySeparatorChar) + `
    [IO.Path]::DirectorySeparatorChar
if (-not $validationRoot.StartsWith(
    $cachePrefix,
    [StringComparison]::OrdinalIgnoreCase
)) {
    throw "static validation directory escaped the project cache"
}
if (Test-Path -LiteralPath $validationRoot) {
    Remove-Item -LiteralPath $validationRoot -Recurse -Force
}

$generatedResources = Join-Path $validationRoot "resources"
$sourceDirectory = Join-Path $validationRoot "portable"
$uiDirectory = Join-Path $sourceDirectory "ui\dist"
$installerPath = Join-Path $validationRoot "installer-static-check.exe"
New-Item -ItemType Directory -Path $generatedResources, $uiDirectory -Force | Out-Null

[IO.File]::WriteAllBytes(
    (Join-Path $sourceDirectory $metadata.ExecutableName),
    [byte[]]@(0)
)
[IO.File]::WriteAllText(
    (Join-Path $sourceDirectory "release.json"),
    (@{
        appId = $metadata.AppId
        channel = $metadata.Channel
        version = $metadata.Version
    } | ConvertTo-Json),
    [Text.UTF8Encoding]::new($false)
)
Copy-Item -LiteralPath (Join-Path $projectRoot "LICENSE") -Destination $sourceDirectory
Copy-Item `
    -LiteralPath (Join-Path $projectRoot "THIRD_PARTY_NOTICES.txt") `
    -Destination $sourceDirectory
[IO.File]::WriteAllText(
    (Join-Path $uiDirectory "manifest.json"),
    '{"schemaVersion":2,"surfaces":[]}',
    [Text.UTF8Encoding]::new($false)
)

$syntaxFiles = @(
    (Join-Path $PSScriptRoot "release-common.ps1"),
    (Join-Path $PSScriptRoot "build-release.ps1"),
    (Join-Path $PSScriptRoot "verify-release.ps1"),
    (Join-Path $PSScriptRoot "cleanup-releases.ps1"),
    (Join-Path $PSScriptRoot "publish-next.ps1"),
    (Join-Path $PSScriptRoot "export-verification-kit.ps1"),
    (Join-Path $PSScriptRoot "run-clean-verification.ps1"),
    (Join-Path $PSScriptRoot "test-installer.ps1"),
    (Join-Path $PSScriptRoot "test-wpf-rollback.ps1"),
    $PSCommandPath
)
foreach ($file in $syntaxFiles) {
    $tokens = $null
    $errors = $null
    [void][Management.Automation.Language.Parser]::ParseFile(
        $file,
        [ref]$tokens,
        [ref]$errors
    )
    if ($errors.Count -gt 0) {
        $messages = @($errors | ForEach-Object { $_.Message }) -join "; "
        throw "PowerShell syntax validation failed for $file`: $messages"
    }
}

$installerSource = [IO.File]::ReadAllText(
    (Join-Path $projectRoot "installer\CodexFloatingBar.Native.nsi")
)
if (-not $installerSource.Contains('"QuietUninstallString"') -or
    -not $installerSource.Contains('--quiet-uninstall')) {
    throw "installer does not register the synchronous running-instance guard"
}
if (-not $installerSource.Contains('Icon "${APP_ICON}"') -or
    -not $installerSource.Contains('UninstallIcon "${APP_ICON}"')) {
    throw "installer and uninstaller do not use the application icon"
}
foreach ($noticeName in @("LICENSE", "THIRD_PARTY_NOTICES.txt")) {
    if (-not $installerSource.Contains("File `"`${SOURCE_DIR}\$noticeName`"")) {
        throw "installer does not package $noticeName"
    }
}

$windresPath = Get-NativeWindresPath
$signToolPath = Get-NativeSignToolPath
if (-not $signToolPath) {
    throw "signtool.exe is required for signed-release readiness validation"
}
$invalidThumbprintRejected = $false
try {
    [void](Invoke-NativeAuthenticodeSign `
        -FilePath (Join-Path $sourceDirectory $metadata.ExecutableName) `
        -CertificateThumbprint "invalid" `
        -TimestampUrl "http://timestamp.digicert.com" `
        -SignToolPath $signToolPath)
} catch {
    $invalidThumbprintRejected = $true
}
if (-not $invalidThumbprintRejected) {
    throw "signing helper accepted an invalid certificate thumbprint"
}

$resourceResult = Write-NativeReleaseResources `
    -Metadata $metadata `
    -ResourcesDirectory (Join-Path $projectRoot "resources") `
    -GeneratedDirectory $generatedResources `
    -OutputResourcePath (Join-Path $validationRoot "resource_windows_amd64.syso") `
    -WindresPath $windresPath

$expandedRC = [IO.File]::ReadAllText($resourceResult.resourceScript)
$expandedManifest = [IO.File]::ReadAllText($resourceResult.manifest)
if ($expandedRC -notmatch '\bVERSIONINFO\b') {
    throw "expanded Windows resource does not contain VERSIONINFO"
}
if ($expandedRC -notmatch '(?im)^\s*101\s+ICON\s+"[^"]+app-icon\.ico"') {
    throw "expanded Windows resource does not contain the application icon"
}
if (-not $expandedManifest.Contains("name=`"$($metadata.AppId)`"")) {
    throw "expanded manifest does not contain the release application identity"
}
if (-not $expandedManifest.Contains("version=`"$($metadata.VersionQuad)`"")) {
    throw "expanded manifest does not contain the release numeric version"
}

$iconPath = Join-Path $projectRoot "resources\app-icon.ico"
$iconSourcePath = Join-Path $projectRoot "resources\app-icon-source.png"
$iconHash = (Get-FileHash -LiteralPath $iconPath -Algorithm SHA256).
    Hash.ToLowerInvariant()
$iconSourceHash = (Get-FileHash -LiteralPath $iconSourcePath -Algorithm SHA256).
    Hash.ToLowerInvariant()
foreach ($declarationPath in @(
    (Join-Path $projectRoot "docs\asset-provenance.md"),
    (Join-Path $projectRoot "THIRD_PARTY_NOTICES.txt")
)) {
    $declaration = [IO.File]::ReadAllText($declarationPath).ToLowerInvariant()
    foreach ($expectedHash in @($iconHash, $iconSourceHash)) {
        if (-not $declaration.Contains($expectedHash)) {
            throw "icon provenance declaration does not contain $expectedHash`: $declarationPath"
        }
    }
}

$makensisPath = Get-NativeMakensisPath -ProjectRoot $projectRoot
if (-not $makensisPath) {
    throw "makensis.exe is required for the static installer validation"
}
$nsisArguments = New-NativeNsisArguments `
    -Metadata $metadata `
    -SourceDirectory $sourceDirectory `
    -OutputFile $installerPath `
    -IconPath $iconPath `
    -InstallerScript (Join-Path $projectRoot "installer\CodexFloatingBar.Native.nsi")
& $makensisPath @nsisArguments
if ($LASTEXITCODE -ne 0) {
    throw "static NSIS compilation failed with exit code $LASTEXITCODE"
}
if (-not (Test-Path -LiteralPath $installerPath -PathType Leaf)) {
    throw "static NSIS compilation did not produce an installer"
}
$installerVersionInfo = (Get-Item -LiteralPath $installerPath).VersionInfo
if ($installerVersionInfo.FileVersion -cne $metadata.Version -or
    $installerVersionInfo.ProductVersion -cne $metadata.Version -or
    $installerVersionInfo.ProductName -cne $metadata.ProductName -or
    $installerVersionInfo.LegalCopyright -cne $metadata.Copyright) {
    throw "static NSIS VERSIONINFO does not match release metadata"
}

$checksumsPath = Join-Path $validationRoot "SHA256SUMS.txt"
$checksums = @(Write-NativeSha256Sums `
    -Artifacts @(
        (Join-Path $sourceDirectory $metadata.ExecutableName),
        $installerPath
    ) `
    -OutputPath $checksumsPath)
if ($checksums.Count -ne 2) {
    throw "static SHA-256 validation did not produce two checksum entries"
}
foreach ($artifact in @(
    (Join-Path $sourceDirectory $metadata.ExecutableName),
    $installerPath
)) {
    $expected = Get-FileHash -LiteralPath $artifact -Algorithm SHA256
    $expectedLine = "{0}  {1}" -f `
        $expected.Hash.ToLowerInvariant(), `
        (Split-Path -Leaf $artifact)
    if ($checksums -notcontains $expectedLine) {
        throw "static SHA-256 validation is missing $artifact"
    }
}

[ordered]@{
    passed = $true
    appId = $metadata.AppId
    channel = $metadata.Channel
    version = $metadata.Version
    versionQuad = $metadata.VersionQuad
    iconResourceIncluded = $true
    installerIconIncluded = $true
    installerVersionInfoVerified = $true
    iconProvenanceVerified = $true
    iconSha256 = $iconHash
    iconSourceSha256 = $iconSourceHash
    windowsResource = $resourceResult.compiledResource
    installer = $installerPath
    checksums = $checksumsPath
    windres = $windresPath
    signTool = $signToolPath
    makensis = $makensisPath
} | ConvertTo-Json
