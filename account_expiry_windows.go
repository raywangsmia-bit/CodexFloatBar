//go:build windows

package main

import (
	"log"
	"time"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/appsettings"
)

func (app *nativeApp) editAccountExpiryDate() {
	value := app.accountExpiryDate
	for {
		entered, accepted, err := promptAccountExpiryDate(app.window, value)
		if err != nil {
			app.reportTrayError("设置账号到期日期", err)
			return
		}
		if !accepted {
			return
		}
		normalized, valid := appsettings.NormalizeAccountExpiryDate(entered)
		if !valid {
			showSystemActionError(
				app.window,
				"请输入有效日期，格式为 YYYY-MM-DD，例如 2026-09-01。",
			)
			value = entered
			continue
		}
		app.accountExpiryDate = normalized
		app.accountExpiryLastReminderHour = time.Time{}
		if app.appearance != nil {
			app.appearance.setAccountExpiryDate(normalized)
			if err := app.appearance.save(); err != nil {
				app.reportTrayError("保存账号到期日期", err)
			}
		}
		app.scheduleAccountExpiryReminder(time.Now())
		return
	}
}

func (app *nativeApp) toggleAccountExpiryReminder() {
	app.accountExpiryReminderEnabled = !app.accountExpiryReminderEnabled
	app.accountExpiryLastReminderHour = time.Time{}
	if app.appearance != nil {
		app.appearance.setAccountExpiryReminder(app.accountExpiryReminderEnabled)
		if err := app.appearance.save(); err != nil {
			app.reportTrayError("保存到期提醒设置", err)
		}
	}
	app.scheduleAccountExpiryReminder(time.Now())
}

func (app *nativeApp) scheduleAccountExpiryReminder(now time.Time) {
	if app.window != 0 {
		procKillTimer.Call(app.window, accountExpiryReminderTimerID)
	}
	app.accountExpiryScheduledHour = time.Time{}
	if app.window == 0 || !app.accountExpiryReminderEnabled || app.accountExpiryDate == "" {
		return
	}
	if _, valid := appsettings.NormalizeAccountExpiryDate(app.accountExpiryDate); !valid {
		return
	}
	next := nextAccountExpiryReminderHour(now)
	delay := next.Sub(now)
	milliseconds := (delay + time.Millisecond - 1) / time.Millisecond
	if milliseconds < 1 {
		milliseconds = 1
	}
	if !setNativeTimer(
		app.window,
		accountExpiryReminderTimerID,
		uint32(milliseconds),
	) {
		return
	}
	app.accountExpiryScheduledHour = next
}

func (app *nativeApp) handleAccountExpiryReminderTimer(now time.Time) {
	procKillTimer.Call(app.window, accountExpiryReminderTimerID)
	scheduled := app.accountExpiryScheduledHour
	app.accountExpiryScheduledHour = time.Time{}
	if accountExpiryReminderDue(
		now,
		app.accountExpiryDate,
		app.accountExpiryReminderEnabled,
		scheduled,
		app.accountExpiryLastReminderHour,
	) {
		app.accountExpiryLastReminderHour = scheduled
		app.showAccountExpiryToast()
	}
	app.scheduleAccountExpiryReminder(now)
}

func (app *nativeApp) showAccountExpiryToast() {
	if err := app.ensureAuxiliaryWindow(&app.usageToastWindow); err != nil {
		log.Printf("showing account expiry toast: %v", err)
		return
	}
	manifest, err := readManifest(app.bundleRoot)
	if err != nil {
		log.Printf("loading account expiry toast manifest: %v", err)
		return
	}
	theme := appsettings.ThemeDark
	if app.appearance != nil {
		theme = app.appearance.current.Theme
	}
	surfaceID := resolveUsageToastSurfaceID(manifest, theme, quotaToneWarn)
	presentation := app.accountExpiryToastPresentation()
	surface, err := app.loadComposedSurfaceWithPresentation(
		manifest,
		surfaceID,
		app.windowDPI(app.usageToastWindow.Handle),
		&presentation,
	)
	if err != nil {
		log.Printf("composing account expiry toast: %v", err)
		return
	}
	position := app.currentWindowPosition(app.usageToastWindow.Handle)
	if err := updateLayeredWindow(
		app.usageToastWindow.Handle,
		surface.Image,
		position,
	); err != nil {
		log.Printf("updating account expiry toast: %v", err)
		return
	}
	app.usageToastWindow.CurrentSurface = surface
	app.accountExpiryToastActive = true
	app.displayUsageToast()
}

func (app *nativeApp) accountExpiryToastPresentation() uiPresentation {
	var presentation uiPresentation
	if app.status != nil {
		presentation = app.status.currentPresentation()
	}
	text := make(map[string]string, len(presentation.Text)+2)
	for key, value := range presentation.Text {
		text[key] = value
	}
	text["toast.title"] = "明日账号到期提醒"
	text["toast.message"] = accountExpiryReminderMessage(app.accountExpiryDate)
	presentation.Text = text
	presentation.Tone = quotaToneWarn
	return presentation
}

func (app *nativeApp) restoreAccountExpiryToast() {
	if !app.accountExpiryToastActive || app.usageToastWindow.Handle == 0 {
		return
	}
	manifest, err := readManifest(app.bundleRoot)
	if err != nil {
		log.Printf("restoring usage toast manifest: %v", err)
		return
	}
	theme := appsettings.ThemeDark
	if app.appearance != nil {
		theme = app.appearance.current.Theme
	}
	tone := quotaToneOffline
	if app.status != nil {
		tone = app.status.tone()
	}
	surfaceID := resolveUsageToastSurfaceID(manifest, theme, tone)
	surface, err := app.loadComposedSurface(
		manifest,
		surfaceID,
		app.windowDPI(app.usageToastWindow.Handle),
	)
	if err != nil {
		log.Printf("restoring usage toast: %v", err)
		return
	}
	position := app.currentWindowPosition(app.usageToastWindow.Handle)
	if err := updateLayeredWindow(
		app.usageToastWindow.Handle,
		surface.Image,
		position,
	); err != nil {
		log.Printf("updating restored usage toast: %v", err)
		return
	}
	app.usageToastWindow.SurfaceID = surfaceID
	app.usageToastWindow.CurrentSurface = surface
	app.accountExpiryToastActive = false
}
