//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

type winEventRange struct {
	Minimum uint32
	Maximum uint32
}

var occlusionWinEventCallback = windows.NewCallback(func(
	_ uintptr,
	event uintptr,
	window uintptr,
	objectID uintptr,
	childID uintptr,
	_ uintptr,
	_ uintptr,
) uintptr {
	if window == 0 {
		return 0
	}
	objectEvent := uint32(event) >= eventObjectDestroy
	wrongObject := int32(objectID) != objidWindow || int32(childID) != 0
	if objectEvent && wrongObject {
		return 0
	}
	if app := activeNativeApp; app != nil {
		app.queueOcclusionRefresh()
	}
	return 0
})

func (app *nativeApp) startOcclusionHooks() error {
	ranges := []winEventRange{
		{Minimum: eventSystemForeground, Maximum: eventSystemForeground},
		{Minimum: eventSystemMinimizeStart, Maximum: eventSystemMinimizeEnd},
		{Minimum: eventObjectDestroy, Maximum: eventObjectReorder},
		{Minimum: eventObjectLocationChange, Maximum: eventObjectLocationChange},
	}
	flags := uintptr(wineventOutOfContext | wineventSkipOwnProcess)
	failed := 0
	for _, eventRange := range ranges {
		hook, _, _ := procSetWinEventHook.Call(
			uintptr(eventRange.Minimum),
			uintptr(eventRange.Maximum),
			0,
			occlusionWinEventCallback,
			0,
			0,
			flags,
		)
		if hook == 0 {
			failed++
			continue
		}
		app.winEventHooks = append(app.winEventHooks, hook)
	}
	if failed == 0 {
		return nil
	}
	return fmt.Errorf(
		"registering %d of %d WinEvent hooks",
		failed,
		len(ranges),
	)
}

func (app *nativeApp) stopOcclusionHooks() {
	for _, hook := range app.winEventHooks {
		procUnhookWinEvent.Call(hook)
	}
	app.winEventHooks = []uintptr{}
	app.occlusionEventPending.Store(false)
}

func (app *nativeApp) queueOcclusionRefresh() {
	if app.window == 0 || !app.occlusionEventPending.CompareAndSwap(false, true) {
		return
	}
	posted, _, _ := procPostMessageW.Call(
		app.window,
		wmNativeOcclusionChanged,
		0,
		0,
	)
	if posted == 0 {
		app.occlusionEventPending.Store(false)
	}
}

func (app *nativeApp) refreshCodexOcclusion() {
	app.occlusionEventPending.Store(false)
	if app.process != nil {
		app.process.refresh()
	}
}
