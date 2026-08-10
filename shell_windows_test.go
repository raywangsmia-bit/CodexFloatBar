//go:build windows

package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalPageDetailsUsesFixedAllowlist(t *testing.T) {
	tests := []struct {
		page externalPage
		url  string
	}{
		{page: externalPageChatGPT, url: "https://chatgpt.com"},
		{page: externalPageBilling, url: "https://platform.openai.com/account/billing/overview"},
		{page: externalPageAPIUsage, url: "https://platform.openai.com/usage"},
		{page: externalPageAPIKeys, url: "https://platform.openai.com/settings/organization/admin-keys"},
		{page: externalPageGitHub, url: "https://github.com/raywangsmia-bit/CodexFloatBar"},
	}
	for _, test := range tests {
		_, got, ok := externalPageDetails(test.page)
		if !ok || got != test.url {
			t.Fatalf("external page %d = %q, %v; want %q, true", test.page, got, ok, test.url)
		}
	}
	if _, _, ok := externalPageDetails(externalPage(255)); ok {
		t.Fatal("unknown external page was allowlisted")
	}
}

func TestShellActionsRejectUnknownPageBeforeOpening(t *testing.T) {
	openCalls := 0
	messages := []string{}
	actions := shellActions{
		open: func(uintptr, string) error {
			openCalls++
			return nil
		},
		showError: func(_ uintptr, message string) {
			messages = append(messages, message)
		},
	}

	if actions.openExternalPage(10, externalPage(255)) {
		t.Fatal("unknown external page reported success")
	}
	if openCalls != 0 {
		t.Fatalf("shell opener was called %d times", openCalls)
	}
	if len(messages) != 1 || !strings.Contains(messages[0], "未知") {
		t.Fatalf("messages = %q", messages)
	}
}

func TestShellActionsOpenCodexConfig(t *testing.T) {
	home := filepath.Join(`C:\Users`, "fixture user")
	wantPath := filepath.Join(home, ".codex", "config.toml")
	var opened string
	actions := shellActions{
		open: func(_ uintptr, target string) error {
			opened = target
			return nil
		},
		showError: func(uintptr, string) {
			t.Fatal("unexpected user-visible error")
		},
		homeDir: func() (string, error) {
			return home, nil
		},
		fileExists: func(path string) (bool, error) {
			if path != wantPath {
				t.Fatalf("config path = %q, want %q", path, wantPath)
			}
			return true, nil
		},
	}

	if !actions.openCodexConfigFile(10) {
		t.Fatal("existing config did not open")
	}
	if opened != wantPath {
		t.Fatalf("opened path = %q, want %q", opened, wantPath)
	}
}

func TestShellActionsReportMissingConfig(t *testing.T) {
	messages := []string{}
	actions := shellActions{
		open: func(uintptr, string) error {
			t.Fatal("shell opener must not run for missing config")
			return nil
		},
		showError: func(_ uintptr, message string) {
			messages = append(messages, message)
		},
		homeDir: func() (string, error) {
			return `C:\Users\fixture`, nil
		},
		fileExists: func(string) (bool, error) {
			return false, nil
		},
	}

	if actions.openCodexConfigFile(10) {
		t.Fatal("missing config reported success")
	}
	if len(messages) != 1 || !strings.Contains(messages[0], "未找到配置文件") {
		t.Fatalf("messages = %q", messages)
	}
}

func TestShellActionsReportOpenFailure(t *testing.T) {
	cause := errors.New("fixture shell failure")
	messages := []string{}
	actions := shellActions{
		open: func(uintptr, string) error {
			return cause
		},
		showError: func(_ uintptr, message string) {
			messages = append(messages, message)
		},
	}

	if actions.openExternalPage(10, externalPageGitHub) {
		t.Fatal("failed shell open reported success")
	}
	if len(messages) != 1 || !strings.Contains(messages[0], cause.Error()) {
		t.Fatalf("messages = %q", messages)
	}
}
