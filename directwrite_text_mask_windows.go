//go:build windows

package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	d2dFactoryTypeSingleThreaded = 0
	d2dRenderTargetTypeSoftware  = 1
	dxgiFormatBGRA8UNorm         = 87
	d2dAlphaModePremultiplied    = 1
	d2dTextAntialiasGrayscale    = 2
	d2dDrawTextOptionsClip       = 0x00000002

	dwriteFactoryTypeShared        = 0
	dwriteFontStyleNormal          = 0
	dwriteFontStretchNormal        = 5
	dwriteTextAlignmentLeading     = 0
	dwriteTextAlignmentTrailing    = 1
	dwriteTextAlignmentCenter      = 2
	dwriteParagraphAlignmentCenter = 2
	dwriteWordWrappingNoWrap       = 1
	dwriteTrimmingCharacter        = 1
	dwriteMeasuringModeNatural     = 0
)

const (
	d2dFactoryCreateDCRenderTarget = 16
	d2dRenderTargetCreateBrush     = 8
	d2dRenderTargetDrawText        = 27
	d2dRenderTargetSetTextAA       = 34
	d2dRenderTargetClear           = 47
	d2dRenderTargetBeginDraw       = 48
	d2dRenderTargetEndDraw         = 49
	d2dDCRenderTargetBindDC        = 57

	dwriteFactoryCreateTextFormat = 15
	dwriteFactoryCreateEllipsis   = 20
	dwriteFactoryGetSystemFonts   = 3
	dwriteFontCollectionFind      = 5
	dwriteTextFormatSetAlignment  = 3
	dwriteTextFormatSetParagraph  = 4
	dwriteTextFormatSetWrapping   = 5
	dwriteTextFormatSetTrimming   = 9
)

var (
	d2d1DLL                 = windows.NewLazySystemDLL("d2d1.dll")
	dwriteDLL               = windows.NewLazySystemDLL("dwrite.dll")
	procD2D1CreateFactory   = d2d1DLL.NewProc("D2D1CreateFactory")
	procDWriteCreateFactory = dwriteDLL.NewProc("DWriteCreateFactory")
)

var (
	sharedDWriteOnce    sync.Once
	sharedDWriteFactory unsafe.Pointer
	sharedDWriteErr     error
	fontFamilyCache     sync.Map
)

var defaultTextFontFamilies = [...]string{
	"Microsoft YaHei UI",
	"Microsoft YaHei",
	"Segoe UI",
}

var (
	iidID2D1Factory = windows.GUID{
		Data1: 0x06152247,
		Data2: 0x6f50,
		Data3: 0x465a,
		Data4: [8]byte{0x92, 0x45, 0x11, 0x8b, 0xfd, 0x3b, 0x60, 0x07},
	}
	iidIDWriteFactory = windows.GUID{
		Data1: 0xb859ee5a,
		Data2: 0xd838,
		Data3: 0x4b5b,
		Data4: [8]byte{0xa2, 0xe8, 0x1a, 0xdc, 0x7d, 0x93, 0xdb, 0x48},
	}
)

type d2dPixelFormat struct {
	Format    uint32
	AlphaMode uint32
}

type d2dRenderTargetProperties struct {
	Type        uint32
	PixelFormat d2dPixelFormat
	DPIX        float32
	DPIY        float32
	Usage       uint32
	MinLevel    uint32
}

type d2dColor struct {
	Red   float32
	Green float32
	Blue  float32
	Alpha float32
}

type d2dRectF struct {
	Left   float32
	Top    float32
	Right  float32
	Bottom float32
}

type dwriteTrimming struct {
	Granularity    uint32
	Delimiter      uint32
	DelimiterCount uint32
}

type textMaskRequest struct {
	Value        string
	Width        int
	Height       int
	FontFamilies []string
	FontPixels   float64
	FontWeight   int
	Align        string
}

func drawTextMask(request textMaskRequest) (*image.Alpha, error) {
	mask, resolvedFamily, err := drawDirectWriteTextMaskResolved(request)
	if err == nil {
		return mask, nil
	}
	request.FontFamilies = []string{resolvedFamily}
	return drawGDITextMask(request)
}

func drawDirectWriteTextMask(request textMaskRequest) (*image.Alpha, error) {
	mask, _, err := drawDirectWriteTextMaskResolved(request)
	return mask, err
}

func drawDirectWriteTextMaskResolved(
	request textMaskRequest,
) (*image.Alpha, string, error) {
	resolvedFamily := preferredFallbackFontFamily(request.FontFamilies)
	writeFactory, err := getSharedDWriteFactory()
	if err != nil {
		return nil, resolvedFamily, err
	}
	resolvedFamily, err = resolveDirectWriteFontFamily(
		writeFactory,
		request.FontFamilies,
	)
	if err != nil {
		return nil, resolvedFamily, err
	}
	mask, err := drawDirectWriteTextMaskWithFactory(
		request,
		writeFactory,
		resolvedFamily,
	)
	return mask, resolvedFamily, err
}

func drawDirectWriteTextMaskWithFactory(
	request textMaskRequest,
	writeFactory unsafe.Pointer,
	fontFamily string,
) (*image.Alpha, error) {
	mask := image.NewAlpha(image.Rect(
		0,
		0,
		max(0, request.Width),
		max(0, request.Height),
	))
	if request.Value == "" || request.Width <= 0 || request.Height <= 0 {
		return mask, nil
	}

	memoryDC, pixels, releaseBitmap, err := createTextMaskDIB(
		request.Width,
		request.Height,
	)
	if err != nil {
		return nil, err
	}
	defer releaseBitmap()

	var d2dFactory unsafe.Pointer
	result, _, _ := procD2D1CreateFactory.Call(
		d2dFactoryTypeSingleThreaded,
		uintptr(unsafe.Pointer(&iidID2D1Factory)),
		0,
		uintptr(unsafe.Pointer(&d2dFactory)),
	)
	if err := hresultError("D2D1CreateFactory", result); err != nil {
		return nil, err
	}
	defer releaseCOM(d2dFactory)

	properties := d2dRenderTargetProperties{
		Type: d2dRenderTargetTypeSoftware,
		PixelFormat: d2dPixelFormat{
			Format:    dxgiFormatBGRA8UNorm,
			AlphaMode: d2dAlphaModePremultiplied,
		},
		DPIX: 96,
		DPIY: 96,
	}
	var renderTarget unsafe.Pointer
	result = callCOM(
		d2dFactory,
		d2dFactoryCreateDCRenderTarget,
		uintptr(unsafe.Pointer(&properties)),
		uintptr(unsafe.Pointer(&renderTarget)),
	)
	if err := hresultError("ID2D1Factory.CreateDCRenderTarget", result); err != nil {
		return nil, err
	}
	defer releaseCOM(renderTarget)

	boundRect := winRect{
		Right:  int32(request.Width),
		Bottom: int32(request.Height),
	}
	result = callCOM(
		renderTarget,
		d2dDCRenderTargetBindDC,
		memoryDC,
		uintptr(unsafe.Pointer(&boundRect)),
	)
	if err := hresultError("ID2D1DCRenderTarget.BindDC", result); err != nil {
		return nil, err
	}

	var textFormat unsafe.Pointer
	result = callCOM(
		writeFactory,
		dwriteFactoryCreateTextFormat,
		uintptr(unsafe.Pointer(utf16Pointer(fontFamily))),
		0,
		uintptr(request.FontWeight),
		dwriteFontStyleNormal,
		dwriteFontStretchNormal,
		uintptr(math.Float32bits(float32(request.FontPixels))),
		uintptr(unsafe.Pointer(utf16Pointer("zh-CN"))),
		uintptr(unsafe.Pointer(&textFormat)),
	)
	if err := hresultError("IDWriteFactory.CreateTextFormat", result); err != nil {
		return nil, err
	}
	defer releaseCOM(textFormat)

	textAlignment := uintptr(dwriteTextAlignmentLeading)
	switch request.Align {
	case "center":
		textAlignment = dwriteTextAlignmentCenter
	case "right":
		textAlignment = dwriteTextAlignmentTrailing
	}
	for _, setting := range []struct {
		name  string
		index int
		value uintptr
	}{
		{name: "SetTextAlignment", index: dwriteTextFormatSetAlignment, value: textAlignment},
		{name: "SetParagraphAlignment", index: dwriteTextFormatSetParagraph, value: dwriteParagraphAlignmentCenter},
		{name: "SetWordWrapping", index: dwriteTextFormatSetWrapping, value: dwriteWordWrappingNoWrap},
	} {
		if err := hresultError(setting.name, callCOM(textFormat, setting.index, setting.value)); err != nil {
			return nil, err
		}
	}

	var ellipsis unsafe.Pointer
	result = callCOM(
		writeFactory,
		dwriteFactoryCreateEllipsis,
		uintptr(textFormat),
		uintptr(unsafe.Pointer(&ellipsis)),
	)
	if err := hresultError("IDWriteFactory.CreateEllipsisTrimmingSign", result); err != nil {
		return nil, err
	}
	defer releaseCOM(ellipsis)
	trimming := dwriteTrimming{Granularity: dwriteTrimmingCharacter}
	result = callCOM(
		textFormat,
		dwriteTextFormatSetTrimming,
		uintptr(unsafe.Pointer(&trimming)),
		uintptr(ellipsis),
	)
	if err := hresultError("IDWriteTextFormat.SetTrimming", result); err != nil {
		return nil, err
	}

	white := d2dColor{Red: 1, Green: 1, Blue: 1, Alpha: 1}
	var brush unsafe.Pointer
	result = callCOM(
		renderTarget,
		d2dRenderTargetCreateBrush,
		uintptr(unsafe.Pointer(&white)),
		0,
		uintptr(unsafe.Pointer(&brush)),
	)
	if err := hresultError("ID2D1RenderTarget.CreateSolidColorBrush", result); err != nil {
		return nil, err
	}
	defer releaseCOM(brush)

	encoded, err := windows.UTF16FromString(request.Value)
	if err != nil {
		return nil, fmt.Errorf("encoding text: %w", err)
	}
	drawRect := d2dRectF{
		Right:  float32(request.Width),
		Bottom: float32(request.Height),
	}
	callCOM(renderTarget, d2dRenderTargetSetTextAA, d2dTextAntialiasGrayscale)
	callCOM(renderTarget, d2dRenderTargetBeginDraw)
	transparent := d2dColor{}
	callCOM(renderTarget, d2dRenderTargetClear, uintptr(unsafe.Pointer(&transparent)))
	callCOM(
		renderTarget,
		d2dRenderTargetDrawText,
		uintptr(unsafe.Pointer(&encoded[0])),
		uintptr(len(encoded)-1),
		uintptr(textFormat),
		uintptr(unsafe.Pointer(&drawRect)),
		uintptr(brush),
		d2dDrawTextOptionsClip,
		dwriteMeasuringModeNatural,
	)
	result = callCOM(renderTarget, d2dRenderTargetEndDraw, 0, 0)
	if err := hresultError("ID2D1RenderTarget.EndDraw", result); err != nil {
		return nil, err
	}

	for y := range request.Height {
		for x := range request.Width {
			offset := (y*request.Width + x) * 4
			mask.SetAlpha(x, y, color.Alpha{A: pixels[offset+3]})
		}
	}
	return mask, nil
}

func getSharedDWriteFactory() (unsafe.Pointer, error) {
	sharedDWriteOnce.Do(func() {
		result, _, _ := procDWriteCreateFactory.Call(
			dwriteFactoryTypeShared,
			uintptr(unsafe.Pointer(&iidIDWriteFactory)),
			uintptr(unsafe.Pointer(&sharedDWriteFactory)),
		)
		sharedDWriteErr = hresultError("DWriteCreateFactory", result)
	})
	return sharedDWriteFactory, sharedDWriteErr
}

func resolveDirectWriteFontFamily(
	writeFactory unsafe.Pointer,
	candidates []string,
) (string, error) {
	cacheKey := strings.Join(candidates, "\x00")
	if cached, ok := fontFamilyCache.Load(cacheKey); ok {
		return cached.(string), nil
	}
	var collection unsafe.Pointer
	result := callCOM(
		writeFactory,
		dwriteFactoryGetSystemFonts,
		uintptr(unsafe.Pointer(&collection)),
		0,
	)
	if err := hresultError("IDWriteFactory.GetSystemFontCollection", result); err != nil {
		return preferredFallbackFontFamily(candidates), err
	}
	defer releaseCOM(collection)

	allCandidates := make([]string, 0, len(candidates)+len(defaultTextFontFamilies))
	allCandidates = append(allCandidates, candidates...)
	allCandidates = append(allCandidates, defaultTextFontFamilies[:]...)
	seen := make(map[string]struct{}, len(allCandidates))
	for _, candidate := range allCandidates {
		family := normalizedFontFamily(candidate)
		if family == "" || isGenericFontFamily(family) {
			continue
		}
		key := strings.ToLower(family)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}

		var index uint32
		var exists int32
		result = callCOM(
			collection,
			dwriteFontCollectionFind,
			uintptr(unsafe.Pointer(utf16Pointer(family))),
			uintptr(unsafe.Pointer(&index)),
			uintptr(unsafe.Pointer(&exists)),
		)
		if err := hresultError("IDWriteFontCollection.FindFamilyName", result); err != nil {
			return preferredFallbackFontFamily(candidates), err
		}
		if exists != 0 {
			fontFamilyCache.Store(cacheKey, family)
			return family, nil
		}
	}
	return "", fmt.Errorf("none of the requested text fonts are installed")
}

func preferredFallbackFontFamily(candidates []string) string {
	for _, candidate := range candidates {
		family := normalizedFontFamily(candidate)
		if family != "" && !isGenericFontFamily(family) {
			return family
		}
	}
	return defaultTextFontFamilies[0]
}

func normalizedFontFamily(value string) string {
	return strings.Trim(strings.TrimSpace(value), "\"'")
}

func isGenericFontFamily(value string) bool {
	switch strings.ToLower(value) {
	case "sans-serif", "serif", "monospace", "system-ui", "ui-sans-serif":
		return true
	default:
		return false
	}
}

func createTextMaskDIB(
	width int,
	height int,
) (uintptr, []byte, func(), error) {
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return 0, nil, nil, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memoryDC, _, lastErr := procCreateCompatibleDC.Call(screenDC)
	if memoryDC == 0 {
		return 0, nil, nil, callError("CreateCompatibleDC", lastErr)
	}
	info := bitmapInfo{Header: bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(width),
		Height:      -int32(height),
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
		procDeleteDC.Call(memoryDC)
		return 0, nil, nil, callError("CreateDIBSection", lastErr)
	}
	previousBitmap, _, _ := procSelectObject.Call(memoryDC, bitmap)
	pixels := unsafe.Slice((*byte)(pixelAddress), width*height*4)
	clear(pixels)
	release := func() {
		if previousBitmap != 0 {
			procSelectObject.Call(memoryDC, previousBitmap)
		}
		procDeleteObject.Call(bitmap)
		procDeleteDC.Call(memoryDC)
	}
	return memoryDC, pixels, release, nil
}

func callCOM(object unsafe.Pointer, method int, args ...uintptr) uintptr {
	vtable := *(*unsafe.Pointer)(object)
	methodAddress := *(*uintptr)(unsafe.Add(
		vtable,
		uintptr(method)*unsafe.Sizeof(uintptr(0)),
	))
	callArgs := make([]uintptr, 0, len(args)+1)
	callArgs = append(callArgs, uintptr(object))
	callArgs = append(callArgs, args...)
	result, _, _ := syscall.SyscallN(methodAddress, callArgs...)
	return result
}

func releaseCOM(object unsafe.Pointer) {
	if object != nil {
		callCOM(object, 2)
	}
}

func hresultError(name string, result uintptr) error {
	if int32(result) >= 0 {
		return nil
	}
	return fmt.Errorf("%s failed with HRESULT 0x%08x", name, uint32(result))
}
