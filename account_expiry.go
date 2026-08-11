package main

import (
	"fmt"
	"time"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/appsettings"
)

const accountExpiryReminderGrace = 5 * time.Minute

func accountExpiryMenuText(value string) string {
	if normalized, ok := appsettings.NormalizeAccountExpiryDate(value); ok && normalized != "" {
		return "账号到期日期：" + normalized
	}
	return "账号到期日期：请填写"
}

func nextAccountExpiryReminderHour(now time.Time) time.Time {
	return time.Date(
		now.Year(),
		now.Month(),
		now.Day(),
		now.Hour()+1,
		0,
		0,
		0,
		now.Location(),
	)
}

func accountExpiryReminderDue(
	now time.Time,
	expiryValue string,
	enabled bool,
	scheduledHour time.Time,
	lastReminderHour time.Time,
) bool {
	if !enabled || scheduledHour.IsZero() || now.Before(scheduledHour) ||
		now.Sub(scheduledHour) > accountExpiryReminderGrace ||
		lastReminderHour.Equal(scheduledHour) {
		return false
	}
	expiry, err := time.ParseInLocation("2006-01-02", expiryValue, now.Location())
	if err != nil {
		return false
	}
	reminderDay := expiry.AddDate(0, 0, -1)
	return sameLocalDate(now, reminderDay)
}

func sameLocalDate(left time.Time, right time.Time) bool {
	left = left.In(right.Location())
	return left.Year() == right.Year() && left.YearDay() == right.YearDay()
}

func accountExpiryReminderMessage(value string) string {
	return fmt.Sprintf("账号将于 %s 到期，请及时续费。", value)
}
