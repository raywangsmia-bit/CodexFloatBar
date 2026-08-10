//go:build windows

package codexprocess

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWPFCompatibleCodexProcessMatching(t *testing.T) {
	tests := []struct {
		name   string
		record processRecord
		want   bool
	}{
		{
			name:   "code mode host",
			record: processRecord{Name: "codex-code-mode-host.exe"},
			want:   true,
		},
		{
			name: "Codex package",
			record: processRecord{
				Name:            "ChatGPT.exe",
				PackageFullName: "OpenAI.Codex_26.721.4979.0_x64__test",
			},
			want: true,
		},
		{
			name: "Codex WindowsApps path",
			record: processRecord{
				Name:      "chatgpt.ExE",
				ImagePath: `C:\Program Files\WindowsApps\OpenAI.Codex_1.0_x64__test\app\ChatGPT.exe`,
			},
			want: true,
		},
		{
			name: "ChatGPT package",
			record: processRecord{
				Name:            "ChatGPT.exe",
				PackageFullName: "OpenAI.ChatGPT_26.721.4979.0_x64__test",
				ImagePath:       `C:\Program Files\WindowsApps\OpenAI.ChatGPT_1.0_x64__test\app\ChatGPT.exe`,
			},
			want: false,
		},
		{
			name: "VS Code Codex CLI",
			record: processRecord{
				Name:      "codex.exe",
				ImagePath: `C:\Users\test\.vscode\extensions\openai.chatgpt\bin\codex.exe`,
			},
			want: false,
		},
		{
			name:   "generic ChatGPT process",
			record: processRecord{Name: "ChatGPT.exe"},
			want:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := matchesCodexProcess(test.record); got != test.want {
				t.Fatalf("matchesCodexProcess() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCodexPackageAndExecutablePathRules(t *testing.T) {
	if !IsPackageFullName("openai.codex_26.721.4979.0_x64__test") {
		t.Fatal("case-insensitive Codex package was not recognized")
	}
	if IsPackageFullName("OpenAI.ChatGPT_26.721.4979.0_x64__test") {
		t.Fatal("ChatGPT package was recognized as Codex")
	}
	if !IsDesktopExecutablePath(
		`C:/Program Files/WindowsApps/OpenAI.Codex_1.0_x64__test/app/ChatGPT.exe`,
	) {
		t.Fatal("Codex WindowsApps path was not recognized")
	}
	if IsDesktopExecutablePath(
		`C:\Program Files\WindowsApps\OpenAI.ChatGPT_1.0_x64__test\app\ChatGPT.exe`,
	) {
		t.Fatal("ChatGPT WindowsApps path was recognized as Codex")
	}
	if IsDesktopExecutablePath(
		`C:\Users\test\.vscode\extensions\openai.chatgpt\bin\codex.exe`,
	) {
		t.Fatal("Codex CLI path was recognized as the desktop app")
	}
}

func TestNormalizeObservedWindowStateFallsBackWithoutWindowProcess(t *testing.T) {
	tests := []struct {
		name               string
		running            bool
		windowProcessCount int
		visible            bool
		want               observedState
	}{
		{name: "not running", want: observedState{}},
		{
			name:    "host-only fallback remains visible",
			running: true,
			want:    observedState{Running: true, Visible: true},
		},
		{
			name:               "packaged window minimized",
			running:            true,
			windowProcessCount: 1,
			want:               observedState{Running: true, Visible: false},
		},
		{
			name:               "packaged window visible",
			running:            true,
			windowProcessCount: 1,
			visible:            true,
			want:               observedState{Running: true, Visible: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeObservedWindowState(
				test.running,
				test.windowProcessCount,
				test.visible,
			)
			if got != test.want {
				t.Fatalf("state = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestStateTrackerDebouncesExitAndRestoresImmediately(t *testing.T) {
	tracker := stateTracker{missThreshold: 3}
	tests := []struct {
		observed    observedState
		wantState   observedState
		wantChanged bool
	}{
		{observed: observedState{Running: true, Visible: true}, wantState: observedState{Running: true, Visible: true}, wantChanged: true},
		{observed: observedState{}, wantState: observedState{Running: true, Visible: true}, wantChanged: false},
		{observed: observedState{}, wantState: observedState{Running: true, Visible: true}, wantChanged: false},
		{observed: observedState{Running: true, Visible: true}, wantState: observedState{Running: true, Visible: true}, wantChanged: false},
		{observed: observedState{}, wantState: observedState{Running: true, Visible: true}, wantChanged: false},
		{observed: observedState{}, wantState: observedState{Running: true, Visible: true}, wantChanged: false},
		{observed: observedState{}, wantState: observedState{}, wantChanged: true},
		{observed: observedState{}, wantState: observedState{}, wantChanged: false},
		{observed: observedState{Running: true, Visible: true}, wantState: observedState{Running: true, Visible: true}, wantChanged: true},
	}
	for index, test := range tests {
		state, changed := tracker.observe(test.observed)
		if state != test.wantState || changed != test.wantChanged {
			t.Fatalf(
				"observation %d = (%v,%v), want (%v,%v)",
				index,
				state,
				changed,
				test.wantState,
				test.wantChanged,
			)
		}
	}
}

func TestStateTrackerConfirmsAnInitiallyMissingCodexAfterThreePolls(t *testing.T) {
	tracker := stateTracker{missThreshold: 3}
	for miss := 1; miss <= 3; miss++ {
		state, changed := tracker.observe(observedState{})
		if state.Running {
			t.Fatalf("miss %d reported Codex running", miss)
		}
		wantChanged := miss == 3
		if changed != wantChanged {
			t.Fatalf("miss %d changed = %v, want %v", miss, changed, wantChanged)
		}
	}
}

func TestMonitorPublishesExitAndRecoveryAndCancels(t *testing.T) {
	observations := make(chan bool)
	detector := DetectorFunc(func(ctx context.Context) (bool, error) {
		select {
		case running := <-observations:
			return running, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	})
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	monitor := NewMonitor(Options{
		PollInterval: 5 * time.Millisecond,
		Detector:     detector,
		Now:          func() time.Time { return now },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- monitor.Run(ctx)
	}()

	sendObservation(t, observations, true)
	assertStatus(t, monitor.Updates(), true, true, now)
	sendObservation(t, observations, false)
	sendObservation(t, observations, false)
	sendObservation(t, observations, false)
	assertStatus(t, monitor.Updates(), false, false, now)
	sendObservation(t, observations, true)
	assertStatus(t, monitor.Updates(), true, true, now)

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop after cancellation")
	}
	if _, open := <-monitor.Updates(); open {
		t.Fatal("updates channel remained open after cancellation")
	}
}

type visibilityDetector struct {
	observations <-chan observedState
}

func (detector visibilityDetector) Running(ctx context.Context) (bool, error) {
	state, err := detector.State(ctx)
	return state.Running, err
}

func (detector visibilityDetector) State(ctx context.Context) (observedState, error) {
	select {
	case state := <-detector.observations:
		return state, nil
	case <-ctx.Done():
		return observedState{}, ctx.Err()
	}
}

func TestMonitorPublishesMinimizeAndRestoreImmediately(t *testing.T) {
	observations := make(chan observedState)
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	monitor := NewMonitor(Options{
		PollInterval: 5 * time.Millisecond,
		Detector:     visibilityDetector{observations: observations},
		Now:          func() time.Time { return now },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = monitor.Run(ctx)
	}()

	for _, state := range []observedState{
		{Running: true, Visible: true},
		{Running: true, Visible: false},
		{Running: true, Visible: true},
	} {
		select {
		case observations <- state:
		case <-time.After(time.Second):
			t.Fatalf("monitor did not request state %+v", state)
		}
		assertStatus(t, monitor.Updates(), state.Running, state.Visible, now)
	}
}

func TestMonitorUsesWPFCompatibleDefaults(t *testing.T) {
	monitor := NewMonitor(Options{Detector: DetectorFunc(func(context.Context) (bool, error) {
		return false, nil
	})})
	if monitor.pollInterval != 2*time.Second {
		t.Fatalf("poll interval = %v, want 2s", monitor.pollInterval)
	}
	if monitor.missThreshold != 3 {
		t.Fatalf("miss threshold = %d, want 3", monitor.missThreshold)
	}
}

func sendObservation(t *testing.T, observations chan<- bool, running bool) {
	t.Helper()
	select {
	case observations <- running:
	case <-time.After(time.Second):
		t.Fatalf("monitor did not request observation %v", running)
	}
}

func assertStatus(
	t *testing.T,
	updates <-chan Status,
	running bool,
	visible bool,
	checkedAt time.Time,
) {
	t.Helper()
	select {
	case status, open := <-updates:
		if !open {
			t.Fatal("updates channel closed early")
		}
		if status.Running != running || status.Visible != visible ||
			!status.CheckedAt.Equal(checkedAt) {
			t.Fatalf(
				"status = %+v, want running=%v visible=%v checkedAt=%v",
				status,
				running,
				visible,
				checkedAt,
			)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for running=%v", running)
	}
}
