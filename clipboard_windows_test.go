//go:build windows

package main

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestEncodeClipboardTextProducesTerminatedUTF16(t *testing.T) {
	encoded, err := encodeClipboardText("状态🙂")
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) < 2 || encoded[len(encoded)-1] != 0 {
		t.Fatalf("encoded clipboard text is not NUL-terminated: %v", encoded)
	}
	if got := windows.UTF16ToString(encoded); got != "状态🙂" {
		t.Fatalf("decoded clipboard text = %q", got)
	}
}

func TestEncodeClipboardTextRejectsEmbeddedNUL(t *testing.T) {
	if _, err := encodeClipboardText("before\x00after"); err == nil {
		t.Fatal("embedded NUL was accepted")
	}
}

func TestClipboardWriterRetriesAndTransfersMemoryOwnership(t *testing.T) {
	backend := &fakeClipboardBackend{
		memory:           42,
		openFailures:     2,
		openFailureCause: errors.New("clipboard busy"),
	}
	waits := []time.Duration{}
	writer := clipboardWriter{
		backend: backend,
		wait: func(delay time.Duration) {
			waits = append(waits, delay)
		},
	}

	if err := writer.write(101, "current status"); err != nil {
		t.Fatal(err)
	}
	if backend.openCalls != 3 {
		t.Fatalf("open calls = %d, want 3", backend.openCalls)
	}
	if len(waits) != 2 || waits[0] != clipboardRetryInterval || waits[1] != clipboardRetryInterval {
		t.Fatalf("retry waits = %v", waits)
	}
	if backend.allocatedText == nil || windows.UTF16ToString(backend.allocatedText) != "current status" {
		t.Fatalf("allocated text = %v", backend.allocatedText)
	}
	if backend.emptyCalls != 1 || backend.setCalls != 1 || backend.closeCalls != 1 {
		t.Fatalf(
			"clipboard calls empty/set/close = %d/%d/%d",
			backend.emptyCalls,
			backend.setCalls,
			backend.closeCalls,
		)
	}
	if backend.freeCalls != 0 {
		t.Fatalf("memory was freed after ownership transfer: %d calls", backend.freeCalls)
	}
}

func TestClipboardWriterFreesMemoryWhenSetFails(t *testing.T) {
	cause := errors.New("set failed")
	backend := &fakeClipboardBackend{
		memory:   42,
		setError: cause,
	}
	writer := clipboardWriter{
		backend: backend,
		wait:    func(time.Duration) {},
	}

	err := writer.write(0, "status")
	if !errors.Is(err, cause) {
		t.Fatalf("write error = %v, want %v", err, cause)
	}
	if backend.freeCalls != 1 {
		t.Fatalf("free calls = %d, want 1", backend.freeCalls)
	}
	if backend.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", backend.closeCalls)
	}
}

func TestClipboardWriterFreesMemoryWhenClipboardStaysBusy(t *testing.T) {
	cause := errors.New("clipboard busy")
	backend := &fakeClipboardBackend{
		memory:           42,
		openFailures:     clipboardOpenAttempts,
		openFailureCause: cause,
	}
	waits := 0
	writer := clipboardWriter{
		backend: backend,
		wait: func(time.Duration) {
			waits++
		},
	}

	err := writer.write(0, "status")
	if !errors.Is(err, cause) {
		t.Fatalf("write error = %v, want %v", err, cause)
	}
	if backend.openCalls != clipboardOpenAttempts || waits != clipboardOpenAttempts-1 {
		t.Fatalf("open calls/waits = %d/%d", backend.openCalls, waits)
	}
	if backend.freeCalls != 1 || backend.closeCalls != 0 {
		t.Fatalf("free/close calls = %d/%d", backend.freeCalls, backend.closeCalls)
	}
}

func TestClipboardWriterDoesNotFreeTransferredMemoryWhenCloseFails(t *testing.T) {
	cause := errors.New("close failed")
	backend := &fakeClipboardBackend{
		memory:     42,
		closeError: cause,
	}
	writer := clipboardWriter{
		backend: backend,
		wait:    func(time.Duration) {},
	}

	err := writer.write(0, "status")
	if !errors.Is(err, cause) {
		t.Fatalf("write error = %v, want %v", err, cause)
	}
	if backend.freeCalls != 0 {
		t.Fatalf("transferred memory was freed: %d calls", backend.freeCalls)
	}
}

type fakeClipboardBackend struct {
	memory           uintptr
	allocatedText    []uint16
	openFailures     int
	openFailureCause error
	setError         error
	closeError       error
	openCalls        int
	emptyCalls       int
	setCalls         int
	closeCalls       int
	freeCalls        int
}

func (backend *fakeClipboardBackend) allocateUnicodeText(encoded []uint16) (uintptr, error) {
	backend.allocatedText = append([]uint16{}, encoded...)
	return backend.memory, nil
}

func (backend *fakeClipboardBackend) free(uintptr) error {
	backend.freeCalls++
	return nil
}

func (backend *fakeClipboardBackend) open(uintptr) error {
	backend.openCalls++
	if backend.openCalls <= backend.openFailures {
		return backend.openFailureCause
	}
	return nil
}

func (backend *fakeClipboardBackend) close() error {
	backend.closeCalls++
	return backend.closeError
}

func (backend *fakeClipboardBackend) empty() error {
	backend.emptyCalls++
	return nil
}

func (backend *fakeClipboardBackend) setUnicodeText(uintptr) error {
	backend.setCalls++
	return backend.setError
}
