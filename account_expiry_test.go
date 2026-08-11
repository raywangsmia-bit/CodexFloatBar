package main

import (
	"testing"
	"time"
)

func TestAccountExpiryReminderScheduleAndEligibility(t *testing.T) {
	location := time.FixedZone("UTC+05:30", 5*60*60+30*60)
	now := time.Date(2026, 8, 11, 10, 34, 56, 0, location)
	next := nextAccountExpiryReminderHour(now)
	wantNext := time.Date(2026, 8, 11, 11, 0, 0, 0, location)
	if !next.Equal(wantNext) {
		t.Fatalf("next reminder = %s, want %s", next, wantNext)
	}

	scheduled := time.Date(2026, 8, 11, 11, 0, 0, 0, location)
	if !accountExpiryReminderDue(
		scheduled.Add(30*time.Second),
		"2026-08-12",
		true,
		scheduled,
		time.Time{},
	) {
		t.Fatal("reminder should be due on the day before expiry")
	}
	tests := []struct {
		name    string
		now     time.Time
		expiry  string
		enabled bool
		last    time.Time
	}{
		{name: "disabled", now: scheduled, expiry: "2026-08-12"},
		{name: "wrong day", now: scheduled.AddDate(0, 0, -1), expiry: "2026-08-12", enabled: true},
		{name: "duplicate hour", now: scheduled, expiry: "2026-08-12", enabled: true, last: scheduled},
		{name: "late after resume", now: scheduled.Add(6 * time.Minute), expiry: "2026-08-12", enabled: true},
		{name: "invalid date", now: scheduled, expiry: "2026-02-30", enabled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if accountExpiryReminderDue(
				test.now,
				test.expiry,
				test.enabled,
				scheduled,
				test.last,
			) {
				t.Fatal("unexpected reminder")
			}
		})
	}
}

func TestAccountExpiryMenuText(t *testing.T) {
	if got := accountExpiryMenuText(""); got != "账号到期日期：请填写" {
		t.Fatalf("empty menu text = %q", got)
	}
	if got := accountExpiryMenuText("2026-09-01"); got != "账号到期日期：2026-09-01" {
		t.Fatalf("saved menu text = %q", got)
	}
}
