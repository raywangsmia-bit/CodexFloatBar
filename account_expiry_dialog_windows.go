//go:build windows

package main

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	accountExpiryDialogClass       = "CodexFloatingBar.AccountExpiryDialog"
	accountDialogWMCommand         = 0x0111
	accountDialogIDOK              = 1
	accountDialogIDCancel          = 2
	accountDialogVKReturn          = 0x0D
	accountDialogWSVisible         = 0x10000000
	accountDialogWSChild           = 0x40000000
	accountDialogWSCaption         = 0x00C00000
	accountDialogWSSysMenu         = 0x00080000
	accountDialogWSTabStop         = 0x00010000
	accountDialogWSBorder          = 0x00800000
	accountDialogESAutoHScroll     = 0x0080
	accountDialogBSDefPushButton   = 0x0001
	accountDialogWSExDlgModalFrame = 0x00000001
	accountDialogWSExClientEdge    = 0x00000200
	accountDialogWMSetFont         = 0x0030
	accountDialogEMSetLimitText    = 0x00C5
	accountDialogEMSetCueBanner    = 0x1501
	accountDialogDefaultGUIFont    = 17
)

type accountExpiryDialogState struct {
	edit     uintptr
	value    string
	accepted bool
}

var (
	accountExpiryDialogOnce  sync.Once
	accountExpiryDialogErr   error
	accountExpiryDialogs     sync.Map
	accountExpiryDialogProc  = windows.NewCallback(accountExpiryDialogWindowProc)
	procEnableWindow         = user32.NewProc("EnableWindow")
	procSetFocus             = user32.NewProc("SetFocus")
	procIsDialogMessageW     = user32.NewProc("IsDialogMessageW")
	procGetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procGetStockObject       = gdi32.NewProc("GetStockObject")
)

func promptAccountExpiryDate(owner uintptr, initial string) (string, bool, error) {
	if err := registerAccountExpiryDialogClass(); err != nil {
		return "", false, err
	}
	instance, _, lastErr := procGetModuleHandleW.Call(0)
	if instance == 0 {
		return "", false, callError("GetModuleHandleW", lastErr)
	}
	dpi := systemDPI()
	scale := func(value int) int32 {
		return int32((value*int(max(uint32(96), dpi)) + 48) / 96)
	}
	width := scale(460)
	height := scale(185)
	x, y := centerDialogPosition(owner, width, height)
	dialog, _, lastErr := procCreateWindowExW.Call(
		accountDialogWSExDlgModalFrame,
		uintptr(unsafe.Pointer(utf16Pointer(accountExpiryDialogClass))),
		uintptr(unsafe.Pointer(utf16Pointer("设置账号到期日期"))),
		accountDialogWSCaption|accountDialogWSSysMenu,
		signed(x),
		signed(y),
		signed(width),
		signed(height),
		owner,
		0,
		instance,
		0,
	)
	if dialog == 0 {
		return "", false, callError("CreateWindowExW(account expiry dialog)", lastErr)
	}
	state := &accountExpiryDialogState{}
	accountExpiryDialogs.Store(dialog, state)
	defer accountExpiryDialogs.Delete(dialog)
	defer func() {
		valid, _, _ := procIsWindow.Call(dialog)
		if valid != 0 {
			procDestroyWindow.Call(dialog)
		}
	}()

	font, _, _ := procGetStockObject.Call(accountDialogDefaultGUIFont)
	label, err := createAccountExpiryDialogControl(
		dialog,
		instance,
		"STATIC",
		"格式：YYYY-MM-DD（例如 2026-08-31），留空可清除：",
		0,
		accountDialogWSChild|accountDialogWSVisible,
		24, 22, 410, 22, 0, scale,
	)
	if err != nil {
		return "", false, err
	}
	edit, err := createAccountExpiryDialogControl(
		dialog,
		instance,
		"EDIT",
		initial,
		accountDialogWSExClientEdge,
		accountDialogWSChild|accountDialogWSVisible|accountDialogWSTabStop|
			accountDialogWSBorder|accountDialogESAutoHScroll,
		24, 52, 410, 28, 100, scale,
	)
	if err != nil {
		return "", false, err
	}
	state.edit = edit
	okButton, err := createAccountExpiryDialogControl(
		dialog,
		instance,
		"BUTTON",
		"确定",
		0,
		accountDialogWSChild|accountDialogWSVisible|accountDialogWSTabStop|
			accountDialogBSDefPushButton,
		264, 104, 80, 30, accountDialogIDOK, scale,
	)
	if err != nil {
		return "", false, err
	}
	cancelButton, err := createAccountExpiryDialogControl(
		dialog,
		instance,
		"BUTTON",
		"取消",
		0,
		accountDialogWSChild|accountDialogWSVisible|accountDialogWSTabStop,
		354, 104, 80, 30, accountDialogIDCancel, scale,
	)
	if err != nil {
		return "", false, err
	}
	for _, control := range []uintptr{label, edit, okButton, cancelButton} {
		procSendMessageW.Call(control, accountDialogWMSetFont, font, 1)
	}
	procSendMessageW.Call(edit, accountDialogEMSetLimitText, 10, 0)
	cue := utf16Pointer("YYYY-MM-DD，例如 2026-08-31")
	procSendMessageW.Call(
		edit,
		accountDialogEMSetCueBanner,
		1,
		uintptr(unsafe.Pointer(cue)),
	)

	if owner != 0 {
		procEnableWindow.Call(owner, 0)
		defer func() {
			procEnableWindow.Call(owner, 1)
			procSetForegroundWindow.Call(owner)
		}()
	}
	procShowWindow.Call(dialog, swShow)
	procSetForegroundWindow.Call(dialog)
	procSetFocus.Call(edit)

	var message winMessage
	for {
		valid, _, _ := procIsWindow.Call(dialog)
		if valid == 0 {
			break
		}
		result, _, lastErr := procGetMessageW.Call(
			uintptr(unsafe.Pointer(&message)),
			0,
			0,
			0,
		)
		if int32(result) == -1 {
			return "", false, callError("GetMessageW(account expiry dialog)", lastErr)
		}
		if result == 0 {
			procPostQuitMessage.Call(message.WParam)
			return "", false, nil
		}
		if message.Message == wmKeyDown {
			switch message.WParam {
			case accountDialogVKReturn:
				acceptAccountExpiryDialog(dialog, state)
				continue
			case vkEscape:
				procDestroyWindow.Call(dialog)
				continue
			}
		}
		handled, _, _ := procIsDialogMessageW.Call(
			dialog,
			uintptr(unsafe.Pointer(&message)),
		)
		if handled == 0 {
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
		}
	}
	return state.value, state.accepted, nil
}

func registerAccountExpiryDialogClass() error {
	accountExpiryDialogOnce.Do(func() {
		instance, _, lastErr := procGetModuleHandleW.Call(0)
		if instance == 0 {
			accountExpiryDialogErr = callError("GetModuleHandleW", lastErr)
			return
		}
		cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
		className := utf16Pointer(accountExpiryDialogClass)
		class := windowClassEx{
			Size:            uint32(unsafe.Sizeof(windowClassEx{})),
			WindowProc:      accountExpiryDialogProc,
			Instance:        instance,
			Cursor:          cursor,
			BackgroundBrush: colorWindow + 1,
			ClassName:       className,
		}
		result, _, lastErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
		if result == 0 {
			accountExpiryDialogErr = callError(
				"RegisterClassExW(account expiry dialog)",
				lastErr,
			)
		}
	})
	return accountExpiryDialogErr
}

func createAccountExpiryDialogControl(
	parent uintptr,
	instance uintptr,
	className string,
	text string,
	extendedStyle uintptr,
	style uintptr,
	x int,
	y int,
	width int,
	height int,
	id uintptr,
	scale func(int) int32,
) (uintptr, error) {
	control, _, lastErr := procCreateWindowExW.Call(
		extendedStyle,
		uintptr(unsafe.Pointer(utf16Pointer(className))),
		uintptr(unsafe.Pointer(utf16Pointer(text))),
		style,
		signed(scale(x)),
		signed(scale(y)),
		signed(scale(width)),
		signed(scale(height)),
		parent,
		id,
		instance,
		0,
	)
	if control == 0 {
		return 0, callError(fmt.Sprintf("CreateWindowExW(%s)", className), lastErr)
	}
	return control, nil
}

func centerDialogPosition(owner uintptr, width int32, height int32) (int32, int32) {
	var ownerRect winRect
	if owner != 0 {
		if result, _, _ := procGetWindowRect.Call(
			owner,
			uintptr(unsafe.Pointer(&ownerRect)),
		); result != 0 {
			return ownerRect.Left + (ownerRect.Right-ownerRect.Left-width)/2,
				ownerRect.Top + (ownerRect.Bottom-ownerRect.Top-height)/2
		}
	}
	screenWidth, _, _ := procGetSystemMetrics.Call(smCXScreen)
	screenHeight, _, _ := procGetSystemMetrics.Call(smCYScreen)
	return (int32(screenWidth) - width) / 2, (int32(screenHeight) - height) / 2
}

func acceptAccountExpiryDialog(dialog uintptr, state *accountExpiryDialogState) {
	if state == nil || state.edit == 0 {
		return
	}
	length, _, _ := procGetWindowTextLengthW.Call(state.edit)
	buffer := make([]uint16, int(length)+1)
	if len(buffer) > 0 {
		procGetWindowTextW.Call(
			state.edit,
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(len(buffer)),
		)
	}
	state.value = windows.UTF16ToString(buffer)
	state.accepted = true
	procDestroyWindow.Call(dialog)
}

func accountExpiryDialogWindowProc(
	window uintptr,
	message uint32,
	wParam uintptr,
	lParam uintptr,
) uintptr {
	switch message {
	case accountDialogWMCommand:
		stateValue, ok := accountExpiryDialogs.Load(window)
		if !ok {
			break
		}
		state := stateValue.(*accountExpiryDialogState)
		switch wParam & 0xffff {
		case accountDialogIDOK:
			acceptAccountExpiryDialog(window, state)
			return 0
		case accountDialogIDCancel:
			procDestroyWindow.Call(window)
			return 0
		}
	case wmClose:
		procDestroyWindow.Call(window)
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(window, uintptr(message), wParam, lParam)
	return result
}
