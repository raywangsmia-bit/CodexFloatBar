//go:build windows

package main

import (
	"fmt"
	"image"
	"unsafe"
)

func updateLayeredWindow(window uintptr, source image.Image, destination geometryPoint) error {
	bounds := source.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid rendered surface size %dx%d", width, height)
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memoryDC, _, lastErr := procCreateCompatibleDC.Call(screenDC)
	if memoryDC == 0 {
		return callError("CreateCompatibleDC", lastErr)
	}
	defer procDeleteDC.Call(memoryDC)

	info := bitmapInfo{
		Header: bitmapInfoHeader{
			Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			Width:       int32(width),
			Height:      -int32(height),
			Planes:      1,
			BitCount:    32,
			Compression: biRGB,
		},
	}
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
		return callError("CreateDIBSection", lastErr)
	}
	defer procDeleteObject.Call(bitmap)

	previous, _, _ := procSelectObject.Call(memoryDC, bitmap)
	if previous != 0 {
		defer procSelectObject.Call(memoryDC, previous)
	}

	pixels := unsafe.Slice((*byte)(pixelAddress), width*height*4)
	copyPremultipliedBGRA(pixels, source)

	destinationPoint := winPoint{X: int32(destination.X), Y: int32(destination.Y)}
	size := winSize{CX: int32(width), CY: int32(height)}
	sourcePoint := winPoint{}
	blend := blendFunction{
		Operation:     acSrcOver,
		ConstantAlpha: 255,
		AlphaFormat:   acSrcAlpha,
	}
	result, _, lastErr := procUpdateLayeredWindow.Call(
		window,
		screenDC,
		uintptr(unsafe.Pointer(&destinationPoint)),
		uintptr(unsafe.Pointer(&size)),
		memoryDC,
		uintptr(unsafe.Pointer(&sourcePoint)),
		0,
		uintptr(unsafe.Pointer(&blend)),
		ulwAlpha,
	)
	if result == 0 {
		return callError("UpdateLayeredWindow", lastErr)
	}
	return nil
}

func copyPremultipliedBGRA(destination []byte, source image.Image) {
	if nrgba, ok := source.(*image.NRGBA); ok {
		copyNRGBAToPremultipliedBGRA(destination, nrgba)
		return
	}
	bounds := source.Bounds()
	width := bounds.Dx()
	for y := range bounds.Dy() {
		for x := range width {
			red, green, blue, alpha := source.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			offset := (y*width + x) * 4
			destination[offset] = byte(blue >> 8)
			destination[offset+1] = byte(green >> 8)
			destination[offset+2] = byte(red >> 8)
			destination[offset+3] = byte(alpha >> 8)
		}
	}
}

func copyNRGBAToPremultipliedBGRA(
	destination []byte,
	source *image.NRGBA,
) {
	bounds := source.Bounds()
	width := bounds.Dx()
	for y := range bounds.Dy() {
		sourceOffset := source.PixOffset(bounds.Min.X, bounds.Min.Y+y)
		for x := range width {
			pixelOffset := sourceOffset + x*4
			destinationOffset := (y*width + x) * 4
			alpha := source.Pix[pixelOffset+3]
			destination[destinationOffset] = premultiplyColor(
				source.Pix[pixelOffset+2],
				alpha,
			)
			destination[destinationOffset+1] = premultiplyColor(
				source.Pix[pixelOffset+1],
				alpha,
			)
			destination[destinationOffset+2] = premultiplyColor(
				source.Pix[pixelOffset],
				alpha,
			)
			destination[destinationOffset+3] = alpha
		}
	}
}

func premultiplyColor(value byte, alpha byte) byte {
	expanded := uint32(value)
	expanded |= expanded << 8
	expanded *= uint32(alpha)
	expanded /= 0xff
	return byte(expanded >> 8)
}
