//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const applicationRunningExitCode = 32

func runQuietUninstall() (int, error) {
	if nativeInstanceRunning() {
		return applicationRunningExitCode, nil
	}

	executablePath, err := os.Executable()
	if err != nil {
		return 1, fmt.Errorf("resolving executable path: %w", err)
	}
	uninstallerPath := filepath.Join(filepath.Dir(executablePath), "Uninstall.exe")
	command := exec.Command(uninstallerPath, "/S")
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return exitError.ExitCode(), nil
		}
		return 1, fmt.Errorf("starting silent uninstaller: %w", err)
	}
	return 0, nil
}

func nativeInstanceRunning() bool {
	mutexName, err := windows.UTF16PtrFromString(nativeMutexName)
	if err == nil {
		mutex, openErr := windows.OpenMutex(windows.SYNCHRONIZE, false, mutexName)
		if openErr == nil {
			_ = windows.CloseHandle(mutex)
			return true
		}
	}

	className := utf16Pointer(nativeWindowClass)
	windowTitle := utf16Pointer(mainWindowTitle)
	window, _, _ := procFindWindowW.Call(
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowTitle)),
	)
	return window != 0
}
