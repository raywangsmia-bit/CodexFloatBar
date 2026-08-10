//go:build windows && amd64

package main

import (
	"testing"
	"unsafe"
)

func TestWin32StructureSizes(t *testing.T) {
	tests := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{name: "WNDCLASSEXW", got: unsafe.Sizeof(windowClassEx{}), want: 80},
		{name: "MSG", got: unsafe.Sizeof(winMessage{}), want: 48},
		{name: "BITMAPINFOHEADER", got: unsafe.Sizeof(bitmapInfoHeader{}), want: 40},
		{name: "MONITORINFO", got: unsafe.Sizeof(monitorInfo{}), want: 40},
		{name: "NOTIFYICONDATAW", got: unsafe.Sizeof(notifyIconData{}), want: 976},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("got %d bytes, want %d", test.got, test.want)
			}
		})
	}
}
