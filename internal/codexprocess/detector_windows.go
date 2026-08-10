//go:build windows

package codexprocess

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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
	procGetWindowRect      = user32.NewProc("GetWindowRect")
	procIsIconic           = user32.NewProc("IsIconic")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	dwmapi                 = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmGetWindowAttr   = dwmapi.NewProc("DwmGetWindowAttribute")
)

const (
	dwmwaExtendedFrameBounds = 9
	dwmwaCloaked             = 14
)

type windowsDetector struct {
	mu              sync.RWMutex
	running         bool
	windowProcesses map[uint32]struct{}
}

type screenRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

var zOrderCollector = struct {
	sync.Mutex
	ctx              context.Context
	processIDs       map[uint32]struct{}
	currentProcessID uint32
	occluders        []screenRect
	visible          bool
}{
	processIDs: map[uint32]struct{}{},
	occluders:  []screenRect{},
}

var zOrderEnumCallback = syscall.NewCallback(func(window uintptr, _ uintptr) uintptr {
	if zOrderCollector.ctx.Err() != nil {
		return 0
	}
	var processID uint32
	procGetWindowProcessID.Call(window, uintptr(unsafe.Pointer(&processID)))
	if _, ok := zOrderCollector.processIDs[processID]; ok {
		shown, _, _ := procIsWindowVisible.Call(window)
		minimized, _, _ := procIsIconic.Call(window)
		if shown == 0 || minimized != 0 || windowCloaked(window) {
			return 1
		}
		rect, ok := visibleWindowRect(window)
		if !ok {
			return 1
		}
		if !rectFullyCovered(rect, zOrderCollector.occluders) {
			zOrderCollector.visible = true
			return 0
		}
		return 1
	}
	if processID == zOrderCollector.currentProcessID || !windowCanOcclude(window) {
		return 1
	}
	if rect, ok := visibleWindowRect(window); ok {
		zOrderCollector.occluders = append(zOrderCollector.occluders, rect)
	}
	return 1
})

type processRecord struct {
	Name            string
	ImagePath       string
	PackageFullName string
}

// IsRunning performs one read-only scan of the Windows process table.
func IsRunning(ctx context.Context) (bool, error) {
	return newWindowsDetector().Running(ctx)
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

func newWindowsDetector() *windowsDetector {
	return &windowsDetector{windowProcesses: map[uint32]struct{}{}}
}

func (detector *windowsDetector) Running(ctx context.Context) (bool, error) {
	state, err := detector.State(ctx)
	return state.Running, err
}

func (detector *windowsDetector) State(ctx context.Context) (observedState, error) {
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
	hostProcesses := map[uint32]struct{}{}
	chatGPTProcesses := map[uint32]struct{}{}
	parentProcesses := map[uint32]uint32{}
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
		parentProcesses[entry.ProcessID] = entry.ParentProcessID
		if isCodexHostProcessName(name) {
			running = true
			hostProcesses[entry.ProcessID] = struct{}{}
		}
		if isChatGPTProcessName(name) {
			chatGPTProcesses[entry.ProcessID] = struct{}{}
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
	addHostAncestorWindows(
		windowProcesses,
		hostProcesses,
		chatGPTProcesses,
		parentProcesses,
	)

	if !running || len(windowProcesses) == 0 {
		detector.cache(running, windowProcesses)
		return normalizeObservedWindowState(running, len(windowProcesses), false), nil
	}
	detector.cache(running, windowProcesses)
	visible, err := hasVisibleCodexWindow(ctx, windowProcesses)
	if err != nil {
		return observedState{}, err
	}
	return normalizeObservedWindowState(true, len(windowProcesses), visible), nil
}

func addHostAncestorWindows(
	windowProcesses map[uint32]struct{},
	hostProcesses map[uint32]struct{},
	chatGPTProcesses map[uint32]struct{},
	parentProcesses map[uint32]uint32,
) {
	for hostProcess := range hostProcesses {
		visited := map[uint32]struct{}{}
		processID := hostProcess
		for processID != 0 {
			if _, seen := visited[processID]; seen {
				break
			}
			visited[processID] = struct{}{}
			parentProcess, ok := parentProcesses[processID]
			if !ok {
				break
			}
			if _, isChatGPT := chatGPTProcesses[parentProcess]; isChatGPT {
				windowProcesses[parentProcess] = struct{}{}
				break
			}
			processID = parentProcess
		}
	}
}

func (detector *windowsDetector) EventState(ctx context.Context) (observedState, error) {
	running, windowProcesses := detector.cachedProcesses()
	if !running || len(windowProcesses) == 0 {
		return normalizeObservedWindowState(running, len(windowProcesses), false), nil
	}
	visible, err := hasVisibleCodexWindow(ctx, windowProcesses)
	if err != nil {
		return observedState{}, err
	}
	return normalizeObservedWindowState(true, len(windowProcesses), visible), nil
}

func (detector *windowsDetector) cache(
	running bool,
	windowProcesses map[uint32]struct{},
) {
	detector.mu.Lock()
	defer detector.mu.Unlock()

	detector.running = running
	detector.windowProcesses = cloneProcessIDs(windowProcesses)
}

func (detector *windowsDetector) cachedProcesses() (bool, map[uint32]struct{}) {
	detector.mu.RLock()
	defer detector.mu.RUnlock()

	return detector.running, cloneProcessIDs(detector.windowProcesses)
}

func cloneProcessIDs(processIDs map[uint32]struct{}) map[uint32]struct{} {
	cloned := make(map[uint32]struct{}, len(processIDs))
	for processID := range processIDs {
		cloned[processID] = struct{}{}
	}
	return cloned
}

func hasVisibleCodexWindow(ctx context.Context, processIDs map[uint32]struct{}) (bool, error) {
	zOrderCollector.Lock()
	defer zOrderCollector.Unlock()
	zOrderCollector.ctx = ctx
	zOrderCollector.processIDs = processIDs
	zOrderCollector.currentProcessID = windows.GetCurrentProcessId()
	zOrderCollector.occluders = zOrderCollector.occluders[:0]
	zOrderCollector.visible = false
	procEnumWindows.Call(zOrderEnumCallback, 0)
	visible := zOrderCollector.visible
	zOrderCollector.ctx = nil
	zOrderCollector.processIDs = map[uint32]struct{}{}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return visible, nil
}

func windowCanOcclude(window uintptr) bool {
	shown, _, _ := procIsWindowVisible.Call(window)
	minimized, _, _ := procIsIconic.Call(window)
	return shown != 0 && minimized == 0 && !windowCloaked(window)
}

func windowCloaked(window uintptr) bool {
	var cloaked uint32
	result, _, _ := procDwmGetWindowAttr.Call(
		window,
		dwmwaCloaked,
		uintptr(unsafe.Pointer(&cloaked)),
		unsafe.Sizeof(cloaked),
	)
	return result == 0 && cloaked != 0
}

func visibleWindowRect(window uintptr) (screenRect, bool) {
	var rect screenRect
	dwmResult, _, _ := procDwmGetWindowAttr.Call(
		window,
		dwmwaExtendedFrameBounds,
		uintptr(unsafe.Pointer(&rect)),
		unsafe.Sizeof(rect),
	)
	if dwmResult == 0 {
		return rect, validScreenRect(rect)
	}
	windowResult, _, _ := procGetWindowRect.Call(
		window,
		uintptr(unsafe.Pointer(&rect)),
	)
	return rect, windowResult != 0 && validScreenRect(rect)
}

func validScreenRect(rect screenRect) bool {
	return rect.Right > rect.Left && rect.Bottom > rect.Top
}

func rectFullyCovered(target screenRect, covers []screenRect) bool {
	remaining := []screenRect{target}
	for _, cover := range covers {
		next := []screenRect{}
		for _, fragment := range remaining {
			next = append(next, subtractRect(fragment, cover)...)
		}
		remaining = next
		if len(remaining) == 0 {
			return true
		}
	}
	return false
}

func subtractRect(target screenRect, cover screenRect) []screenRect {
	intersection := screenRect{
		Left:   max(target.Left, cover.Left),
		Top:    max(target.Top, cover.Top),
		Right:  min(target.Right, cover.Right),
		Bottom: min(target.Bottom, cover.Bottom),
	}
	if intersection.Right <= intersection.Left || intersection.Bottom <= intersection.Top {
		return []screenRect{target}
	}

	remaining := []screenRect{}
	appendRect := func(rect screenRect) {
		if rect.Right > rect.Left && rect.Bottom > rect.Top {
			remaining = append(remaining, rect)
		}
	}
	appendRect(screenRect{
		Left: target.Left, Top: target.Top,
		Right: target.Right, Bottom: intersection.Top,
	})
	appendRect(screenRect{
		Left: target.Left, Top: intersection.Bottom,
		Right: target.Right, Bottom: target.Bottom,
	})
	appendRect(screenRect{
		Left: target.Left, Top: intersection.Top,
		Right: intersection.Left, Bottom: intersection.Bottom,
	})
	appendRect(screenRect{
		Left: intersection.Right, Top: intersection.Top,
		Right: target.Right, Bottom: intersection.Bottom,
	})
	return remaining
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
