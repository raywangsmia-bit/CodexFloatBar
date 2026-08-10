param(
    [string]$InstallerPath = "",
    [string]$PreviousInstallerPath = "",
    [string]$VerificationRoot = "",
    [string]$InstallationDirectory = "",
    [string]$MetadataPath = ""
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot "release-common.ps1")
if (-not $MetadataPath) {
    $MetadataPath = Join-Path $projectRoot "resources\release-metadata.psd1"
}
$metadata = Get-NativeReleaseMetadata -Path $MetadataPath
$runtimeDataRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot "runtime-data"))
if (-not $InstallerPath) {
    $installerName = "{0}-{1}-Setup.exe" -f $metadata.AppId, $metadata.Version
    $InstallerPath = Join-Path (Join-Path $projectRoot "release") $installerName
}
if (-not $VerificationRoot) {
    $runName = "installer-verification-{0}-{1}" -f `
        (Get-Date -Format "yyyyMMdd-HHmmss"), `
        $PID
    $VerificationRoot = Join-Path $runtimeDataRoot $runName
}

$InstallerPath = [IO.Path]::GetFullPath($InstallerPath)
if ($PreviousInstallerPath) {
    $PreviousInstallerPath = [IO.Path]::GetFullPath($PreviousInstallerPath)
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
if ($InstallationDirectory) {
    $InstallationDirectory = [IO.Path]::GetFullPath($InstallationDirectory)
    $verificationInstallPrefix = $VerificationRoot.TrimEnd(
        [IO.Path]::DirectorySeparatorChar
    ) + [IO.Path]::DirectorySeparatorChar
    $systemTemp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd(
        [IO.Path]::DirectorySeparatorChar
    )
    $installationParent = Split-Path -Parent $InstallationDirectory
    $installationLeaf = Split-Path -Leaf $InstallationDirectory
    $isVerificationChild = $InstallationDirectory.StartsWith(
        $verificationInstallPrefix,
        [StringComparison]::OrdinalIgnoreCase
    )
    $isSafeTempChild = `
        $installationParent.Equals($systemTemp, [StringComparison]::OrdinalIgnoreCase) -and `
        $installationLeaf -match '^codexfloatingbar-install-test-[0-9a-f]{32}$'
    if (-not $isVerificationChild -and -not $isSafeTempChild) {
        throw "installation directory must be inside the report or a guarded TEMP child"
    }
    if (Test-Path -LiteralPath $InstallationDirectory) {
        throw "installation directory already exists: $InstallationDirectory"
    }
}
if (Test-Path -LiteralPath $VerificationRoot) {
    throw "verification root already exists: $VerificationRoot"
}
if (-not (Test-Path -LiteralPath $InstallerPath -PathType Leaf)) {
    throw "installer was not found: $InstallerPath"
}
if ($PreviousInstallerPath -and -not (
    Test-Path -LiteralPath $PreviousInstallerPath -PathType Leaf
)) {
    throw "previous installer was not found: $PreviousInstallerPath"
}

$registryPath = "HKCU:\Software\$($metadata.AppId)"
$uninstallRegistryPath = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\$($metadata.UninstallKey)"
$runRegistryPath = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
$wpfStartupValueName = "CodexFloatingBar"
$localAppDataRoot = [IO.Path]::GetFullPath(
    [Environment]::GetFolderPath("LocalApplicationData")
)
$applicationDataPath = [IO.Path]::GetFullPath(
    (Join-Path $localAppDataRoot $metadata.AppId)
)
$localAppDataPrefix = $localAppDataRoot.TrimEnd(
    [IO.Path]::DirectorySeparatorChar
) + [IO.Path]::DirectorySeparatorChar
if (-not $applicationDataPath.StartsWith(
    $localAppDataPrefix,
    [StringComparison]::OrdinalIgnoreCase
)) {
    throw "application data path resolves outside LocalAppData"
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

$wpfStartupBefore = Get-OptionalRegistryValue `
    -Path $runRegistryPath `
    -Name $wpfStartupValueName
$startMenuPath = Join-Path `
    ([Environment]::GetFolderPath("Programs")) `
    $metadata.StartMenuFolder
if (Test-Path -LiteralPath $registryPath) {
    throw "refusing to touch an existing installation registry key: $registryPath"
}
if (Test-Path -LiteralPath $uninstallRegistryPath) {
    throw "refusing to touch an existing uninstall registry key: $uninstallRegistryPath"
}
if (Test-Path -LiteralPath $startMenuPath) {
    throw "refusing to touch an existing Start menu folder: $startMenuPath"
}
$existingStartup = Get-OptionalRegistryValue `
    -Path $runRegistryPath `
    -Name $metadata.StartupValueName
if ($null -ne $existingStartup) {
    throw "refusing to touch an existing startup value: $($metadata.StartupValueName)"
}
if (Test-Path -LiteralPath $applicationDataPath) {
    throw "refusing to touch existing Next user data: $applicationDataPath"
}

function Invoke-NativeProcess {
    param(
        [Parameter(Mandatory = $true)]
        [string]$FilePath,
        [string[]]$Arguments = @(),
        [string]$RawArguments = ""
    )

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $FilePath
    $startInfo.UseShellExecute = $false
    if ($RawArguments) {
        $startInfo.Arguments = $RawArguments
    } else {
        foreach ($argument in $Arguments) {
            $startInfo.ArgumentList.Add($argument)
        }
    }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    if (-not $process.Start()) {
        throw "failed to start $FilePath"
    }
    $process.WaitForExit()
    return $process.ExitCode
}

if (-not ("NativeInstallerSmoke" -as [type])) {
    Add-Type @'
using System;
using System.Runtime.InteropServices;

public static class NativeInstallerSmoke {
    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    public static extern IntPtr FindWindow(string className, string title);

    [DllImport("user32.dll")]
    public static extern bool IsWindow(IntPtr window);

    [DllImport("user32.dll")]
    public static extern bool IsWindowVisible(IntPtr window);

    [DllImport("user32.dll")]
    public static extern uint GetWindowThreadProcessId(IntPtr window, out uint processId);

    [DllImport("user32.dll")]
    public static extern bool PostMessage(IntPtr window, uint message, UIntPtr wParam, IntPtr lParam);
}
'@
}

function Start-NativeMainWindow {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ExecutablePath,
        [Parameter(Mandatory = $true)]
        [string]$LogPath,
        [Parameter(Mandatory = $true)]
        [string]$WindowClass,
        [Parameter(Mandatory = $true)]
        [string]$WindowTitle
    )

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $ExecutablePath
    $startInfo.UseShellExecute = $false
    $startInfo.ArgumentList.Add("--log-file")
    $startInfo.ArgumentList.Add($LogPath)
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    if (-not $process.Start()) {
        throw "failed to start installed executable"
    }

    try {
        $deadline = [DateTime]::UtcNow.AddSeconds(10)
        $window = [IntPtr]::Zero
        $valid = $false
        $visible = $false
        [uint32]$windowProcessID = 0
        do {
            Start-Sleep -Milliseconds 50
            $window = [NativeInstallerSmoke]::FindWindow(
                $WindowClass,
                $WindowTitle
            )
            if ($window -ne [IntPtr]::Zero) {
                $valid = [NativeInstallerSmoke]::IsWindow($window)
                $visible = [NativeInstallerSmoke]::IsWindowVisible($window)
                [void][NativeInstallerSmoke]::GetWindowThreadProcessId(
                    $window,
                    [ref]$windowProcessID
                )
            }
        } while (
            ((-not $valid) -or (-not $visible) -or $windowProcessID -ne $process.Id) -and
            [DateTime]::UtcNow -lt $deadline
        )

        if (-not $valid -or -not $visible -or $windowProcessID -ne $process.Id) {
            throw "installed main HWND did not become valid and visible"
        }
        return [pscustomobject]@{
            Process = $process
            Window = $window
            HandleText = "0x{0:x}" -f $window.ToInt64()
            Valid = $valid
            Visible = $visible
            ProcessMatched = $windowProcessID -eq $process.Id
        }
    } catch {
        if (-not $process.HasExited) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        }
        throw
    }
}

function Stop-NativeMainWindow {
    param(
        [Parameter(Mandatory = $true)]
        [pscustomobject]$Session
    )

    if ($Session.Process.HasExited) {
        throw "installed executable exited before it was asked to stop"
    }
    if (-not [NativeInstallerSmoke]::PostMessage(
        $Session.Window,
        0x0002,
        [UIntPtr]::Zero,
        [IntPtr]::Zero
    )) {
        throw "failed to post WM_DESTROY to installed executable"
    }
    if (-not $Session.Process.WaitForExit(5000)) {
        throw "installed executable did not exit after WM_DESTROY"
    }
    return $Session.Process.ExitCode
}

New-Item -ItemType Directory -Path $VerificationRoot -Force | Out-Null
$installRoot = if ($InstallationDirectory) {
    $InstallationDirectory
} else {
    Join-Path $VerificationRoot "install"
}
$launchLogPath = Join-Path $VerificationRoot "installed-launch.log"
$verificationPath = Join-Path $VerificationRoot "verification.json"
$uninstallerPath = Join-Path $installRoot "Uninstall.exe"
$installedExecutable = Join-Path $installRoot $metadata.ExecutableName
$initialInstallerPath = if ($PreviousInstallerPath) {
    $PreviousInstallerPath
} else {
    $InstallerPath
}
$uninstallAttempted = $false
$startupValueCreated = $false
$activeSession = $null
$result = [ordered]@{
    passed = $false
    installer = $InstallerPath
    previousInstaller = if ($PreviousInstallerPath) { $PreviousInstallerPath } else { $null }
    installDirectory = $installRoot
    initialInstallerExitCode = $null
    initialVersion = $null
    installerExitCode = $null
    upgradedVersion = $null
    staleUiRemovedOnUpgrade = $false
    startupPreservedOnUpgrade = $false
    occupiedInstallerExitCode = $null
    occupiedUninstallerExitCode = $null
    runningApplicationSurvivedOccupiedChecks = $false
    launchExitCode = $null
    mainWindowHandle = $null
    mainWindowValid = $false
    mainWindowVisible = $false
    mainWindowProcessMatched = $false
    uninstallerExitCode = $null
    installedExecutableFound = $false
    installedManifestFound = $false
    releaseMetadataFound = $false
    appsAndFeaturesFound = $false
    appsAndFeaturesValuesValid = $false
    quietUninstallStringValid = $false
    startupValueRemoved = $false
    wpfStartupPreserved = $false
    applicationDataCaptured = $false
    applicationDataIsolated = $false
    installDirectoryRemoved = $false
    registryRemoved = $false
    uninstallRegistryRemoved = $false
    startMenuRemoved = $false
    errors = @()
}

try {
    $result.initialInstallerExitCode = Invoke-NativeProcess `
        -FilePath $initialInstallerPath `
        -RawArguments "/S /D=$installRoot"
    if ($result.initialInstallerExitCode -ne 0) {
        throw "initial silent installer exited with code $($result.initialInstallerExitCode)"
    }

    $installedReleasePath = Join-Path $installRoot "release.json"
    if (-not (Test-Path -LiteralPath $installedReleasePath -PathType Leaf)) {
        throw "initial release metadata is missing"
    }
    foreach ($noticeName in @("LICENSE", "THIRD_PARTY_NOTICES.txt")) {
        if (-not (Test-Path -LiteralPath (Join-Path $installRoot $noticeName) -PathType Leaf)) {
            throw "installed distribution notice is missing: $noticeName"
        }
    }
    $initialRelease = Get-Content -LiteralPath $installedReleasePath -Raw | ConvertFrom-Json
    $result.initialVersion = [string]$initialRelease.version

    $staleUiPath = Join-Path $installRoot "ui\dist\assets\obsolete-generation\stale.png"
    New-Item -ItemType Directory -Path (Split-Path -Parent $staleUiPath) -Force | Out-Null
    [IO.File]::WriteAllBytes($staleUiPath, [byte[]](1, 2, 3, 4))

    New-ItemProperty `
        -LiteralPath $runRegistryPath `
        -Name $metadata.StartupValueName `
        -Value '"C:\obsolete\CodexFloatingBar.Next.exe"' `
        -PropertyType String `
        -Force | Out-Null
    $startupValueCreated = $true

    $result.installerExitCode = Invoke-NativeProcess `
        -FilePath $InstallerPath `
        -RawArguments "/S /D=$installRoot"
    if ($result.installerExitCode -ne 0) {
        throw "upgrade installer exited with code $($result.installerExitCode)"
    }

    $result.staleUiRemovedOnUpgrade = -not (Test-Path -LiteralPath $staleUiPath)
    if (-not $result.staleUiRemovedOnUpgrade) {
        throw "upgrade left an obsolete UI generation in the install directory"
    }

    $upgradedRelease = Get-Content -LiteralPath $installedReleasePath -Raw | ConvertFrom-Json
    $result.upgradedVersion = [string]$upgradedRelease.version
    if ($result.upgradedVersion -ne $metadata.Version) {
        throw "upgraded release metadata does not contain the target version"
    }

    $expectedStartupValue = '"{0}"' -f $installedExecutable
    $startupAfterUpgrade = Get-OptionalRegistryValue `
        -Path $runRegistryPath `
        -Name $metadata.StartupValueName
    $result.startupPreservedOnUpgrade = $startupAfterUpgrade -eq $expectedStartupValue
    if (-not $result.startupPreservedOnUpgrade) {
        throw "upgrade did not preserve and update the enabled startup value"
    }

    $result.installedExecutableFound = Test-Path `
        -LiteralPath $installedExecutable `
        -PathType Leaf
    $result.installedManifestFound = Test-Path `
        -LiteralPath (Join-Path $installRoot "ui\dist\manifest.json") `
        -PathType Leaf
    $result.releaseMetadataFound = Test-Path `
        -LiteralPath $installedReleasePath `
        -PathType Leaf
    if (-not $result.installedExecutableFound -or
        -not $result.installedManifestFound -or
        -not $result.releaseMetadataFound) {
        throw "upgraded executable, release metadata, or UI manifest is missing"
    }

    $result.appsAndFeaturesFound = Test-Path -LiteralPath $uninstallRegistryPath
    if (-not $result.appsAndFeaturesFound) {
        throw "Apps & Features registry entry is missing"
    }
    $uninstallEntry = Get-ItemProperty -LiteralPath $uninstallRegistryPath
    $expectedQuietUninstallString = '"{0}" --quiet-uninstall' -f $installedExecutable
    $result.quietUninstallStringValid = `
        $uninstallEntry.QuietUninstallString -eq $expectedQuietUninstallString
    $result.appsAndFeaturesValuesValid = `
        $uninstallEntry.DisplayName -eq $metadata.ProductName -and `
        $uninstallEntry.DisplayVersion -eq $metadata.Version -and `
        $uninstallEntry.Publisher -eq $metadata.Publisher -and `
        $uninstallEntry.InstallLocation -eq $installRoot -and `
        $uninstallEntry.NoModify -eq 1 -and `
        $uninstallEntry.NoRepair -eq 1 -and `
        $result.quietUninstallStringValid
    if (-not $result.appsAndFeaturesValuesValid) {
        throw "Apps & Features registry values do not match release metadata"
    }

    $activeSession = Start-NativeMainWindow `
        -ExecutablePath $installedExecutable `
        -LogPath $launchLogPath `
        -WindowClass $metadata.WindowClass `
        -WindowTitle $metadata.WindowTitle
    $result.mainWindowHandle = $activeSession.HandleText
    $result.mainWindowValid = $activeSession.Valid
    $result.mainWindowVisible = $activeSession.Visible
    $result.mainWindowProcessMatched = $activeSession.ProcessMatched

    $result.occupiedInstallerExitCode = Invoke-NativeProcess `
        -FilePath $InstallerPath `
        -RawArguments "/S /D=$installRoot"
    if ($result.occupiedInstallerExitCode -ne 32) {
        throw "occupied silent installer exited with code $($result.occupiedInstallerExitCode), expected 32"
    }

    $result.occupiedUninstallerExitCode = Invoke-NativeProcess `
        -FilePath $installedExecutable `
        -Arguments @("--quiet-uninstall")
    if ($result.occupiedUninstallerExitCode -ne 32) {
        throw "occupied silent uninstaller exited with code $($result.occupiedUninstallerExitCode), expected 32"
    }

    $result.runningApplicationSurvivedOccupiedChecks = `
        -not $activeSession.Process.HasExited -and `
        [NativeInstallerSmoke]::IsWindow($activeSession.Window)
    if (-not $result.runningApplicationSurvivedOccupiedChecks) {
        throw "occupied installer or uninstaller terminated the running application"
    }

    $result.launchExitCode = Stop-NativeMainWindow -Session $activeSession
    $activeSession = $null
    if ($result.launchExitCode -ne 0) {
        throw "installed executable exited with code $($result.launchExitCode)"
    }

    $uninstallAttempted = $true
    $result.uninstallerExitCode = Invoke-NativeProcess `
        -FilePath $installedExecutable `
        -Arguments @("--quiet-uninstall")
    if ($result.uninstallerExitCode -ne 0) {
        throw "silent uninstaller exited with code $($result.uninstallerExitCode)"
    }
} catch {
    $result.errors += $_.Exception.Message
} finally {
    if ($null -ne $activeSession -and -not $activeSession.Process.HasExited) {
        try {
            [void](Stop-NativeMainWindow -Session $activeSession)
        } catch {
            Stop-Process `
                -Id $activeSession.Process.Id `
                -Force `
                -ErrorAction SilentlyContinue
            $result.errors += "cleanup application stop failed: $($_.Exception.Message)"
        }
    }
    $canCleanupWithHelper = Test-Path `
        -LiteralPath $installedExecutable `
        -PathType Leaf
    $canCleanupWithUninstaller = Test-Path `
        -LiteralPath $uninstallerPath `
        -PathType Leaf
    if (-not $uninstallAttempted -and ($canCleanupWithHelper -or $canCleanupWithUninstaller)) {
        try {
            $uninstallAttempted = $true
            if ($canCleanupWithHelper) {
                $result.uninstallerExitCode = Invoke-NativeProcess `
                    -FilePath $installedExecutable `
                    -Arguments @("--quiet-uninstall")
            } else {
                $result.uninstallerExitCode = Invoke-NativeProcess `
                    -FilePath $uninstallerPath `
                    -Arguments @("/S")
            }
        } catch {
            $result.errors += "cleanup uninstall failed: $($_.Exception.Message)"
        }
    }

    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    while ((Test-Path -LiteralPath $installRoot) -and [DateTime]::UtcNow -lt $deadline) {
        Start-Sleep -Milliseconds 100
    }
    $result.installDirectoryRemoved = -not (Test-Path -LiteralPath $installRoot)
    $result.registryRemoved = -not (Test-Path -LiteralPath $registryPath)
    $result.uninstallRegistryRemoved = -not (
        Test-Path -LiteralPath $uninstallRegistryPath
    )
    $result.startMenuRemoved = -not (Test-Path -LiteralPath $startMenuPath)
    $remainingStartup = Get-OptionalRegistryValue `
        -Path $runRegistryPath `
        -Name $metadata.StartupValueName
    $result.startupValueRemoved = $null -eq $remainingStartup
    $wpfStartupAfter = Get-OptionalRegistryValue `
        -Path $runRegistryPath `
        -Name $wpfStartupValueName
    $result.wpfStartupPreserved = $wpfStartupAfter -eq $wpfStartupBefore
    if (Test-Path -LiteralPath $applicationDataPath) {
        try {
            $capturedDataPath = Join-Path $VerificationRoot "test-app-data"
            if (Test-Path -LiteralPath $capturedDataPath) {
                throw "captured application data path already exists"
            }
            Move-Item `
                -LiteralPath $applicationDataPath `
                -Destination $capturedDataPath
            $result.applicationDataCaptured = $true
        } catch {
            $result.errors += "application data isolation failed: $($_.Exception.Message)"
        }
    }
    $result.applicationDataIsolated = -not (Test-Path -LiteralPath $applicationDataPath)
    if ($startupValueCreated -and -not $result.startupValueRemoved) {
        Remove-ItemProperty `
            -LiteralPath $runRegistryPath `
            -Name $metadata.StartupValueName `
            -Force `
            -ErrorAction SilentlyContinue
    }
    $result.passed = `
        $result.initialInstallerExitCode -eq 0 -and `
        $result.installerExitCode -eq 0 -and `
        $result.upgradedVersion -eq $metadata.Version -and `
        $result.staleUiRemovedOnUpgrade -and `
        $result.startupPreservedOnUpgrade -and `
        $result.occupiedInstallerExitCode -eq 32 -and `
        $result.occupiedUninstallerExitCode -eq 32 -and `
        $result.runningApplicationSurvivedOccupiedChecks -and `
        $result.launchExitCode -eq 0 -and `
        $result.mainWindowValid -and `
        $result.mainWindowVisible -and `
        $result.mainWindowProcessMatched -and `
        $result.releaseMetadataFound -and `
        $result.appsAndFeaturesFound -and `
        $result.appsAndFeaturesValuesValid -and `
        $result.quietUninstallStringValid -and `
        $result.uninstallerExitCode -eq 0 -and `
        $result.startupValueRemoved -and `
        $result.wpfStartupPreserved -and `
        $result.applicationDataIsolated -and `
        $result.installDirectoryRemoved -and `
        $result.registryRemoved -and `
        $result.uninstallRegistryRemoved -and `
        $result.startMenuRemoved -and `
        $result.errors.Count -eq 0

    $result | ConvertTo-Json -Depth 6 | Set-Content `
        -LiteralPath $verificationPath `
        -Encoding UTF8
}

$result | ConvertTo-Json -Depth 6
if (-not $result.passed) {
    exit 1
}
exit 0
