//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	swShowNormal  = 1
	mbOK          = 0x00000000
	mbIconWarning = 0x00000030
)

type externalPage uint8

const (
	externalPageChatGPT externalPage = iota + 1
	externalPageBilling
	externalPageAPIUsage
	externalPageAPIKeys
	externalPageGitHub
)

type shellActions struct {
	open       func(uintptr, string) error
	showError  func(uintptr, string)
	homeDir    func() (string, error)
	fileExists func(string) (bool, error)
}

var (
	systemActionShell32 = windows.NewLazySystemDLL("shell32.dll")
	systemActionUser32  = windows.NewLazySystemDLL("user32.dll")
	procShellExecuteW   = systemActionShell32.NewProc("ShellExecuteW")
	procMessageBoxW     = systemActionUser32.NewProc("MessageBoxW")
)

func openExternalPage(owner uintptr, page externalPage) bool {
	return newShellActions().openExternalPage(owner, page)
}

func openCodexConfigFile(owner uintptr) bool {
	return newShellActions().openCodexConfigFile(owner)
}

func newShellActions() shellActions {
	return shellActions{
		open:       shellOpen,
		showError:  showSystemActionError,
		homeDir:    os.UserHomeDir,
		fileExists: regularFileExists,
	}
}

func (actions shellActions) openExternalPage(owner uintptr, page externalPage) bool {
	label, url, ok := externalPageDetails(page)
	if !ok {
		actions.reportError(owner, "无法打开未知的外部页面。")
		return false
	}
	if err := actions.open(owner, url); err != nil {
		actions.reportError(owner, fmt.Sprintf("打开 %s 失败：%v", label, err))
		return false
	}
	return true
}

func (actions shellActions) openCodexConfigFile(owner uintptr) bool {
	home, err := actions.homeDir()
	if err != nil {
		actions.reportError(owner, fmt.Sprintf("获取用户目录失败：%v", err))
		return false
	}

	path, err := codexConfigPath(home)
	if err != nil {
		actions.reportError(owner, fmt.Sprintf("获取配置文件路径失败：%v", err))
		return false
	}
	exists, err := actions.fileExists(path)
	if err != nil {
		actions.reportError(owner, fmt.Sprintf("检查配置文件失败：%v", err))
		return false
	}
	if !exists {
		actions.reportError(owner, fmt.Sprintf("未找到配置文件：%s", path))
		return false
	}
	if err := actions.open(owner, path); err != nil {
		actions.reportError(owner, fmt.Sprintf("打开配置文件失败：%v", err))
		return false
	}
	return true
}

func (actions shellActions) reportError(owner uintptr, message string) {
	if actions.showError == nil {
		return
	}
	actions.showError(owner, message)
}

func externalPageDetails(page externalPage) (string, string, bool) {
	switch page {
	case externalPageChatGPT:
		return "ChatGPT 账户页", "https://chatgpt.com", true
	case externalPageBilling:
		return "Billing 页面", "https://platform.openai.com/account/billing/overview", true
	case externalPageAPIUsage:
		return "API 用量页面", "https://platform.openai.com/usage", true
	case externalPageAPIKeys:
		return "API Keys 页面", "https://platform.openai.com/settings/organization/admin-keys", true
	case externalPageGitHub:
		return "GitHub 仓库", "https://github.com/raywangsmia-bit/CodexFloatBar", true
	default:
		return "", "", false
	}
}

func codexConfigPath(home string) (string, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return "", errors.New("user home directory is empty")
	}
	if strings.ContainsRune(home, '\x00') {
		return "", errors.New("user home directory contains NUL")
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !info.IsDir(), nil
}

func shellOpen(owner uintptr, target string) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("shell target is empty")
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}

	result, _, _ := procShellExecuteW.Call(
		owner,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0,
		0,
		swShowNormal,
	)
	if result <= 32 {
		return fmt.Errorf("ShellExecuteW failed with code %d", result)
	}
	return nil
}

func showSystemActionError(owner uintptr, message string) {
	message = strings.ReplaceAll(message, "\x00", "�")
	text, err := windows.UTF16PtrFromString(message)
	if err != nil {
		return
	}
	title, err := windows.UTF16PtrFromString("CodexFloatingBar")
	if err != nil {
		return
	}
	procMessageBoxW.Call(
		owner,
		uintptr(unsafe.Pointer(text)),
		uintptr(unsafe.Pointer(title)),
		mbOK|mbIconWarning,
	)
}
