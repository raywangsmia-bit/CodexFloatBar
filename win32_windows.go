//go:build windows

package main

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	csVRedraw = 0x0001
	csHRedraw = 0x0002

	wsPopup = 0x80000000

	wsExTopmost    = 0x00000008
	wsExAppWindow  = 0x00040000
	wsExToolWindow = 0x00000080
	wsExLayered    = 0x00080000
	wsExNoActivate = 0x08000000

	swHide           = 0
	swShowNoActivate = 4
	swShow           = 5
	swRestore        = 9

	wmDestroy          = 0x0002
	wmNull             = 0x0000
	wmContextMenu      = 0x007B
	wmNCDestroy        = 0x0082
	wmClose            = 0x0010
	wmNCHitTest        = 0x0084
	wmNCMouseMove      = 0x00A0
	wmNCRButtonUp      = 0x00A5
	wmKeyDown          = 0x0100
	wmTimer            = 0x0113
	wmMouseMove        = 0x0200
	wmLButtonUp        = 0x0202
	wmRButtonUp        = 0x0205
	wmLButtonDblClk    = 0x0203
	wmWindowPosChanged = 0x0047
	wmEnterSizeMove    = 0x0231
	wmExitSizeMove     = 0x0232
	wmDPIChanged       = 0x02E0
	wmApp              = 0x8000
	wmUser             = 0x0400
	ninSelect          = wmUser
	ninKeySelect       = wmUser + 1

	wmNativeReload           = wmApp + 1
	wmNativeTray             = wmApp + 2
	wmNativeShow             = wmApp + 3
	wmNativeInitialize       = wmApp + 4
	wmNativeSelfTestComplete = wmApp + 5
	wmNativeStatusChanged    = wmApp + 6
	wmNativeCodexChanged     = wmApp + 7

	htClient                  = 1
	htCaption                 = 2
	idcArrow                  = 32512
	applicationIconResourceID = 101
	vkEscape                  = 0x1B
	smCXScreen                = 0
	smCYScreen                = 1
	colorWindow               = 5

	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
	swpNoSize     = 0x0001

	biRGB               = 0
	dibRGBColors        = 0
	ulwAlpha            = 0x00000002
	acSrcAlpha          = 0x01
	acSrcOver           = 0x00
	bkModeTransparent   = 1
	drawTextCenter      = 0x00000001
	drawTextRight       = 0x00000002
	drawTextVCenter     = 0x00000004
	drawTextSingleLine  = 0x00000020
	drawTextNoPrefix    = 0x00000800
	drawTextEndEllipsis = 0x00008000
	defaultCharset      = 1
	antialiasedQuality  = 4

	nimAdd         = 0x00000000
	nimModify      = 0x00000001
	nimDelete      = 0x00000002
	nimSetFocus    = 0x00000003
	nimSetVersion  = 0x00000004
	nifMessage     = 0x00000001
	nifIcon        = 0x00000002
	nifTip         = 0x00000004
	nifInfo        = 0x00000010
	notifyVersion4 = 4
	niifInfo       = 0x00000001

	mfString       = 0x00000000
	mfGrayed       = 0x00000001
	mfDisabled     = 0x00000002
	mfChecked      = 0x00000008
	mfPopup        = 0x00000010
	mfSeparator    = 0x00000800
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	trayCommandRefreshStatus    = 1001
	trayCommandCopyStatus       = 1002
	trayCommandVisible          = 1003
	trayCommandOpenConfig       = 1004
	trayCommandOpenChatGPT      = 1005
	trayCommandOpenBilling      = 1006
	trayCommandOpenAPIUsage     = 1007
	trayCommandOpenAPIKeys      = 1008
	trayCommandOpenGitHub       = 1009
	trayCommandThemeDark        = 1010
	trayCommandThemeLight       = 1011
	trayCommandLayoutHorizontal = 1012
	trayCommandLayoutVertical   = 1013
	trayCommandScale90          = 1014
	trayCommandScale100         = 1015
	trayCommandScale110         = 1016
	trayCommandAutoCollapse     = 1017
	trayCommandStartup          = 1018
	trayCommandExit             = 1019
	trayCommandReloadUI         = 1020
	trayCommandFollowCodex      = 1021

	grGDIObjects  = 0
	grUserObjects = 1
)

type winPoint struct {
	X int32
	Y int32
}

type winSize struct {
	CX int32
	CY int32
}

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type winMessage struct {
	Window  uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   winPoint
	Private uint32
}

type windowClassEx struct {
	Size            uint32
	Style           uint32
	WindowProc      uintptr
	ClassExtra      int32
	WindowExtra     int32
	Instance        uintptr
	Icon            uintptr
	Cursor          uintptr
	BackgroundBrush uintptr
	MenuName        *uint16
	ClassName       *uint16
	SmallIcon       uintptr
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	ImageSize     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ColorsUsed    uint32
	ColorsNeeded  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
}

type blendFunction struct {
	Operation     byte
	Flags         byte
	ConstantAlpha byte
	AlphaFormat   byte
}

type notifyIconData struct {
	Size            uint32
	Window          uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	ItemGUID        windows.GUID
	BalloonIcon     uintptr
}

type monitorInfo struct {
	Size    uint32
	Monitor winRect
	Work    winRect
	Flags   uint32
}

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procShowWindow                    = user32.NewProc("ShowWindow")
	procIsWindow                      = user32.NewProc("IsWindow")
	procIsWindowVisible               = user32.NewProc("IsWindowVisible")
	procSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procTranslateMessage              = user32.NewProc("TranslateMessage")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procPostMessageW                  = user32.NewProc("PostMessageW")
	procSendMessageW                  = user32.NewProc("SendMessageW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procReleaseCapture                = user32.NewProc("ReleaseCapture")
	procLoadCursorW                   = user32.NewProc("LoadCursorW")
	procLoadIconW                     = user32.NewProc("LoadIconW")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procGetWindowRect                 = user32.NewProc("GetWindowRect")
	procSetWindowPos                  = user32.NewProc("SetWindowPos")
	procGetDC                         = user32.NewProc("GetDC")
	procReleaseDC                     = user32.NewProc("ReleaseDC")
	procUpdateLayeredWindow           = user32.NewProc("UpdateLayeredWindow")
	procGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
	procGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procFindWindowW                   = user32.NewProc("FindWindowW")
	procRegisterWindowMessageW        = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu               = user32.NewProc("CreatePopupMenu")
	procAppendMenuW                   = user32.NewProc("AppendMenuW")
	procTrackPopupMenu                = user32.NewProc("TrackPopupMenu")
	procDestroyMenu                   = user32.NewProc("DestroyMenu")
	procGetCursorPos                  = user32.NewProc("GetCursorPos")
	procSetTimer                      = user32.NewProc("SetTimer")
	procKillTimer                     = user32.NewProc("KillTimer")
	procMonitorFromWindow             = user32.NewProc("MonitorFromWindow")
	procEnumDisplayMonitors           = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	procGetGuiResources               = user32.NewProc("GetGuiResources")
	procGetWindowThreadProcessID      = user32.NewProc("GetWindowThreadProcessId")
	procDrawTextW                     = user32.NewProc("DrawTextW")

	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procCreateFontW        = gdi32.NewProc("CreateFontW")
	procSetTextColor       = gdi32.NewProc("SetTextColor")
	procSetBkMode          = gdi32.NewProc("SetBkMode")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

var monitorCollector = struct {
	sync.Mutex
	areas []workArea
}{}

var monitorEnumCallback = windows.NewCallback(func(
	monitor uintptr,
	_ uintptr,
	_ uintptr,
	_ uintptr,
) uintptr {
	info := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	result, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		return 1
	}
	monitorCollector.areas = append(monitorCollector.areas, workArea{
		X:      int(info.Work.Left),
		Y:      int(info.Work.Top),
		Width:  int(info.Work.Right - info.Work.Left),
		Height: int(info.Work.Bottom - info.Work.Top),
	})
	return 1
})

func enumerateWorkAreas() []workArea {
	monitorCollector.Lock()
	defer monitorCollector.Unlock()

	monitorCollector.areas = []workArea{}
	procEnumDisplayMonitors.Call(0, 0, monitorEnumCallback, 0)
	return append([]workArea{}, monitorCollector.areas...)
}

func utf16Pointer(value string) *uint16 {
	pointer, err := windows.UTF16PtrFromString(value)
	if err != nil {
		panic(err)
	}
	return pointer
}

func callError(name string, lastErr error) error {
	if lastErr == nil || lastErr == syscall.Errno(0) {
		return fmt.Errorf("%s failed", name)
	}
	return fmt.Errorf("%s failed: %w", name, lastErr)
}

func signed(value int32) uintptr {
	return uintptr(int64(value))
}
