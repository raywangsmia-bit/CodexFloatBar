//go:build windows

package main

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	cfUnicodeText          = 13
	globalMemoryMoveable   = 0x0002
	clipboardOpenAttempts  = 6
	clipboardRetryInterval = 15 * time.Millisecond
)

type clipboardBackend interface {
	allocateUnicodeText([]uint16) (uintptr, error)
	free(uintptr) error
	open(uintptr) error
	close() error
	empty() error
	setUnicodeText(uintptr) error
}

type clipboardWriter struct {
	backend clipboardBackend
	wait    func(time.Duration)
}

type windowsClipboardBackend struct{}

var (
	clipboardUser32      = windows.NewLazySystemDLL("user32.dll")
	clipboardKernel32    = windows.NewLazySystemDLL("kernel32.dll")
	clipboardNTDLL       = windows.NewLazySystemDLL("ntdll.dll")
	procOpenClipboard    = clipboardUser32.NewProc("OpenClipboard")
	procCloseClipboard   = clipboardUser32.NewProc("CloseClipboard")
	procEmptyClipboard   = clipboardUser32.NewProc("EmptyClipboard")
	procSetClipboardData = clipboardUser32.NewProc("SetClipboardData")
	procGlobalAlloc      = clipboardKernel32.NewProc("GlobalAlloc")
	procGlobalLock       = clipboardKernel32.NewProc("GlobalLock")
	procGlobalUnlock     = clipboardKernel32.NewProc("GlobalUnlock")
	procGlobalFree       = clipboardKernel32.NewProc("GlobalFree")
	procSetLastError     = clipboardKernel32.NewProc("SetLastError")
	procRtlMoveMemory    = clipboardNTDLL.NewProc("RtlMoveMemory")
)

func writeClipboardText(owner uintptr, text string) error {
	writer := clipboardWriter{
		backend: windowsClipboardBackend{},
		wait:    time.Sleep,
	}
	return writer.write(owner, text)
}

func (writer clipboardWriter) write(owner uintptr, text string) (resultErr error) {
	encoded, err := encodeClipboardText(text)
	if err != nil {
		return err
	}

	memory, err := writer.backend.allocateUnicodeText(encoded)
	if err != nil {
		return fmt.Errorf("allocating clipboard text: %w", err)
	}
	ownsMemory := true
	defer func() {
		if !ownsMemory {
			return
		}
		if freeErr := writer.backend.free(memory); freeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("freeing clipboard text: %w", freeErr))
		}
	}()

	wait := writer.wait
	if wait == nil {
		wait = time.Sleep
	}
	if err := openClipboardWithRetry(
		writer.backend,
		owner,
		clipboardOpenAttempts,
		clipboardRetryInterval,
		wait,
	); err != nil {
		return err
	}
	defer func() {
		if closeErr := writer.backend.close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("closing clipboard: %w", closeErr))
		}
	}()

	if err := writer.backend.empty(); err != nil {
		return fmt.Errorf("emptying clipboard: %w", err)
	}
	if err := writer.backend.setUnicodeText(memory); err != nil {
		return fmt.Errorf("setting clipboard text: %w", err)
	}

	// SetClipboardData owns the HGLOBAL after a successful call.
	ownsMemory = false
	return nil
}

func encodeClipboardText(text string) ([]uint16, error) {
	encoded, err := windows.UTF16FromString(text)
	if err != nil {
		return nil, fmt.Errorf("encoding clipboard text: %w", err)
	}
	return encoded, nil
}

func openClipboardWithRetry(
	backend clipboardBackend,
	owner uintptr,
	attempts int,
	delay time.Duration,
	wait func(time.Duration),
) error {
	if attempts < 1 {
		return errors.New("clipboard open attempts must be positive")
	}

	var lastErr error
	for attempt := range attempts {
		err := backend.open(owner)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt+1 < attempts {
			wait(delay)
		}
	}
	return fmt.Errorf("opening clipboard after %d attempts: %w", attempts, lastErr)
}

func (windowsClipboardBackend) allocateUnicodeText(encoded []uint16) (uintptr, error) {
	size := uintptr(len(encoded)) * unsafe.Sizeof(uint16(0))
	memory, _, lastErr := procGlobalAlloc.Call(globalMemoryMoveable, size)
	if memory == 0 {
		return 0, clipboardCallError("GlobalAlloc", lastErr)
	}

	address, _, lastErr := procGlobalLock.Call(memory)
	if address == 0 {
		_, _, _ = procGlobalFree.Call(memory)
		return 0, clipboardCallError("GlobalLock", lastErr)
	}

	procRtlMoveMemory.Call(
		address,
		uintptr(unsafe.Pointer(&encoded[0])),
		size,
	)
	runtime.KeepAlive(encoded)

	procSetLastError.Call(0)
	unlocked, _, lastErr := procGlobalUnlock.Call(memory)
	unlockFailed := unlocked == 0 && lastErr != nil && lastErr != syscall.Errno(0)
	if unlockFailed {
		_, _, _ = procGlobalFree.Call(memory)
		return 0, clipboardCallError("GlobalUnlock", lastErr)
	}
	return memory, nil
}

func (windowsClipboardBackend) free(memory uintptr) error {
	result, _, lastErr := procGlobalFree.Call(memory)
	if result != 0 {
		return clipboardCallError("GlobalFree", lastErr)
	}
	return nil
}

func (windowsClipboardBackend) open(owner uintptr) error {
	result, _, lastErr := procOpenClipboard.Call(owner)
	if result == 0 {
		return clipboardCallError("OpenClipboard", lastErr)
	}
	return nil
}

func (windowsClipboardBackend) close() error {
	result, _, lastErr := procCloseClipboard.Call()
	if result == 0 {
		return clipboardCallError("CloseClipboard", lastErr)
	}
	return nil
}

func (windowsClipboardBackend) empty() error {
	result, _, lastErr := procEmptyClipboard.Call()
	if result == 0 {
		return clipboardCallError("EmptyClipboard", lastErr)
	}
	return nil
}

func (windowsClipboardBackend) setUnicodeText(memory uintptr) error {
	result, _, lastErr := procSetClipboardData.Call(cfUnicodeText, memory)
	if result == 0 {
		return clipboardCallError("SetClipboardData", lastErr)
	}
	return nil
}

func clipboardCallError(name string, lastErr error) error {
	if lastErr == nil || lastErr == syscall.Errno(0) {
		return fmt.Errorf("%s failed", name)
	}
	return fmt.Errorf("%s failed: %w", name, lastErr)
}
