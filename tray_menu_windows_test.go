//go:build windows

package main

import "testing"

func TestTrayCommandIDsAreUnique(t *testing.T) {
	commands := []uintptr{
		trayCommandRefreshStatus,
		trayCommandCopyStatus,
		trayCommandVisible,
		trayCommandOpenConfig,
		trayCommandOpenChatGPT,
		trayCommandOpenBilling,
		trayCommandOpenAPIUsage,
		trayCommandOpenAPIKeys,
		trayCommandOpenGitHub,
		trayCommandThemeDark,
		trayCommandThemeLight,
		trayCommandLayoutHorizontal,
		trayCommandLayoutVertical,
		trayCommandScale90,
		trayCommandScale100,
		trayCommandScale110,
		trayCommandAutoCollapse,
		trayCommandStartup,
		trayCommandExit,
		trayCommandFollowCodex,
		trayCommandReloadUI,
	}

	seen := make(map[uintptr]struct{}, len(commands))
	for _, command := range commands {
		if _, exists := seen[command]; exists {
			t.Fatalf("duplicate tray command ID %d", command)
		}
		seen[command] = struct{}{}
	}
}
