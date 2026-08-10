//go:build windows

package main

import (
	"strings"
	"testing"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/codexdata"
)

func TestQuotaToneThresholds(t *testing.T) {
	tests := []struct {
		remaining int
		want      quotaTone
	}{
		{remaining: 100, want: quotaToneGood},
		{remaining: 61, want: quotaToneGood},
		{remaining: 60, want: quotaToneWarn},
		{remaining: 11, want: quotaToneWarn},
		{remaining: 10, want: quotaToneDanger},
		{remaining: 0, want: quotaToneDanger},
	}
	for _, test := range tests {
		if got := toneForRemaining(test.remaining); got != test.want {
			t.Fatalf("toneForRemaining(%d) = %q, want %q", test.remaining, got, test.want)
		}
	}
}

func TestQuotaLevelObserverOnlyNotifiesWhenQuotaWorsens(t *testing.T) {
	var observer quotaLevelObserver
	tests := []struct {
		name   string
		tone   quotaTone
		notify bool
	}{
		{name: "initial good", tone: quotaToneGood},
		{name: "good to warn", tone: quotaToneWarn, notify: true},
		{name: "same warn", tone: quotaToneWarn},
		{name: "warn to danger", tone: quotaToneDanger, notify: true},
		{name: "recover to good", tone: quotaToneGood},
		{name: "good directly to danger", tone: quotaToneDanger, notify: true},
		{name: "unavailable clears", tone: quotaToneOffline},
		{name: "first after unavailable", tone: quotaToneWarn},
	}
	for _, test := range tests {
		if got := observer.observe(test.tone); got != test.notify {
			t.Fatalf("%s notify = %v, want %v", test.name, got, test.notify)
		}
	}
}

func TestClipboardStatusExcludesSourceErrorsAndRawContent(t *testing.T) {
	runtime := &statusRuntime{current: codexdata.AppSnapshot{
		Account: codexdata.AccountSummary{DisplayText: "Codex: ChatGPT"},
		Config: codexdata.ConfigSummary{
			Message: `SECRET_CONFIG_PATH=C:\Users\fixture\.codex\config.toml`,
		},
		Runtime: codexdata.RuntimeStatus{
			Model:           "gpt-5.6",
			ReasoningEffort: "high",
			SpeedTier:       "fast",
		},
		RateLimit: codexdata.RateLimitSummary{
			Message: "SECRET_RAW_LOG_CONTENT",
		},
	}}

	text := runtime.clipboardText(true)
	for _, secret := range []string{
		"SECRET_CONFIG_PATH",
		"SECRET_RAW_LOG_CONTENT",
		`.codex\config.toml`,
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("clipboard text leaked %q: %s", secret, text)
		}
	}
	for _, expected := range []string{"Codex：运行中", "模型：gpt-5.6", "推理强度：高"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("clipboard text is missing %q: %s", expected, text)
		}
	}
}
