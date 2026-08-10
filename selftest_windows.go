//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	selfTestVisibilityIterations = 200
	selfTestLayoutIterations     = 50
	selfTestCollapseIterations   = 50
	selfTestWakeTimeout          = 8 * time.Second
	selfTestTimerID              = 3
)

type nativeSelfTest struct {
	outputPath       string
	report           nativeSelfTestReport
	wakeStartedAt    time.Time
	wakeBaseline     int
	wakeMessageCount int
	wakeResultMu     sync.Mutex
	wakeResultReady  bool
	wakeResultErr    error
	wakeResultOutput string
	completed        bool
	reportWritten    bool
	finalErr         error
}

type nativeSelfTestReport struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Passed        bool                  `json:"passed"`
	StartedAt     string                `json:"startedAt"`
	FinishedAt    string                `json:"finishedAt"`
	ProcessID     int                   `json:"processId"`
	WindowClass   string                `json:"windowClass"`
	Checks        []nativeSelfTestCheck `json:"checks"`
	Failures      []string              `json:"failures"`
}

type nativeSelfTestCheck struct {
	Name                 string `json:"name"`
	Iterations           int    `json:"iterations"`
	Passed               bool   `json:"passed"`
	DurationMilliseconds int64  `json:"durationMilliseconds"`
	Error                string `json:"error,omitempty"`
}

func newNativeSelfTest(outputPath string) *nativeSelfTest {
	return &nativeSelfTest{
		outputPath: outputPath,
		report: nativeSelfTestReport{
			SchemaVersion: 1,
			ProcessID:     os.Getpid(),
			WindowClass:   nativeSelfTestWindowClass,
			Checks:        []nativeSelfTestCheck{},
			Failures:      []string{},
		},
	}
}

func (test *nativeSelfTest) start(app *nativeApp, trayErr error) {
	startedAt := time.Now()
	test.report.StartedAt = startedAt.UTC().Format(time.RFC3339Nano)

	test.checkWindow(app)
	test.checkVisibility(app)
	test.checkLayouts(app)
	test.checkCollapseAndExpand(app)
	test.checkTraySelection(app, trayErr)
	test.startWakeProbe(app)
}

func (test *nativeSelfTest) checkWindow(app *nativeApp) {
	startedAt := time.Now()
	var checkErr error
	if valid, _, _ := procIsWindow.Call(app.window); valid == 0 {
		checkErr = fmt.Errorf("IsWindow rejected hwnd 0x%x", app.window)
	}
	if visible, _, _ := procIsWindowVisible.Call(app.window); visible == 0 && checkErr == nil {
		checkErr = errors.New("the native window was not visible after startup")
	}
	test.recordCheck("window_created", 1, startedAt, checkErr)
}

func (test *nativeSelfTest) checkVisibility(app *nativeApp) {
	startedAt := time.Now()
	var checkErr error
	procShowWindow.Call(app.window, swShow)
	for iteration := 1; iteration <= selfTestVisibilityIterations; iteration++ {
		app.toggleVisible()
		if nativeWindowVisible(app.window) && checkErr == nil {
			checkErr = fmt.Errorf("iteration %d did not hide the window", iteration)
		}

		app.toggleVisible()
		if !nativeWindowVisible(app.window) && checkErr == nil {
			checkErr = fmt.Errorf("iteration %d did not show the window", iteration)
		}
	}
	if valid, _, _ := procIsWindow.Call(app.window); valid == 0 && checkErr == nil {
		checkErr = errors.New("the window handle became invalid during visibility stress")
	}
	test.recordCheck(
		"show_hide",
		selfTestVisibilityIterations,
		startedAt,
		checkErr,
	)
}

func (test *nativeSelfTest) checkLayouts(app *nativeApp) {
	startedAt := time.Now()
	var checkErr error
	for iteration := 1; iteration <= selfTestLayoutIterations; iteration++ {
		expectedID := "main-horizontal"
		if app.surfaceID == "main-horizontal" {
			expectedID = "main-vertical"
		}
		app.executeAction("toggle-layout")

		if app.surfaceID != expectedID && checkErr == nil {
			checkErr = fmt.Errorf(
				"iteration %d selected %q instead of %q",
				iteration,
				app.surfaceID,
				expectedID,
			)
		}
		if app.currentSurface == nil {
			if checkErr == nil {
				checkErr = fmt.Errorf("iteration %d cleared the rendered surface", iteration)
			}
			continue
		}
		if app.currentSurface.Surface.ID != expectedID && checkErr == nil {
			checkErr = fmt.Errorf(
				"iteration %d rendered %q instead of %q",
				iteration,
				app.currentSurface.Surface.ID,
				expectedID,
			)
		}
		geometry, ok := app.currentGeometry()
		if !ok {
			if checkErr == nil {
				checkErr = fmt.Errorf("iteration %d could not read window geometry", iteration)
			}
			continue
		}
		expectedWidth := app.currentSurface.Variant.Width
		expectedHeight := app.currentSurface.Variant.Height
		wrongSize := geometry.Width != expectedWidth || geometry.Height != expectedHeight
		if wrongSize && checkErr == nil {
			checkErr = fmt.Errorf(
				"iteration %d has size %dx%d instead of %dx%d",
				iteration,
				geometry.Width,
				geometry.Height,
				expectedWidth,
				expectedHeight,
			)
		}
	}
	test.recordCheck("layout_toggle", selfTestLayoutIterations, startedAt, checkErr)
}

func (test *nativeSelfTest) checkCollapseAndExpand(app *nativeApp) {
	startedAt := time.Now()
	app.autoCollapseEnabled = false
	procKillTimer.Call(app.window, pollTimerID)
	app.expandImmediately()

	original, geometryOK := app.currentGeometry()
	area, areaOK := app.currentWorkArea()
	if !geometryOK || !areaOK {
		test.recordCheck(
			"edge_collapse_expand",
			selfTestCollapseIterations,
			startedAt,
			errors.New("window or monitor geometry was unavailable"),
		)
		return
	}

	docked := original
	docked.X = area.X + area.Width - docked.Width
	docked.Y = area.Y + max(0, (area.Height-docked.Height)/2)
	procSetWindowPos.Call(
		app.window,
		0,
		signed(int32(docked.X)),
		signed(int32(docked.Y)),
		0,
		0,
		swpNoSize|swpNoZOrder|swpNoActivate,
	)

	var checkErr error
	for iteration := 1; iteration <= selfTestCollapseIterations; iteration++ {
		app.expandImmediately()
		procSetWindowPos.Call(
			app.window,
			0,
			signed(int32(docked.X)),
			signed(int32(docked.Y)),
			0,
			0,
			swpNoSize|swpNoZOrder|swpNoActivate,
		)

		app.startCollapseAnimation(docked, area)
		for range animationSteps {
			app.advanceAnimation()
		}
		collapsed, ok := app.currentGeometry()
		expectedCollapsed := collapsedPosition(docked, area)
		if !ok && checkErr == nil {
			checkErr = fmt.Errorf("iteration %d could not read collapsed geometry", iteration)
		}
		wrongCollapsedPosition := collapsed.X != expectedCollapsed.X ||
			collapsed.Y != expectedCollapsed.Y
		if (!app.collapsed || wrongCollapsedPosition) && checkErr == nil {
			checkErr = fmt.Errorf(
				"iteration %d collapsed at %d,%d instead of %d,%d",
				iteration,
				collapsed.X,
				collapsed.Y,
				expectedCollapsed.X,
				expectedCollapsed.Y,
			)
		}

		app.startExpandAnimation()
		for range animationSteps {
			app.advanceAnimation()
		}
		expanded, ok := app.currentGeometry()
		if !ok && checkErr == nil {
			checkErr = fmt.Errorf("iteration %d could not read expanded geometry", iteration)
		}
		wrongExpandedPosition := expanded.X != docked.X || expanded.Y != docked.Y
		if (app.collapsed || wrongExpandedPosition) && checkErr == nil {
			checkErr = fmt.Errorf(
				"iteration %d expanded at %d,%d instead of %d,%d",
				iteration,
				expanded.X,
				expanded.Y,
				docked.X,
				docked.Y,
			)
		}
	}

	procKillTimer.Call(app.window, animationTimerID)
	app.animation = windowAnimation{}
	app.collapsed = false
	app.hasExpandedPosition = false
	procSetWindowPos.Call(
		app.window,
		0,
		signed(int32(original.X)),
		signed(int32(original.Y)),
		0,
		0,
		swpNoSize|swpNoZOrder|swpNoActivate,
	)
	test.recordCheck(
		"edge_collapse_expand",
		selfTestCollapseIterations,
		startedAt,
		checkErr,
	)
}

func (test *nativeSelfTest) checkTraySelection(app *nativeApp, trayErr error) {
	startedAt := time.Now()
	checkErr := trayErr
	trayInitialized := app.tray.Window == app.window && app.tray.ID != 0 && app.tray.Icon != 0
	if !trayInitialized && checkErr == nil {
		checkErr = errors.New("the tray icon state was not initialized")
	}

	procShowWindow.Call(app.window, swHide)
	selectionEvent := uint32(wmLButtonDblClk)
	if app.trayVersion4 {
		selectionEvent = ninSelect
	}
	app.handleTrayEvent(selectionEvent)
	if !nativeWindowVisible(app.window) && checkErr == nil {
		checkErr = errors.New("the tray selection callback did not show the window")
	}
	app.handleTrayEvent(selectionEvent)
	if !nativeWindowVisible(app.window) && checkErr == nil {
		checkErr = errors.New("the tray selection callback did not keep the window visible")
	}
	test.recordCheck("tray_add_and_select", 2, startedAt, checkErr)
}

func (test *nativeSelfTest) startWakeProbe(app *nativeApp) {
	test.wakeStartedAt = time.Now()
	test.wakeBaseline = test.wakeMessageCount
	procShowWindow.Call(app.window, swHide)
	if nativeWindowVisible(app.window) {
		test.recordCheck(
			"second_instance_wake",
			1,
			test.wakeStartedAt,
			errors.New("the primary window could not be hidden before the wake probe"),
		)
		test.finish(app)
		return
	}

	executablePath, err := os.Executable()
	if err != nil {
		test.recordCheck("second_instance_wake", 1, test.wakeStartedAt, err)
		test.finish(app)
		return
	}

	uiRoot := filepath.Dir(app.bundleRoot)
	window := app.window
	surfaceID := app.surfaceID
	procSetTimer.Call(window, selfTestTimerID, uintptr(selfTestWakeTimeout.Milliseconds()), 0)
	go test.runWakeProbe(executablePath, uiRoot, surfaceID, window)
}

func (test *nativeSelfTest) runWakeProbe(
	executablePath string,
	uiRoot string,
	surfaceID string,
	window uintptr,
) {
	ctx, cancel := context.WithTimeout(context.Background(), selfTestWakeTimeout-time.Second)
	defer cancel()

	command := exec.CommandContext(
		ctx,
		executablePath,
		"--ui-root", uiRoot,
		"--surface", surfaceID,
		"--self-test-wake-probe",
	)
	output, commandErr := command.CombinedOutput()
	if ctx.Err() != nil {
		commandErr = fmt.Errorf("wake probe timed out: %w", ctx.Err())
	}
	test.setWakeProbeResult(commandErr, strings.TrimSpace(string(output)))
	procPostMessageW.Call(window, wmNativeSelfTestComplete, 0, 0)
}

func (test *nativeSelfTest) setWakeProbeResult(err error, output string) {
	test.wakeResultMu.Lock()
	defer test.wakeResultMu.Unlock()
	test.wakeResultReady = true
	test.wakeResultErr = err
	test.wakeResultOutput = output
}

func (test *nativeSelfTest) wakeProbeResult() (bool, error, string) {
	test.wakeResultMu.Lock()
	defer test.wakeResultMu.Unlock()
	return test.wakeResultReady, test.wakeResultErr, test.wakeResultOutput
}

func (test *nativeSelfTest) observeWakeMessage() {
	test.wakeMessageCount++
}

func (test *nativeSelfTest) completeWakeProbe(app *nativeApp) {
	if test.completed {
		return
	}
	procKillTimer.Call(app.window, selfTestTimerID)
	ready, probeErr, output := test.wakeProbeResult()
	if !ready {
		return
	}

	checkErr := probeErr
	if test.wakeMessageCount <= test.wakeBaseline && checkErr == nil {
		checkErr = errors.New("the second instance exited without delivering a wake message")
	}
	if !nativeWindowVisible(app.window) && checkErr == nil {
		checkErr = errors.New("the first instance was not visible after the wake message")
	}
	if output != "" && checkErr != nil {
		checkErr = fmt.Errorf("%w; child output: %s", checkErr, output)
	}
	test.recordCheck("second_instance_wake", 1, test.wakeStartedAt, checkErr)
	test.finish(app)
}

func (test *nativeSelfTest) handleWakeTimeout(app *nativeApp) {
	if test.completed {
		return
	}
	ready, _, _ := test.wakeProbeResult()
	if ready {
		test.completeWakeProbe(app)
		return
	}
	procKillTimer.Call(app.window, selfTestTimerID)
	test.recordCheck(
		"second_instance_wake",
		1,
		test.wakeStartedAt,
		errors.New("the second-instance wake probe did not complete before its deadline"),
	)
	test.finish(app)
}

func (test *nativeSelfTest) recordCheck(
	name string,
	iterations int,
	startedAt time.Time,
	err error,
) {
	check := nativeSelfTestCheck{
		Name:                 name,
		Iterations:           iterations,
		Passed:               err == nil,
		DurationMilliseconds: time.Since(startedAt).Milliseconds(),
	}
	if err != nil {
		check.Error = err.Error()
		test.report.Failures = append(test.report.Failures, name+": "+err.Error())
	}
	test.report.Checks = append(test.report.Checks, check)
}

func (test *nativeSelfTest) finish(app *nativeApp) {
	if test.completed {
		return
	}
	test.completed = true
	test.report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	test.report.Passed = len(test.report.Failures) == 0
	if err := test.writeReport(); err != nil {
		test.finalErr = fmt.Errorf("writing native self-test report: %w", err)
	} else {
		test.reportWritten = true
	}
	if !test.report.Passed && test.finalErr == nil {
		test.finalErr = fmt.Errorf("native self-test failed; see %q", test.outputPath)
	}
	procDestroyWindow.Call(app.window)
}

func (test *nativeSelfTest) writeReport() error {
	contents, err := json.MarshalIndent(test.report, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding report: %w", err)
	}
	contents = append(contents, '\n')
	if err := writeAtomic(test.outputPath, contents); err != nil {
		return err
	}
	return nil
}

func (test *nativeSelfTest) ensureFailureReport(cause error) error {
	if test.reportWritten {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if test.report.StartedAt == "" {
		test.report.StartedAt = now
	}
	if len(test.report.Checks) == 0 {
		test.report.Checks = append(test.report.Checks, nativeSelfTestCheck{
			Name:       "startup",
			Iterations: 1,
			Passed:     false,
			Error:      cause.Error(),
		})
		test.report.Failures = append(test.report.Failures, "startup: "+cause.Error())
	}
	test.report.Passed = false
	test.report.FinishedAt = now
	if err := test.writeReport(); err != nil {
		return err
	}
	test.reportWritten = true
	return nil
}

func (test *nativeSelfTest) resultError() error {
	if !test.completed {
		return errors.New("the native self-test exited before completion")
	}
	return test.finalErr
}

func nativeWindowVisible(window uintptr) bool {
	visible, _, _ := procIsWindowVisible.Call(window)
	return visible != 0
}
