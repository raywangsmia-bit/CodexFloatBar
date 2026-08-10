param(
    [ValidateRange(1, 100)]
    [int]$KeepReleases = 2,
    [string]$ProtectDirectory = "",
    [switch]$IncludeValidationCaches,
    [switch]$WhatIf
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$releaseRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot "release"))
$releasePattern = '^next-beta-\d+\.\d+\.\d+(?:\.\d+)?' +
    '(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?(?:\+[0-9A-Za-z][0-9A-Za-z.-]*)?$'

$protectedPath = ""
if ($ProtectDirectory) {
    $protectedPath = [IO.Path]::GetFullPath($ProtectDirectory)
    if (-not (Split-Path -Parent $protectedPath).Equals(
        $releaseRoot,
        [StringComparison]::OrdinalIgnoreCase
    ) -or
        (Split-Path -Leaf $protectedPath) -notmatch $releasePattern) {
        throw "protected release must be one next-beta-* directory below $releaseRoot"
    }
}

$candidates = @()
if (Test-Path -LiteralPath $releaseRoot -PathType Container) {
    $candidates = @(
        Get-ChildItem -LiteralPath $releaseRoot -Directory -Force |
            Where-Object { $_.Name -match $releasePattern } |
            Sort-Object `
                @{ Expression = { $_.LastWriteTimeUtc }; Descending = $true }, `
                @{ Expression = { $_.Name }; Descending = $true }
    )
}
$keepPaths = [Collections.Generic.HashSet[string]]::new(
    [StringComparer]::OrdinalIgnoreCase
)
foreach ($candidate in $candidates | Select-Object -First $KeepReleases) {
    [void]$keepPaths.Add($candidate.FullName)
}
if ($protectedPath) {
    [void]$keepPaths.Add($protectedPath)
}

$removed = @()
$planned = @()
foreach ($candidate in $candidates) {
    if ($keepPaths.Contains($candidate.FullName)) {
        continue
    }
    if (-not (Split-Path -Parent $candidate.FullName).Equals(
        $releaseRoot,
        [StringComparison]::OrdinalIgnoreCase
    ) -or
        $candidate.Name -notmatch $releasePattern) {
        throw "refusing unsafe release cleanup target: $($candidate.FullName)"
    }
    if ($WhatIf) {
        $planned += $candidate.FullName
        continue
    }
    Remove-Item -LiteralPath $candidate.FullName -Recurse -Force
    $removed += $candidate.FullName
}

$cacheRemoved = @()
$cachePlanned = @()
if ($IncludeValidationCaches) {
    $cacheRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot ".cache"))
    $cachePrefix = $cacheRoot.TrimEnd([IO.Path]::DirectorySeparatorChar) + `
        [IO.Path]::DirectorySeparatorChar
    $cacheTargets = @()
    foreach ($name in @("release-static-validation", "release-resources")) {
        $path = Join-Path $cacheRoot $name
        if (Test-Path -LiteralPath $path) {
            $cacheTargets += Get-Item -LiteralPath $path -Force
        }
    }
    if (Test-Path -LiteralPath $cacheRoot -PathType Container) {
        $cacheTargets += Get-ChildItem `
            -LiteralPath $cacheRoot `
            -Directory `
            -Filter "release-*-zip-verify" `
            -Force
    }
    foreach ($target in $cacheTargets | Sort-Object FullName -Unique) {
        $fullPath = [IO.Path]::GetFullPath($target.FullName)
        if (-not $fullPath.StartsWith(
            $cachePrefix,
            [StringComparison]::OrdinalIgnoreCase
        ) -or
            $target.Name -notmatch '^release-(?:static-validation|resources|.+-zip-verify)$') {
            throw "refusing unsafe release cache cleanup target: $fullPath"
        }
        if ($WhatIf) {
            $cachePlanned += $fullPath
            continue
        }
        Remove-Item -LiteralPath $fullPath -Recurse -Force
        $cacheRemoved += $fullPath
    }
}

[ordered]@{
    passed = $true
    releaseRoot = $releaseRoot
    keepReleases = $KeepReleases
    protected = if ($protectedPath) { $protectedPath } else { $null }
    whatIf = [bool]$WhatIf
    kept = @($keepPaths | Sort-Object)
    planned = $planned
    removed = $removed
    cachePlanned = $cachePlanned
    cacheRemoved = $cacheRemoved
} | ConvertTo-Json -Depth 4
