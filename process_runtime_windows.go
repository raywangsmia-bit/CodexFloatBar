//go:build windows

package main

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/codexprocess"
)

type processRuntime struct {
	monitor   *codexprocess.Monitor
	pending   atomic.Pointer[codexprocess.Status]
	cancel    context.CancelFunc
	startOnce sync.Once
	stopOnce  sync.Once
	disabled  bool
}

type codexVisibilityAction uint8

const (
	codexVisibilityUnchanged codexVisibilityAction = iota
	codexVisibilityShow
	codexVisibilityHide
)

func codexProcessVisibilityAction(
	stateKnown bool,
	wasAvailable bool,
	available bool,
	manuallyHidden bool,
) codexVisibilityAction {
	if !stateKnown {
		if available && !manuallyHidden {
			return codexVisibilityShow
		}
		return codexVisibilityHide
	}
	if available {
		if !wasAvailable && !manuallyHidden {
			return codexVisibilityShow
		}
		return codexVisibilityUnchanged
	}
	if wasAvailable {
		return codexVisibilityHide
	}
	return codexVisibilityUnchanged
}

func newProcessRuntime() *processRuntime {
	return &processRuntime{monitor: codexprocess.NewMonitor(codexprocess.Options{})}
}

func (runtime *processRuntime) disable() {
	if runtime != nil {
		runtime.disabled = true
	}
}

func (runtime *processRuntime) start(window uintptr) {
	if runtime == nil || runtime.disabled || runtime.monitor == nil {
		return
	}
	runtime.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		runtime.cancel = cancel
		go runtime.forwardUpdates(window)
		go func() {
			err := runtime.monitor.Run(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("Codex process monitor stopped: %v", err)
			}
		}()
	})
}

func (runtime *processRuntime) forwardUpdates(window uintptr) {
	for status := range runtime.monitor.Updates() {
		latest := status
		runtime.pending.Store(&latest)
		posted, _, lastErr := procPostMessageW.Call(window, wmNativeCodexChanged, 0, 0)
		if posted == 0 {
			log.Printf("posting Codex process update: %v", lastErr)
		}
	}
}

func (runtime *processRuntime) acceptPending() (codexprocess.Status, bool) {
	if runtime == nil {
		return codexprocess.Status{}, false
	}
	status := runtime.pending.Swap(nil)
	if status == nil {
		return codexprocess.Status{}, false
	}
	return *status, true
}

func (runtime *processRuntime) stop() {
	if runtime == nil {
		return
	}
	runtime.stopOnce.Do(func() {
		if runtime.cancel != nil {
			runtime.cancel()
		}
	})
}

func (app *nativeApp) startProcessMonitor() {
	if app.process != nil {
		app.process.start(app.window)
	}
}

func (app *nativeApp) stopProcessMonitor() {
	if app.process != nil {
		app.process.stop()
	}
}
