//go:build windows

package main

import (
	"fmt"
	"image"
	"image/color"
	"unsafe"

	"golang.org/x/sys/windows"
)

func drawGDITextMask(request textMaskRequest) (*image.Alpha, error) {
	mask := image.NewAlpha(image.Rect(
		0,
		0,
		max(0, request.Width),
		max(0, request.Height),
	))
	if request.Value == "" || request.Width <= 0 || request.Height <= 0 {
		return mask, nil
	}
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, screenDC)
	memoryDC, _, lastErr := procCreateCompatibleDC.Call(screenDC)
	if memoryDC == 0 {
		return nil, callError("CreateCompatibleDC", lastErr)
	}
	defer procDeleteDC.Call(memoryDC)

	info := bitmapInfo{Header: bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(request.Width),
		Height:      -int32(request.Height),
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}}
	var pixelAddress unsafe.Pointer
	bitmap, _, lastErr := procCreateDIBSection.Call(
		memoryDC,
		uintptr(unsafe.Pointer(&info)),
		dibRGBColors,
		uintptr(unsafe.Pointer(&pixelAddress)),
		0,
		0,
	)
	if bitmap == 0 {
		return nil, callError("CreateDIBSection", lastErr)
	}
	defer procDeleteObject.Call(bitmap)
	previousBitmap, _, _ := procSelectObject.Call(memoryDC, bitmap)
	if previousBitmap != 0 {
		defer procSelectObject.Call(memoryDC, previousBitmap)
	}
	pixels := unsafe.Slice(
		(*byte)(pixelAddress),
		request.Width*request.Height*4,
	)
	clear(pixels)

	fontFamily := preferredFallbackFontFamily(request.FontFamilies)
	font, _, lastErr := procCreateFontW.Call(
		signed(-int32(request.FontPixels+0.5)),
		0,
		0,
		0,
		uintptr(request.FontWeight),
		0,
		0,
		0,
		defaultCharset,
		0,
		0,
		antialiasedQuality,
		0,
		uintptr(unsafe.Pointer(utf16Pointer(fontFamily))),
	)
	if font == 0 {
		return nil, callError("CreateFontW", lastErr)
	}
	defer procDeleteObject.Call(font)
	previousFont, _, _ := procSelectObject.Call(memoryDC, font)
	if previousFont != 0 {
		defer procSelectObject.Call(memoryDC, previousFont)
	}
	procSetBkMode.Call(memoryDC, bkModeTransparent)
	procSetTextColor.Call(memoryDC, 0x00ffffff)

	encoded, err := windows.UTF16FromString(request.Value)
	if err != nil {
		return nil, fmt.Errorf("encoding text: %w", err)
	}
	flags := uintptr(drawTextVCenter | drawTextSingleLine | drawTextNoPrefix | drawTextEndEllipsis)
	switch request.Align {
	case "center":
		flags |= drawTextCenter
	case "right":
		flags |= drawTextRight
	}
	rect := winRect{Right: int32(request.Width), Bottom: int32(request.Height)}
	result, _, lastErr := procDrawTextW.Call(
		memoryDC,
		uintptr(unsafe.Pointer(&encoded[0])),
		uintptr(len(encoded)-1),
		uintptr(unsafe.Pointer(&rect)),
		flags,
	)
	if result == 0 {
		return nil, callError("DrawTextW", lastErr)
	}
	for y := range request.Height {
		for x := range request.Width {
			offset := (y*request.Width + x) * 4
			coverage := max(pixels[offset], pixels[offset+1], pixels[offset+2])
			mask.SetAlpha(x, y, color.Alpha{A: coverage})
		}
	}
	return mask, nil
}
