Set-StrictMode -Version Latest

function Get-NativeReleaseMetadata {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,
        [string]$Version = "",
        [string]$VersionQuad = ""
    )

    $resolvedPath = [IO.Path]::GetFullPath($Path)
    if (-not (Test-Path -LiteralPath $resolvedPath -PathType Leaf)) {
        throw "release metadata was not found: $resolvedPath"
    }
    if ([bool]$Version -xor [bool]$VersionQuad) {
        throw "Version and VersionQuad overrides must be supplied together"
    }

    $source = Import-PowerShellDataFile -LiteralPath $resolvedPath
    $metadata = @{}
    foreach ($key in $source.Keys) {
        $metadata[$key] = $source[$key]
    }
    if ($Version) {
        $metadata.Version = $Version
    }
    if ($VersionQuad) {
        $metadata.VersionQuad = $VersionQuad
    }

    $required = @(
        "SchemaVersion",
        "Channel",
        "Version",
        "VersionQuad",
        "AppId",
        "ProductName",
        "FileDescription",
        "Publisher",
        "Website",
        "ExecutableName",
        "PortableDirectoryName",
        "InstallDirectoryName",
        "StartMenuFolder",
        "StartupValueName",
        "UninstallKey",
        "WindowClass",
        "WindowTitle",
        "Architecture"
    )
    foreach ($name in $required) {
        if (-not $metadata.ContainsKey($name) -or
            [string]::IsNullOrWhiteSpace([string]$metadata[$name])) {
            throw "release metadata field is missing: $name"
        }
    }
    if ([int]$metadata.SchemaVersion -ne 1) {
        throw "unsupported release metadata schema: $($metadata.SchemaVersion)"
    }

    $safeIdentity = "^[A-Za-z0-9][A-Za-z0-9._-]*$"
    foreach ($name in @("AppId", "StartupValueName", "UninstallKey")) {
        if ([string]$metadata[$name] -notmatch $safeIdentity) {
            throw "release metadata field $name is not a safe identity"
        }
    }
    if ([string]$metadata.Version -notmatch `
        '^\d+\.\d+\.\d+(?:\.\d+)?(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?(?:\+[0-9A-Za-z][0-9A-Za-z.-]*)?$') {
        throw "release Version is not a supported semantic version: $($metadata.Version)"
    }

    $versionParts = @([string]$metadata.VersionQuad -split '\.')
    if ($versionParts.Count -ne 4) {
        throw "release VersionQuad must contain four numeric components"
    }
    foreach ($part in $versionParts) {
        [uint32]$number = 0
        if (-not [uint32]::TryParse($part, [ref]$number) -or $number -gt 65535) {
            throw "release VersionQuad component is outside 0..65535: $part"
        }
    }
    $semanticCore = ([string]$metadata.Version) -replace '[-+].*$', ''
    $semanticParts = @($semanticCore -split '\.')
    for ($index = 0; $index -lt $semanticParts.Count; $index++) {
        if ([uint32]$semanticParts[$index] -ne [uint32]$versionParts[$index]) {
            throw "release Version and VersionQuad numeric components do not match"
        }
    }

    foreach ($name in @(
        "ExecutableName",
        "PortableDirectoryName",
        "InstallDirectoryName",
        "StartMenuFolder"
    )) {
        $value = [string]$metadata[$name]
        if ($value -eq "." -or
            $value -eq ".." -or
            [IO.Path]::IsPathRooted($value) -or
            [IO.Path]::GetFileName($value) -ne $value -or
            $value.IndexOfAny([IO.Path]::GetInvalidFileNameChars()) -ge 0 -or
            $value.IndexOfAny([char[]]@('$', '!')) -ge 0) {
            throw "release metadata field $name must be one safe path segment"
        }
    }
    if (-not ([string]$metadata.ExecutableName).EndsWith(
        ".exe",
        [StringComparison]::OrdinalIgnoreCase
    )) {
        throw "release ExecutableName must end with .exe"
    }
    $identityContracts = @{
        ExecutableName        = "$($metadata.AppId).exe"
        PortableDirectoryName = $metadata.AppId
        InstallDirectoryName  = $metadata.AppId
        StartMenuFolder       = $metadata.ProductName
        StartupValueName      = $metadata.AppId
        UninstallKey          = $metadata.AppId
        WindowClass           = "$($metadata.AppId).Window"
        WindowTitle           = $metadata.ProductName
    }
    foreach ($name in $identityContracts.Keys) {
        if ([string]$metadata[$name] -cne [string]$identityContracts[$name]) {
            throw "release metadata field $name is inconsistent with AppId or ProductName"
        }
    }
    if ([string]$metadata.Architecture -cne "win-x64") {
        throw "this release pipeline supports only the win-x64 architecture"
    }

    foreach ($name in @(
        "Channel",
        "ProductName",
        "FileDescription",
        "Publisher",
        "WindowClass",
        "WindowTitle",
        "Architecture"
    )) {
        $value = [string]$metadata[$name]
        if ($value.IndexOfAny([char[]]@('"', "`r", "`n", '$', '!')) -ge 0) {
            throw "release metadata field $name contains unsafe resource or NSIS characters"
        }
    }

    $website = $null
    if (-not [Uri]::TryCreate(
        [string]$metadata.Website,
        [UriKind]::Absolute,
        [ref]$website
    ) -or
        $website.Scheme -ne "https" -or
        ([string]$metadata.Website).IndexOfAny(
            [char[]]@('"', "`r", "`n", '$', '!')
        ) -ge 0) {
        throw "release Website must be an absolute HTTPS URL"
    }

    return [pscustomobject]$metadata
}

function Assert-NativeRuntimeIdentity {
    param(
        [Parameter(Mandatory = $true)]
        [psobject]$Metadata,
        [Parameter(Mandatory = $true)]
        [string]$ProjectRoot
    )

    $identityPath = Join-Path $ProjectRoot "internal\appidentity\identity.go"
    if (-not (Test-Path -LiteralPath $identityPath -PathType Leaf)) {
        throw "native runtime identity source was not found: $identityPath"
    }
    $source = [IO.File]::ReadAllText($identityPath)
    $expected = @{
        AppID = $Metadata.AppId
        ProductName = $Metadata.ProductName
    }
    foreach ($entry in $expected.GetEnumerator()) {
        $pattern = '(?m)^\s*{0}\s*=\s*"{1}"\s*$' -f `
            [regex]::Escape($entry.Key), `
            [regex]::Escape([string]$entry.Value)
        if ($source -notmatch $pattern) {
            throw "release metadata $($entry.Key) does not match native runtime identity"
        }
    }
}

function Get-NativeWindresPath {
    $command = Get-Command windres.exe -ErrorAction SilentlyContinue
    if (-not $command) {
        throw "windres.exe is required to generate the manifest and VERSIONINFO resource"
    }
    return $command.Source
}

function Get-NativeMakensisPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ProjectRoot
    )

    $toolsRoot = Join-Path $ProjectRoot ".tools"
    $preferred = Join-Path $toolsRoot "nsis-3.12\makensis.exe"
    if (Test-Path -LiteralPath $preferred -PathType Leaf) {
        return $preferred
    }
    if (Test-Path -LiteralPath $toolsRoot -PathType Container) {
        $roots = Get-ChildItem `
            -LiteralPath $toolsRoot `
            -Directory `
            -Filter "nsis-*" | Sort-Object Name -Descending
        foreach ($root in $roots) {
            $candidate = Join-Path $root.FullName "makensis.exe"
            if (Test-Path -LiteralPath $candidate -PathType Leaf) {
                return $candidate
            }
        }
    }

    $command = Get-Command makensis.exe -ErrorAction SilentlyContinue
    if ($command) {
        return $command.Source
    }
    return $null
}

function Get-NativeSignToolPath {
    $command = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if ($command) {
        return $command.Source
    }

    $kitsRoot = "C:\Program Files (x86)\Windows Kits\10\bin"
    if (Test-Path -LiteralPath $kitsRoot -PathType Container) {
        $candidates = Get-ChildItem `
            -LiteralPath $kitsRoot `
            -Directory | Sort-Object Name -Descending
        foreach ($candidate in $candidates) {
            $signTool = Join-Path $candidate.FullName "x64\signtool.exe"
            if (Test-Path -LiteralPath $signTool -PathType Leaf) {
                return $signTool
            }
        }
    }
    return $null
}

function Invoke-NativeAuthenticodeSign {
    param(
        [Parameter(Mandatory = $true)]
        [string]$FilePath,
        [Parameter(Mandatory = $true)]
        [string]$CertificateThumbprint,
        [Parameter(Mandatory = $true)]
        [string]$TimestampUrl,
        [Parameter(Mandatory = $true)]
        [string]$SignToolPath
    )

    $resolvedFile = [IO.Path]::GetFullPath($FilePath)
    if (-not (Test-Path -LiteralPath $resolvedFile -PathType Leaf)) {
        throw "file to sign was not found: $resolvedFile"
    }
    $normalizedThumbprint = $CertificateThumbprint.Replace(" ", "")
    if ($normalizedThumbprint -notmatch '^[0-9a-fA-F]{40}$') {
        throw "code-signing certificate thumbprint must contain 40 hexadecimal characters"
    }
    $timestampUri = $null
    if (-not [Uri]::TryCreate($TimestampUrl, [UriKind]::Absolute, [ref]$timestampUri) -or
        $timestampUri.Scheme -notin @("http", "https")) {
        throw "timestamp URL must be an absolute HTTP or HTTPS URL"
    }

    & $SignToolPath sign `
        /sha1 $normalizedThumbprint `
        /fd SHA256 `
        /tr $timestampUri.AbsoluteUri `
        /td SHA256 `
        $resolvedFile
    if ($LASTEXITCODE -ne 0) {
        throw "signtool failed to sign $resolvedFile with exit code $LASTEXITCODE"
    }

    & $SignToolPath verify /pa /all $resolvedFile | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "signtool verification failed for $resolvedFile with exit code $LASTEXITCODE"
    }
    $signature = Get-AuthenticodeSignature -LiteralPath $resolvedFile
    if ($signature.Status -ne [Management.Automation.SignatureStatus]::Valid) {
        throw "Authenticode verification returned $($signature.Status) for $resolvedFile"
    }
    if ($null -eq $signature.TimeStamperCertificate) {
        throw "signed file does not contain a timestamp countersignature: $resolvedFile"
    }

    return [ordered]@{
        file = $resolvedFile
        status = $signature.Status.ToString()
        signer = $signature.SignerCertificate.Subject
        timestampSigner = $signature.TimeStamperCertificate.Subject
    }
}

function Expand-NativeReleaseTemplate {
    param(
        [Parameter(Mandatory = $true)]
        [string]$TemplatePath,
        [Parameter(Mandatory = $true)]
        [string]$OutputPath,
        [Parameter(Mandatory = $true)]
        [hashtable]$Values
    )

    $content = [IO.File]::ReadAllText([IO.Path]::GetFullPath($TemplatePath))
    foreach ($entry in $Values.GetEnumerator()) {
        $token = "@@{0}@@" -f $entry.Key
        if (-not $content.Contains($token)) {
            continue
        }
        $content = $content.Replace($token, [string]$entry.Value)
    }
    $unresolved = [regex]::Match($content, '@@[A-Z0-9_]+@@')
    if ($unresolved.Success) {
        throw "release template has an unresolved token: $($unresolved.Value)"
    }

    $directory = Split-Path -Parent $OutputPath
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    [IO.File]::WriteAllText(
        [IO.Path]::GetFullPath($OutputPath),
        $content,
        [Text.UTF8Encoding]::new($false)
    )
}

function Write-NativeReleaseResources {
    param(
        [Parameter(Mandatory = $true)]
        [psobject]$Metadata,
        [Parameter(Mandatory = $true)]
        [string]$ResourcesDirectory,
        [Parameter(Mandatory = $true)]
        [string]$GeneratedDirectory,
        [Parameter(Mandatory = $true)]
        [string]$OutputResourcePath,
        [Parameter(Mandatory = $true)]
        [string]$WindresPath
    )

    $values = @{
        APP_ID           = $Metadata.AppId
        CHANNEL          = $Metadata.Channel
        DISPLAY_VERSION  = $Metadata.Version
        EXECUTABLE_NAME  = $Metadata.ExecutableName
        FILE_DESCRIPTION = $Metadata.FileDescription
        ICON_PATH        = ([IO.Path]::GetFullPath(
            (Join-Path $ResourcesDirectory "app-icon.ico")
        )).Replace('\', '/')
        PRODUCT_NAME     = $Metadata.ProductName
        PUBLISHER        = $Metadata.Publisher
        VERSION_COMMA    = ([string]$Metadata.VersionQuad).Replace('.', ',')
        VERSION_QUAD     = $Metadata.VersionQuad
    }
    $manifestPath = Join-Path $GeneratedDirectory "app.manifest"
    $resourceScriptPath = Join-Path $GeneratedDirectory "app.rc"
    Expand-NativeReleaseTemplate `
        -TemplatePath (Join-Path $ResourcesDirectory "app.manifest.in") `
        -OutputPath $manifestPath `
        -Values $values
    Expand-NativeReleaseTemplate `
        -TemplatePath (Join-Path $ResourcesDirectory "app.rc.in") `
        -OutputPath $resourceScriptPath `
        -Values $values

    Push-Location $GeneratedDirectory
    try {
        & $WindresPath `
            --input "app.rc" `
            --output ([IO.Path]::GetFullPath($OutputResourcePath)) `
            --output-format coff `
            --target pe-x86-64
        if ($LASTEXITCODE -ne 0) {
            throw "windres failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }

    return [ordered]@{
        manifest = $manifestPath
        resourceScript = $resourceScriptPath
        compiledResource = [IO.Path]::GetFullPath($OutputResourcePath)
    }
}

function New-NativeNsisArguments {
    param(
        [Parameter(Mandatory = $true)]
        [psobject]$Metadata,
        [Parameter(Mandatory = $true)]
        [string]$SourceDirectory,
        [Parameter(Mandatory = $true)]
        [string]$OutputFile,
        [Parameter(Mandatory = $true)]
        [string]$InstallerScript
    )

    $resolvedSource = [IO.Path]::GetFullPath($SourceDirectory)
    $resolvedOutput = [IO.Path]::GetFullPath($OutputFile)
    foreach ($path in @($resolvedSource, $resolvedOutput)) {
        if ($path.IndexOfAny([char[]]@('"', "`r", "`n", '$', '!')) -ge 0) {
            throw "NSIS source and output paths cannot contain quote, dollar, or bang characters"
        }
    }

    return @(
        "/DSOURCE_DIR=$resolvedSource",
        "/DOUTPUT_FILE=$resolvedOutput",
        "/DAPP_ID=$($Metadata.AppId)",
        "/DAPP_DISPLAY_NAME=$($Metadata.ProductName)",
        "/DAPP_VERSION=$($Metadata.Version)",
        "/DAPP_CHANNEL=$($Metadata.Channel)",
        "/DAPP_PUBLISHER=$($Metadata.Publisher)",
        "/DAPP_WEBSITE=$($Metadata.Website)",
        "/DEXECUTABLE_NAME=$($Metadata.ExecutableName)",
        "/DINSTALL_DIR_NAME=$($Metadata.InstallDirectoryName)",
        "/DSTART_MENU_FOLDER=$($Metadata.StartMenuFolder)",
        "/DSTARTUP_VALUE_NAME=$($Metadata.StartupValueName)",
        "/DUNINSTALL_KEY=$($Metadata.UninstallKey)",
        "/DWINDOW_CLASS=$($Metadata.WindowClass)",
        "/DWINDOW_TITLE=$($Metadata.WindowTitle)",
        [IO.Path]::GetFullPath($InstallerScript)
    )
}

function Write-NativeSha256Sums {
    param(
        [Parameter(Mandatory = $true)]
        [string[]]$Artifacts,
        [Parameter(Mandatory = $true)]
        [string]$OutputPath
    )

    if ($Artifacts.Count -eq 0) {
        throw "at least one release artifact is required for SHA-256"
    }
    $names = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::OrdinalIgnoreCase
    )
    $lines = foreach ($artifact in $Artifacts) {
        $resolvedArtifact = [IO.Path]::GetFullPath($artifact)
        if (-not (Test-Path -LiteralPath $resolvedArtifact -PathType Leaf)) {
            throw "release artifact was not found before hashing: $resolvedArtifact"
        }
        $name = Split-Path -Leaf $resolvedArtifact
        if (-not $names.Add($name)) {
            throw "release artifacts contain a duplicate file name: $name"
        }
        $hash = Get-FileHash -LiteralPath $resolvedArtifact -Algorithm SHA256
        "{0}  {1}" -f $hash.Hash.ToLowerInvariant(), $name
    }

    [IO.File]::WriteAllLines(
        [IO.Path]::GetFullPath($OutputPath),
        [string[]]$lines,
        [Text.Encoding]::ASCII
    )
    return $lines
}
