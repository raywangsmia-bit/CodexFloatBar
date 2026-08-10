//go:build windows

package codexprocess

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	codexHostProcessName    = "codex-code-mode-host"
	chatGPTProcessName      = "ChatGPT"
	maxPackageFullNameChars = 4096
	maxProcessImageChars    = 32768
)

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procGetPackageFullName = kernel32.NewProc("GetPackageFullName")
	user32                 = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows        = user32.NewProc("EnumWindows")
	procGetWindowProcessID = user32.NewProc("GetWindowThreadProcessId")
	procIsIconic           = user32.NewProc("IsIconic")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
)

type windowsDetector struct{}

type processRecord struct {
	Name            string
	ImagePath       string
	PackageFullName string
}

// IsRunning performs one read-only scan of the Windows process table.
func IsRunning(ctx context.Context) (bool, error) {
	return windowsDetector{}.Running(ctx)
}

// IsDesktopExecutablePath matches only the packaged Codex ChatGPT host path.
func IsDesktopExecutablePath(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	normalized := strings.ReplaceAll(path, "/", `\`)
	if !strings.EqualFold(filepath.Base(normalized), "ChatGPT.exe") {
		return false
	}
	return strings.Contains(
		strings.ToLower(normalized),
		`\windowsapps\openai.codex_`,
	)
}

// IsPackageFullName matches the Microsoft Store Codex package family.
func IsPackageFullName(packageFullName string) bool {
	return len(packageFullName) >= len("OpenAI.Codex_") &&
		strings.EqualFold(packageFullName[:len("OpenAI.Codex_")], "OpenAI.Codex_")
}

func (windowsDetector) Running(ctx context.Context) (bool, error) {
	state, err := windowsDetector{}.State(ctx)
	return state.Running, err
}

func (windowsDetector) State(ctx context.Context) (observedState, error) {
	if err := ctx.Err(); err != nil {
		return observedState{}, err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return observedState{}, fmt.Errorf("snapshotting Windows processes: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	running := false
	windowProcesses := map[uint32]struct{}{}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return observedState{}, nil
		}
		return observedState{}, fmt.Errorf("reading first Windows process: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return observedState{}, err
		}
		name := windows.UTF16ToString(entry.ExeFile[:])
		if isCodexHostProcessName(name) {
			running = true
		}
		if isChatGPTProcessName(name) {
			record := inspectChatGPTProcess(entry.ProcessID, name)
			if matchesCodexProcess(record) {
				running = true
				windowProcesses[entry.ProcessID] = struct{}{}
			}
		}

		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return observedState{}, fmt.Errorf("reading next Windows process: %w", err)
		}
	}

	if !running || len(windowProcesses) == 0 {
		return normalizeObservedWindowState(running, len(windowProcesses), false), nil
	}
	visible, err := hasVisibleCodexWindow(ctx, windowProcesses)
	if err != nil {
		return observedState{}, err
	}
	return normalizeObservedWindowState(true, len(windowProcesses), visible), nil
}

func hasVisibleCodexWindow(ctx context.Context, processIDs map[uint32]struct{}) (bool, error) {
	visible := false
	callback := syscall.NewCallback(func(window uintptr, _ uintptr) uintptr {
		if ctx.Err() != nil {
			return 0
		}
		var processID uint32
		procGetWindowProcessID.Call(window, uintptr(unsafe.Pointer(&processID)))
		if _, ok := processIDs[processID]; !ok {
			return 1
		}
		shown, _, _ := procIsWindowVisible.Call(window)
		minimized, _, _ := procIsIconic.Call(window)
		if shown == 0 || minimized != 0 {
			return 1
		}
		visible = true
		return 0
	})
	procEnumWindows.Call(callback, 0)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return visible, nil
}

func normalizeObservedWindowState(
	running bool,
	windowProcessCount int,
	visible bool,
) observedState {
	if !running {
		return observedState{}
	}
	if windowProcessCount == 0 {
		return observedState{Running: true, Visible: true}
	}
	return observedState{Running: true, Visible: visible}
}

func inspectChatGPTProcess(processID uint32, name string) processRecord {
	record := processRecord{Name: name}
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		processID,
	)
	if err != nil {
		return record
	}
	defer windows.CloseHandle(handle)

	record.PackageFullName = readPackageFullName(handle)
	if IsPackageFullName(record.PackageFullName) {
		return record
	}
	record.ImagePath = readProcessImagePath(handle)
	return record
}

func readPackageFullName(process windows.Handle) string {
	var length uint32
	result, _, _ := procGetPackageFullName.Call(
		uintptr(process),
		uintptr(unsafe.Pointer(&length)),
		0,
	)
	if syscall.Errno(result) != windows.ERROR_INSUFFICIENT_BUFFER ||
		length == 0 || length > maxPackageFullNameChars {
		return ""
	}
	buffer := make([]uint16, length)
	result, _, _ = procGetPackageFullName.Call(
		uintptr(process),
		uintptr(unsafe.Pointer(&length)),
		uintptr(unsafe.Pointer(&buffer[0])),
	)
	if result != 0 {
		return ""
	}
	return windows.UTF16ToString(buffer)
}

func readProcessImagePath(process windows.Handle) string {
	buffer := make([]uint16, maxProcessImageChars)
	length := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(
		process,
		0,
		&buffer[0],
		&length,
	); err != nil || length == 0 || length > uint32(len(buffer)) {
		return ""
	}
	return windows.UTF16ToString(buffer[:length])
}

func matchesCodexProcess(record processRecord) bool {
	if isCodexHostProcessName(record.Name) {
		return true
	}
	if !isChatGPTProcessName(record.Name) {
		return false
	}
	return IsPackageFullName(record.PackageFullName) ||
		IsDesktopExecutablePath(record.ImagePath)
}

func isCodexHostProcessName(name string) bool {
	return strings.EqualFold(processName(name), codexHostProcessName)
}

func isChatGPTProcessName(name string) bool {
	return strings.EqualFold(processName(name), chatGPTProcessName)
}

func processName(name string) string {
	base := filepath.Base(strings.ReplaceAll(name, "/", `\`))
	if len(base) >= len(".exe") && strings.EqualFold(base[len(base)-len(".exe"):], ".exe") {
		return base[:len(base)-len(".exe")]
	}
	return base
}
