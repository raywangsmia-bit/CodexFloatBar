//go:build windows

package main

import "testing"

func TestAccountExpiryToastPresentationUsesReminderCopy(t *testing.T) {
	app := &nativeApp{
		accountExpiryDate: "2026-09-01",
		status:            &statusRuntime{},
	}
	presentation := app.accountExpiryToastPresentation()
	if presentation.Text["toast.title"] != "明日账号到期提醒" {
		t.Fatalf("toast title = %q", presentation.Text["toast.title"])
	}
	if presentation.Text["toast.message"] !=
		"账号将于 2026-09-01 到期，请及时续费。" {
		t.Fatalf("toast message = %q", presentation.Text["toast.message"])
	}
	if presentation.Tone != quotaToneWarn {
		t.Fatalf("toast tone = %q", presentation.Tone)
	}
}
