param(
    [Parameter(Mandatory = $true)]
    [string]$WpfExecutablePath,
    [string]$VerificationRoot = ""
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$runtimeDataRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot "runtime-data"))
if (-not $VerificationRoot) {
    $runName = "wpf-rollback-verification-{0}-{1}" -f `
        (Get-Date -Format "yyyyMMdd-HHmmss"), `
        $PID
    $VerificationRoot = Join-Path $runtimeDataRoot $runName
}

$WpfExecutablePath = [IO.Path]::GetFullPath($WpfExecutablePath)
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
if (-not (Test-Path -LiteralPath $WpfExecutablePath -PathType Leaf)) {
    throw "WPF executable was not found: $WpfExecutablePath"
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

if (-not ("WpfRollbackSmoke" -as [type])) {
    Add-Type @'
using System;
using System.Runtime.InteropServices;
using System.Text;

public static class WpfRollbackSmoke {
    public delegate bool EnumWindowsProc(IntPtr window, IntPtr parameter);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    public static extern IntPtr FindWindow(string className, string title);

    [DllImport("user32.dll")]
    public static extern bool EnumWindows(EnumWindowsProc callback, IntPtr parameter);

    [DllImport("user32.dll")]
    public static extern bool IsWindow(IntPtr window);

    [DllImport("user32.dll")]
    public static extern bool IsWindowVisible(IntPtr window);

    [DllImport("user32.dll")]
    public static extern uint GetWindowThreadProcessId(IntPtr window, out uint processId);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern int GetWindowText(IntPtr window, StringBuilder text, int length);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern int GetClassName(IntPtr window, StringBuilder text, int length);

    public static IntPtr FindProcessWindow(uint expectedProcessId) {
        IntPtr found = IntPtr.Zero;
        EnumWindows((window, parameter) => {
            uint processId;
            GetWindowThreadProcessId(window, out processId);
            if (processId == expectedProcessId && IsWindow(window)) {
                if (IsWindowVisible(window)) {
                    found = window;
                    return false;
                }
                if (found == IntPtr.Zero) {
                    found = window;
                }
            }
            return true;
        }, IntPtr.Zero);
        return found;
    }

    public static string ReadWindowText(IntPtr window) {
        var text = new StringBuilder(512);
        GetWindowText(window, text, text.Capacity);
        return text.ToString();
    }

    public static string ReadWindowClass(IntPtr window) {
        var text = new StringBuilder(256);
        GetClassName(window, text, text.Capacity);
        return text.ToString();
    }
}
'@
}

$runRegistryPath = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
$startupValueName = "CodexFloatingBar"
$localAppDataRoot = [IO.Path]::GetFullPath(
    [Environment]::GetFolderPath("LocalApplicationData")
)
$applicationDataPath = [IO.Path]::GetFullPath(
    (Join-Path $localAppDataRoot "CodexFloatingBar")
)
$localAppDataPrefix = $localAppDataRoot.TrimEnd(
    [IO.Path]::DirectorySeparatorChar
) + [IO.Path]::DirectorySeparatorChar
if (-not $applicationDataPath.StartsWith(
    $localAppDataPrefix,
    [StringComparison]::OrdinalIgnoreCase
)) {
    throw "WPF data path resolves outside LocalAppData"
}
if (Test-Path -LiteralPath $applicationDataPath) {
    throw "refusing to touch existing WPF user data: $applicationDataPath"
}
$startupBefore = Get-OptionalRegistryValue `
    -Path $runRegistryPath `
    -Name $startupValueName

New-Item -ItemType Directory -Path $VerificationRoot -Force | Out-Null
$verificationPath = Join-Path $VerificationRoot "verification.json"
$process = $null
$result = [ordered]@{
    passed = $false
    executable = $WpfExecutablePath
    sha256 = (Get-FileHash -LiteralPath $WpfExecutablePath -Algorithm SHA256).Hash.ToLowerInvariant()
    processStarted = $false
    processStayedRunning = $false
    windowHandle = $null
    windowTitle = $null
    windowClass = $null
    windowValid = $false
    windowVisible = $false
    windowProcessMatched = $false
    wpfStartupPreserved = $false
    applicationDataCaptured = $false
    applicationDataIsolated = $false
    cleanupForced = $false
    cleanupProcessExited = $false
    errors = @()
}

try {
    $existingWindow = [WpfRollbackSmoke]::FindWindow($null, "CodexFloatingBar")
    if ($existingWindow -ne [IntPtr]::Zero) {
        throw "an existing WPF CodexFloatingBar window is already present"
    }

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $WpfExecutablePath
    $startInfo.UseShellExecute = $false
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    if (-not $process.Start()) {
        throw "failed to start WPF executable"
    }
    $result.processStarted = $true

    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    $window = [IntPtr]::Zero
    $windowVisible = $false
    [uint32]$windowProcessID = 0
    do {
        Start-Sleep -Milliseconds 50
        if ($process.HasExited) {
            break
        }
        $window = [WpfRollbackSmoke]::FindProcessWindow($process.Id)
        if ($window -ne [IntPtr]::Zero) {
            $windowVisible = [WpfRollbackSmoke]::IsWindowVisible($window)
            [void][WpfRollbackSmoke]::GetWindowThreadProcessId(
                $window,
                [ref]$windowProcessID
            )
        }
    } while (
        ($window -eq [IntPtr]::Zero -or
            -not $windowVisible -or
            $windowProcessID -ne $process.Id) -and
        [DateTime]::UtcNow -lt $deadline
    )

    $result.processStayedRunning = -not $process.HasExited
    $result.windowHandle = if ($window -ne [IntPtr]::Zero) {
        "0x{0:x}" -f $window.ToInt64()
    } else {
        $null
    }
    $result.windowTitle = if ($window -ne [IntPtr]::Zero) {
        [WpfRollbackSmoke]::ReadWindowText($window)
    } else {
        $null
    }
    $result.windowClass = if ($window -ne [IntPtr]::Zero) {
        [WpfRollbackSmoke]::ReadWindowClass($window)
    } else {
        $null
    }
    $result.windowValid = `
        $window -ne [IntPtr]::Zero -and `
        [WpfRollbackSmoke]::IsWindow($window)
    $result.windowVisible = `
        $result.windowValid -and `
        [WpfRollbackSmoke]::IsWindowVisible($window)
    $result.windowProcessMatched = $windowProcessID -eq $process.Id
    if (-not $result.processStayedRunning -or
        -not $result.windowValid -or
        -not $result.windowVisible -or
        -not $result.windowProcessMatched) {
        throw "WPF rollback executable did not create a valid visible window for its process"
    }
} catch {
    $result.errors += $_.Exception.Message
} finally {
    if ($null -ne $process -and -not $process.HasExited) {
        $result.cleanupForced = $true
        $process.Kill()
        if (-not $process.WaitForExit(5000)) {
            $result.errors += "WPF verification process did not exit during cleanup"
        }
    }
    $result.cleanupProcessExited = $null -eq $process -or $process.HasExited
    $startupAfter = Get-OptionalRegistryValue `
        -Path $runRegistryPath `
        -Name $startupValueName
    $result.wpfStartupPreserved = $startupAfter -eq $startupBefore
    if (Test-Path -LiteralPath $applicationDataPath) {
        try {
            $capturedDataPath = Join-Path $VerificationRoot "test-wpf-data"
            if (Test-Path -LiteralPath $capturedDataPath) {
                throw "captured WPF data path already exists"
            }
            Move-Item `
                -LiteralPath $applicationDataPath `
                -Destination $capturedDataPath
            $result.applicationDataCaptured = $true
        } catch {
            $result.errors += "WPF data isolation failed: $($_.Exception.Message)"
        }
    }
    $result.applicationDataIsolated = -not (Test-Path -LiteralPath $applicationDataPath)
    $result.passed = `
        $result.processStarted -and `
        $result.processStayedRunning -and `
        $result.windowValid -and `
        $result.windowVisible -and `
        $result.windowProcessMatched -and `
        $result.wpfStartupPreserved -and `
        $result.applicationDataIsolated -and `
        $result.cleanupProcessExited -and `
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
