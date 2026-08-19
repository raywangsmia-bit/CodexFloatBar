//go:build windows

package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/appidentity"
	"github.com/raywangsmia-bit/CodexFloatBar/internal/appsettings"
	"golang.org/x/sys/windows"
)

const (
	nativeWindowClass            = appidentity.WindowClass
	nativeMutexName              = appidentity.MutexName
	nativeSelfTestWindowClass    = appidentity.AppID + ".SelfTest.Window"
	nativeSelfTestMutexName      = "Local\\" + appidentity.AppID + ".SelfTest.SingleInstance"
	mainWindowTitle              = appidentity.ProductName
	statisticsWindowTitle        = appidentity.ProductName + " Statistics"
	usageToastWindowTitle        = appidentity.ProductName + " Usage Toast"
	statisticsSurfaceID          = "statistics"
	usageToastSurfaceID          = "usage-toast"
	pollTimerID                  = 1
	animationTimerID             = 2
	usageToastAnimationTimerID   = 3
	usageToastTimerID            = 4
	trayRetryTimerID             = 5
	accountExpiryReminderTimerID = 6
	auxiliaryGapLogical          = 8
	usageToastOffsetLogical      = 6
	trayRetryLimit               = 5
	mainWindowAnimationDuration  = 160 * time.Millisecond
	usageToastShowDuration       = 180 * time.Millisecond
	usageToastHideDuration       = 130 * time.Millisecond
	usageToastVisibleDuration    = 4 * time.Second
	mainWindowAnimationTick      = 16 * time.Millisecond
	usageToastAnimationTick      = 17 * time.Millisecond
)

type windowRole uint8

const (
	windowRoleUnknown windowRole = iota
	windowRoleMain
	windowRoleStatistics
	windowRoleUsageToast
)

type auxiliaryWindow struct {
	Role           windowRole
	Handle         uintptr
	SurfaceID      string
	Title          string
	CurrentSurface *renderedSurface
	Visibility     auxiliaryVisibility
	Dirty          bool
}

type auxiliaryVisibility uint8

const (
	auxiliaryHidden auxiliaryVisibility = iota
	auxiliaryShowing
	auxiliaryVisible
	auxiliaryHiding
)

type toastVisibilityEvent uint8

const (
	toastShowRequested toastVisibilityEvent = iota
	toastHideRequested
	toastAnimationCompleted
)

func transitionToastVisibility(
	current auxiliaryVisibility,
	event toastVisibilityEvent,
) auxiliaryVisibility {
	switch event {
	case toastShowRequested:
		if current == auxiliaryHidden || current == auxiliaryHiding {
			return auxiliaryShowing
		}
	case toastHideRequested:
		if current == auxiliaryShowing || current == auxiliaryVisible {
			return auxiliaryHiding
		}
	case toastAnimationCompleted:
		if current == auxiliaryShowing {
			return auxiliaryVisible
		}
		if current == auxiliaryHiding {
			return auxiliaryHidden
		}
	}
	return current
}

type timerSyncAction uint8

const (
	timerSyncNone timerSyncAction = iota
	timerSyncStart
	timerSyncStop
)

func autoCollapseTimerSyncAction(
	enabled bool,
	mainVisible bool,
	running bool,
) timerSyncAction {
	shouldRun := enabled && mainVisible
	if shouldRun && !running {
		return timerSyncStart
	}
	if !shouldRun && running {
		return timerSyncStop
	}
	return timerSyncNone
}

type nativeApp struct {
	bundleRoot                    string
	surfaceID                     string
	currentSurface                *renderedSurface
	placement                     placementStore
	savedPlacement                *windowPlacement
	window                        uintptr
	instance                      uintptr
	mutex                         windows.Handle
	tray                          notifyIconData
	trayVersion4                  bool
	trayRetryAttempts             int
	taskbarCreatedMessage         uint32
	lastManifestState             fileState
	stopWatching                  chan struct{}
	stopOnce                      sync.Once
	watchBundleEnabled            bool
	autoCollapseEnabled           bool
	autoCollapseTimerRunning      bool
	collapsed                     bool
	expandedPosition              geometryPoint
	hasExpandedPosition           bool
	awayPolls                     int
	animation                     windowAnimation
	usageToastAnimation           toastWindowAnimation
	animationsEnabled             bool
	windowClass                   string
	mutexName                     string
	placementDisabled             bool
	selfTest                      *nativeSelfTest
	statisticsWindow              auxiliaryWindow
	usageToastWindow              auxiliaryWindow
	updatingWindows               bool
	status                        *statusRuntime
	process                       *processRuntime
	appearance                    *appearanceRuntime
	startedAt                     time.Time
	surfaceOverride               bool
	statisticsDetached            bool
	manuallyHidden                bool
	followCodexEnabled            bool
	accountExpiryDate             string
	accountExpiryReminderEnabled  bool
	accountExpiryScheduledHour    time.Time
	accountExpiryLastReminderHour time.Time
	accountExpiryToastActive      bool
	codexStateKnown               bool
	codexWasRunning               bool
	codexWasVisible               bool
	winEventHooks                 []uintptr
	occlusionEventPending         atomic.Bool
	baseSurfaces                  map[string]*renderedSurface
}

type windowAnimation struct {
	active        bool
	start         geometryPoint
	target        geometryPoint
	startedAt     time.Time
	duration      time.Duration
	endsCollapsed bool
}

type toastWindowAnimation struct {
	active          bool
	startedAt       time.Time
	duration        time.Duration
	startPosition   geometryPoint
	targetPosition  geometryPoint
	stablePosition  geometryPoint
	currentPosition geometryPoint
	startAlpha      byte
	targetAlpha     byte
	currentAlpha    byte
	targetState     auxiliaryVisibility
}

type toastAnimationRequest struct {
	startedAt      time.Time
	startPosition  geometryPoint
	targetPosition geometryPoint
	stablePosition geometryPoint
	startAlpha     byte
	targetAlpha    byte
	targetState    auxiliaryVisibility
	baseDuration   time.Duration
}

type fileState struct {
	Size       int64
	ModifiedAt time.Time
}

type windowRenderUpdate struct {
	Window           uintptr
	Surface          *renderedSurface
	Position         geometryPoint
	PreviousSurface  *renderedSurface
	PreviousPosition geometryPoint
}

var activeNativeApp *nativeApp

func newNativeApp(bundleRoot string, surfaceID string, startedAt time.Time) *nativeApp {
	return &nativeApp{
		bundleRoot:         bundleRoot,
		surfaceID:          surfaceID,
		surfaceOverride:    surfaceID != "",
		placement:          newPlacementStore(),
		stopWatching:       make(chan struct{}),
		baseSurfaces:       map[string]*renderedSurface{},
		windowClass:        nativeWindowClass,
		mutexName:          nativeMutexName,
		status:             newStatusRuntime(),
		process:            newProcessRuntime(),
		appearance:         newAppearanceRuntime(),
		startedAt:          startedAt,
		animationsEnabled:  true,
		followCodexEnabled: true,
		statisticsWindow: auxiliaryWindow{
			Role:      windowRoleStatistics,
			SurfaceID: statisticsSurfaceID,
			Title:     statisticsWindowTitle,
		},
		usageToastWindow: auxiliaryWindow{
			Role:      windowRoleUsageToast,
			SurfaceID: usageToastSurfaceID,
			Title:     usageToastWindowTitle,
		},
	}
}

func (app *nativeApp) useSelfTestIdentity() {
	app.windowClass = nativeSelfTestWindowClass
	app.mutexName = nativeSelfTestMutexName
	app.placementDisabled = true
	if app.status != nil {
		app.status.disable()
	}
	if app.appearance != nil {
		app.appearance.disable()
	}
	if app.process != nil {
		app.process.disable()
	}
}

func (app *nativeApp) run() error {
	alreadyRunning, err := app.acquireSingleInstance()
	if err != nil {
		return err
	}
	if alreadyRunning {
		if app.selfTest != nil {
			return errors.New("another native self-test is already running")
		}
		if !wakeExistingWindow(app.windowClass, mainWindowTitle, 3*time.Second) {
			log.Print("the first instance is running but its window was not ready")
		}
		return nil
	}
	defer windows.CloseHandle(app.mutex)

	setProcessDPIAware()
	app.animationsEnabled = nativeAnimationsEnabled()
	startupDPI := systemDPI()
	if app.appearance != nil {
		if err := app.appearance.load(startupDPI); err != nil {
			log.Printf("loading appearance: %v", err)
		}
		app.autoCollapseEnabled = app.appearance.current.AutoCollapse
		app.followCodexEnabled = app.appearance.current.FollowCodex
		app.accountExpiryDate = app.appearance.current.AccountExpiryDate
		app.accountExpiryReminderEnabled = app.appearance.current.AccountExpiryReminder
		app.statisticsDetached = app.appearance.current.StatisticsWindow != nil
	}
	if !app.placementDisabled {
		app.loadSavedPlacement()
	}
	manifest, err := readManifest(app.bundleRoot)
	if err != nil {
		return err
	}
	app.selectSurfaceIDs(manifest)
	initial, err := app.loadComposedSurface(
		manifest,
		app.surfaceID,
		startupDPI,
	)
	if err != nil {
		return fmt.Errorf("loading initial UI: %w", err)
	}
	if app.surfaceID == "" {
		app.surfaceID = initial.Surface.ID
	}

	activeNativeApp = app
	defer func() {
		activeNativeApp = nil
	}()

	if err := app.registerWindowClass(); err != nil {
		return err
	}
	if err := app.createWindow(initial); err != nil {
		return err
	}
	app.scheduleAccountExpiryReminder(time.Now())
	app.savePlacement()
	if err := app.startStatusMonitor(); err != nil {
		log.Printf("starting Codex data monitor: %v", err)
	}
	defer app.stopStatusMonitor()
	app.startProcessMonitor()
	defer app.stopProcessMonitor()
	if err := app.startOcclusionHooks(); err != nil {
		log.Printf("starting Codex occlusion hooks: %v", err)
	}
	defer app.stopOcclusionHooks()
	defer app.stopWatcher()

	app.reloadSurface()
	previouslyVisible, _, _ := procShowWindow.Call(app.window, swShow)
	valid, _, _ := procIsWindow.Call(app.window)
	visible, _, _ := procIsWindowVisible.Call(app.window)
	app.syncAutoCollapseTimer(visible != 0)
	log.Printf(
		"window ready hwnd=0x%x previously-visible=%t valid=%t visible=%t",
		app.window,
		previouslyVisible != 0,
		valid != 0,
		visible != 0,
	)
	defer app.deleteTrayIcon()
	procPostMessageW.Call(app.window, wmNativeInitialize, 0, 0)
	app.startBundleWatcher()

	if err := runMessageLoop(); err != nil {
		return err
	}
	if app.selfTest != nil {
		return app.selfTest.resultError()
	}
	return nil
}

func (app *nativeApp) loadSavedPlacement() {
	if app.appearance != nil {
		settings := app.appearance.current
		if position := settings.MainWindowForLayout(settings.Layout); position != nil {
			app.savedPlacement = &windowPlacement{
				X:      position.X,
				Y:      position.Y,
				Layout: string(settings.Layout),
			}
			return
		}
	}
	if placement, ok := app.placement.load(); ok {
		app.savedPlacement = &placement
		if app.appearance != nil && !app.surfaceOverride {
			app.appearance.setLayout(appsettings.Layout(placement.Layout))
		}
	}
}

func (app *nativeApp) selectSurfaceIDs(manifest bundleManifest) {
	if app.appearance == nil {
		return
	}
	if !app.surfaceOverride {
		app.surfaceID = resolveMainSurfaceID(manifest, app.appearance.current)
	}
	app.statisticsWindow.SurfaceID = resolveStatisticsSurfaceID(
		manifest,
		app.appearance.current.Theme,
	)
	tone := quotaToneOffline
	if app.status != nil {
		tone = app.status.tone()
	}
	app.usageToastWindow.SurfaceID = resolveUsageToastSurfaceID(
		manifest,
		app.appearance.current.Theme,
		tone,
	)
	app.pruneBaseSurfaces()
}

func (app *nativeApp) pruneBaseSurfaces() {
	if app.baseSurfaces == nil {
		return
	}
	active := map[string]struct{}{
		app.surfaceID:                  {},
		app.statisticsWindow.SurfaceID: {},
		app.usageToastWindow.SurfaceID: {},
	}
	for surfaceID := range app.baseSurfaces {
		if _, keep := active[surfaceID]; !keep {
			delete(app.baseSurfaces, surfaceID)
		}
	}
}

func (app *nativeApp) acquireSingleInstance() (bool, error) {
	name := utf16Pointer(app.mutexName)
	mutex, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if mutex != 0 {
			_ = windows.CloseHandle(mutex)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("creating single-instance mutex: %w", err)
	}
	app.mutex = mutex
	return false, nil
}

func (app *nativeApp) registerWindowClass() error {
	instance, _, lastErr := procGetModuleHandleW.Call(0)
	if instance == 0 {
		return callError("GetModuleHandleW", lastErr)
	}
	app.instance = instance

	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	icon, err := app.loadApplicationIcon()
	if err != nil {
		return err
	}
	className := utf16Pointer(app.windowClass)
	class := windowClassEx{
		Size:            uint32(unsafe.Sizeof(windowClassEx{})),
		Style:           csHRedraw | csVRedraw,
		WindowProc:      windows.NewCallback(nativeWindowProc),
		Instance:        instance,
		Icon:            icon,
		Cursor:          cursor,
		BackgroundBrush: colorWindow + 1,
		ClassName:       className,
		SmallIcon:       icon,
	}
	result, _, lastErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
	if result == 0 {
		return callError("RegisterClassExW", lastErr)
	}

	messageName := utf16Pointer("TaskbarCreated")
	message, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(messageName)))
	app.taskbarCreatedMessage = uint32(message)
	return nil
}

func (app *nativeApp) createWindow(surface *renderedSurface) error {
	width := int32(surface.Variant.Width)
	height := int32(surface.Variant.Height)
	areas := enumerateWorkAreas()
	geometry := windowGeometry{
		Width:  int(width),
		Height: int(height),
	}
	if len(areas) > 0 {
		geometry.X = areas[0].X + max(0, (areas[0].Width-geometry.Width)/2)
		geometry.Y = areas[0].Y + max(0, (areas[0].Height-geometry.Height)/2)
	}
	if app.savedPlacement != nil {
		geometry.X = app.savedPlacement.X
		geometry.Y = app.savedPlacement.Y
		geometry = clampGeometry(geometry, areas)
	}

	className := utf16Pointer(app.windowClass)
	title := utf16Pointer(mainWindowTitle)
	window, _, lastErr := procCreateWindowExW.Call(
		mainWindowExtendedStyle(),
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsPopup,
		signed(int32(geometry.X)),
		signed(int32(geometry.Y)),
		signed(width),
		signed(height),
		0,
		0,
		app.instance,
		0,
	)
	if window == 0 {
		return callError("CreateWindowExW", lastErr)
	}
	app.window = window
	if err := updateLayeredWindow(
		window,
		surface.Image,
		geometryPoint{X: geometry.X, Y: geometry.Y},
	); err != nil {
		return err
	}
	app.currentSurface = surface
	return nil
}

func (app *nativeApp) reloadSurface() {
	app.reloadSurfaceAtDPI(app.windowDPI(app.window))
}

func (app *nativeApp) reloadSurfaceAtDPI(dpi uint32) {
	app.reloadSurfaceAtDPIWithMode(dpi, false)
}

func (app *nativeApp) reloadStatusSurfaces() {
	app.reloadSurfaceAtDPIWithMode(app.windowDPI(app.window), true)
}

func (app *nativeApp) reloadSurfaceAtDPIWithMode(dpi uint32, statusOnly bool) {
	manifest, err := readManifest(app.bundleRoot)
	if err != nil {
		log.Printf("keeping previous UI after reload error: %v", err)
		return
	}
	app.selectSurfaceIDs(manifest)
	surface, err := app.loadComposedSurface(manifest, app.surfaceID, dpi)
	if err != nil {
		log.Printf("keeping previous UI after reload error: %v", err)
		return
	}
	if !statusOnly {
		app.settleUsageToastAnimation()
	}
	var nextStatistics *renderedSurface
	if !statusOnly || app.shouldComposeAuxiliaryStatus(&app.statisticsWindow) {
		nextStatistics, err = app.loadAuxiliarySurface(
			manifest,
			&app.statisticsWindow,
			app.auxiliaryDPI(&app.statisticsWindow, dpi),
		)
		if err != nil {
			log.Printf("keeping previous UI after statistics reload error: %v", err)
			return
		}
	} else if app.statisticsWindow.Handle != 0 {
		app.statisticsWindow.Dirty = true
	}
	var nextUsageToast *renderedSurface
	nextUsageToastSurfaceID := app.usageToastWindow.SurfaceID
	if !statusOnly || app.shouldComposeAuxiliaryStatus(&app.usageToastWindow) {
		if !statusOnly && app.accountExpiryToastActive &&
			app.usageToastWindow.Handle != 0 {
			theme := appsettings.ThemeDark
			if app.appearance != nil {
				theme = app.appearance.current.Theme
			}
			nextUsageToastSurfaceID = resolveUsageToastSurfaceID(
				manifest,
				theme,
				quotaToneWarn,
			)
			presentation := app.accountExpiryToastPresentation()
			nextUsageToast, err = app.loadComposedSurfaceWithPresentation(
				manifest,
				nextUsageToastSurfaceID,
				app.auxiliaryDPI(&app.usageToastWindow, dpi),
				&presentation,
			)
		} else {
			nextUsageToast, err = app.loadAuxiliarySurface(
				manifest,
				&app.usageToastWindow,
				app.auxiliaryDPI(&app.usageToastWindow, dpi),
			)
		}
		if err != nil {
			log.Printf("keeping previous UI after usage toast reload error: %v", err)
			return
		}
	} else if app.usageToastWindow.Handle != 0 {
		app.usageToastWindow.Dirty = true
	}
	animationInProgress := app.animation.active
	wasAnimating := animationInProgress && !statusOnly
	keepCollapsed := app.collapsed && !animationInProgress && app.hasExpandedPosition
	clearCollapsedState := !statusOnly &&
		(wasAnimating || (app.collapsed && !keepCollapsed))
	nextExpandedPosition := app.expandedPosition
	updateExpandedPosition := false
	geometry, ok := app.currentGeometry()
	if !ok {
		log.Print("keeping previous UI because the window geometry is unavailable")
		return
	}
	if ((app.collapsed && !animationInProgress) || wasAnimating) && app.hasExpandedPosition {
		geometry.X = app.expandedPosition.X
		geometry.Y = app.expandedPosition.Y
	}
	areas := enumerateWorkAreas()
	area, foundArea := bestIntersectingWorkArea(geometry, areas)
	if foundArea {
		if statusOnly && animationInProgress {
			geometry.Width = surface.Variant.Width
			geometry.Height = surface.Variant.Height
		} else {
			geometry = resizeGeometryPreservingDock(
				geometry,
				surface.Variant.Width,
				surface.Variant.Height,
				area,
			)
		}
		expandedPosition := geometryPoint{X: geometry.X, Y: geometry.Y}
		if keepCollapsed {
			collapsed, canCollapse := collapsedPositionForWorkAreas(
				geometry,
				area,
				areas,
			)
			if canCollapse {
				geometry.X = collapsed.X
				geometry.Y = collapsed.Y
				nextExpandedPosition = expandedPosition
				updateExpandedPosition = true
			} else {
				keepCollapsed = false
				clearCollapsedState = true
			}
		}
	} else {
		geometry.Width = surface.Variant.Width
		geometry.Height = surface.Variant.Height
		geometry = clampGeometry(geometry, areas)
	}

	updates := []windowRenderUpdate{{
		Window:           app.window,
		Surface:          surface,
		Position:         geometryPoint{X: geometry.X, Y: geometry.Y},
		PreviousSurface:  app.currentSurface,
		PreviousPosition: app.currentWindowPosition(app.window),
	}}
	auxiliaryPositions := map[windowRole]windowGeometry{}
	if foundArea {
		statisticsLayoutSurface := nextStatistics
		if statisticsLayoutSurface == nil {
			statisticsLayoutSurface = app.statisticsWindow.CurrentSurface
		}
		usageToastLayoutSurface := nextUsageToast
		if usageToastLayoutSurface == nil {
			usageToastLayoutSurface = app.usageToastWindow.CurrentSurface
		}
		auxiliaryPositions = app.dockedAuxiliaryPositions(
			geometry,
			area,
			surface,
			statisticsLayoutSurface,
			usageToastLayoutSurface,
			windowRoleUnknown,
		)
	}
	updates = app.appendAuxiliaryUpdate(
		updates,
		&app.statisticsWindow,
		nextStatistics,
		auxiliaryPositions,
		geometryPoint{X: geometry.X, Y: geometry.Y},
	)
	updates = app.appendAuxiliaryUpdate(
		updates,
		&app.usageToastWindow,
		nextUsageToast,
		auxiliaryPositions,
		geometryPoint{X: geometry.X, Y: geometry.Y},
	)
	if err := app.applyWindowUpdates(updates); err != nil {
		log.Printf("keeping previous UI after render error: %v", err)
		return
	}
	if wasAnimating {
		procKillTimer.Call(app.window, animationTimerID)
		app.animation = windowAnimation{}
	}
	if clearCollapsedState {
		app.collapsed = false
	}
	if updateExpandedPosition {
		app.expandedPosition = nextExpandedPosition
		app.hasExpandedPosition = true
	}
	app.currentSurface = surface
	if nextStatistics != nil {
		app.statisticsWindow.CurrentSurface = nextStatistics
		app.statisticsWindow.Dirty = false
	}
	if nextUsageToast != nil {
		app.usageToastWindow.SurfaceID = nextUsageToastSurfaceID
		app.usageToastWindow.CurrentSurface = nextUsageToast
		app.usageToastWindow.Dirty = false
	}
	log.Printf(
		"loaded %s at %.0f%% (%dx%d), page BUILD %s, static %s, windows=%d",
		surface.Surface.ID,
		surface.Variant.Scale*100,
		surface.Variant.Width,
		surface.Variant.Height,
		surface.Manifest.Version.Build,
		surface.Manifest.Version.StaticVersion,
		len(updates),
	)
}

func (app *nativeApp) shouldComposeAuxiliaryStatus(auxiliary *auxiliaryWindow) bool {
	if auxiliary == nil || auxiliary.Handle == 0 {
		return false
	}
	if auxiliary.Role == windowRoleUsageToast {
		return auxiliary.Visibility == auxiliaryVisible && !app.accountExpiryToastActive
	}
	return auxiliary.Visibility == auxiliaryVisible
}

func (app *nativeApp) loadAuxiliarySurface(
	manifest bundleManifest,
	auxiliary *auxiliaryWindow,
	dpi uint32,
) (*renderedSurface, error) {
	if auxiliary.Handle == 0 {
		return nil, nil
	}
	return app.loadComposedSurface(manifest, auxiliary.SurfaceID, dpi)
}

func (app *nativeApp) auxiliaryDPI(auxiliary *auxiliaryWindow, mainDPI uint32) uint32 {
	if auxiliary != nil && auxiliary.Handle != 0 {
		return app.windowDPI(auxiliary.Handle)
	}
	return mainDPI
}

func (app *nativeApp) loadComposedSurface(
	manifest bundleManifest,
	surfaceID string,
	dpi uint32,
) (*renderedSurface, error) {
	return app.loadComposedSurfaceWithPresentation(manifest, surfaceID, dpi, nil)
}

func (app *nativeApp) loadComposedSurfaceWithPresentation(
	manifest bundleManifest,
	surfaceID string,
	dpi uint32,
	presentation *uiPresentation,
) (*renderedSurface, error) {
	targetScale := float64(dpi) / 96
	if app.appearance != nil {
		targetScale = app.appearance.targetScale(dpi)
	}
	surface := app.baseSurfaces[surfaceID]
	if !surfaceMatches(surface, manifest, targetScale) {
		loaded, err := loadRenderedSurfaceFromManifestAtScale(
			app.bundleRoot,
			manifest,
			surfaceID,
			targetScale,
		)
		if err != nil {
			return nil, err
		}
		surface = loaded
		if app.baseSurfaces == nil {
			app.baseSurfaces = map[string]*renderedSurface{}
		}
		app.baseSurfaces[surfaceID] = surface
	}
	if presentation != nil {
		return composeRenderedSurfaceWithPresentation(surface, *presentation)
	}
	return app.composeSurface(surface)
}

func surfaceMatches(
	surface *renderedSurface,
	manifest bundleManifest,
	targetScale float64,
) bool {
	if surface == nil || surface.Variant.Scale != targetScale {
		return false
	}
	currentVersion := surface.Manifest.Version
	nextVersion := manifest.Version
	return currentVersion.Build == nextVersion.Build &&
		currentVersion.StaticVersion == nextVersion.StaticVersion
}

func (app *nativeApp) composeSurface(surface *renderedSurface) (*renderedSurface, error) {
	if app.status == nil {
		return surface, nil
	}
	return app.status.compose(surface)
}

func (app *nativeApp) appendAuxiliaryUpdate(
	updates []windowRenderUpdate,
	auxiliary *auxiliaryWindow,
	surface *renderedSurface,
	positions map[windowRole]windowGeometry,
	fallback geometryPoint,
) []windowRenderUpdate {
	if auxiliary.Handle == 0 || surface == nil {
		return updates
	}
	position := app.currentWindowPositionOr(auxiliary.Handle, fallback)
	if geometry, ok := positions[auxiliary.Role]; ok {
		position = geometryPoint{X: geometry.X, Y: geometry.Y}
	} else if auxiliary.Role == windowRoleStatistics && app.statisticsDetached {
		geometry := clampGeometry(
			windowGeometry{
				X:      position.X,
				Y:      position.Y,
				Width:  surface.Variant.Width,
				Height: surface.Variant.Height,
			},
			enumerateWorkAreas(),
		)
		position = geometryPoint{X: geometry.X, Y: geometry.Y}
	}
	return append(updates, windowRenderUpdate{
		Window:           auxiliary.Handle,
		Surface:          surface,
		Position:         position,
		PreviousSurface:  auxiliary.CurrentSurface,
		PreviousPosition: app.currentWindowPositionOr(auxiliary.Handle, fallback),
	})
}

func (app *nativeApp) applyWindowUpdates(updates []windowRenderUpdate) error {
	app.updatingWindows = true
	defer func() {
		app.updatingWindows = false
	}()

	for index, update := range updates {
		if err := updateLayeredWindow(
			update.Window,
			update.Surface.Image,
			update.Position,
		); err != nil {
			app.rollbackWindowUpdates(updates[:index])
			return err
		}
	}
	return nil
}

func (app *nativeApp) rollbackWindowUpdates(updates []windowRenderUpdate) {
	for _, update := range updates {
		if update.PreviousSurface == nil {
			continue
		}
		if err := updateLayeredWindow(
			update.Window,
			update.PreviousSurface.Image,
			update.PreviousPosition,
		); err != nil {
			log.Printf("rolling back hwnd=0x%x: %v", update.Window, err)
		}
	}
}

func (app *nativeApp) ensureAuxiliaryWindow(auxiliary *auxiliaryWindow) error {
	if auxiliary.Handle != 0 {
		valid, _, _ := procIsWindow.Call(auxiliary.Handle)
		if valid != 0 {
			if auxiliary.Dirty {
				return app.refreshAuxiliaryWindow(auxiliary)
			}
			return nil
		}
		auxiliary.Handle = 0
		auxiliary.CurrentSurface = nil
		auxiliary.Visibility = auxiliaryHidden
		auxiliary.Dirty = false
	}
	if app.currentSurface == nil {
		return errors.New("the main surface is unavailable")
	}

	dpi := app.windowDPI(app.window)
	surface, err := app.loadComposedSurface(
		app.currentSurface.Manifest,
		auxiliary.SurfaceID,
		dpi,
	)
	if err != nil {
		return err
	}
	mainGeometry, ok := app.currentGeometry()
	if !ok {
		return errors.New("the main window geometry is unavailable")
	}
	position := geometryPoint{X: mainGeometry.X, Y: mainGeometry.Y}
	if area, found := app.currentWorkArea(); found {
		statisticsSurface := app.statisticsWindow.CurrentSurface
		usageToastSurface := app.usageToastWindow.CurrentSurface
		switch auxiliary.Role {
		case windowRoleStatistics:
			statisticsSurface = surface
		case windowRoleUsageToast:
			usageToastSurface = surface
		}
		positions := app.dockedAuxiliaryPositions(
			mainGeometry,
			area,
			app.currentSurface,
			statisticsSurface,
			usageToastSurface,
			auxiliary.Role,
		)
		if geometry, exists := positions[auxiliary.Role]; exists {
			position = geometryPoint{X: geometry.X, Y: geometry.Y}
		}
	}
	if auxiliary.Role == windowRoleStatistics && app.statisticsDetached &&
		app.appearance != nil && app.appearance.current.StatisticsWindow != nil {
		saved := app.appearance.current.StatisticsWindow
		geometry := clampGeometry(
			windowGeometry{
				X:      saved.X,
				Y:      saved.Y,
				Width:  surface.Variant.Width,
				Height: surface.Variant.Height,
			},
			enumerateWorkAreas(),
		)
		position = geometryPoint{X: geometry.X, Y: geometry.Y}
	}

	extendedStyle := auxiliaryWindowExtendedStyle(auxiliary.Role)
	className := utf16Pointer(app.windowClass)
	title := utf16Pointer(auxiliary.Title)
	window, _, lastErr := procCreateWindowExW.Call(
		extendedStyle,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsPopup,
		signed(int32(position.X)),
		signed(int32(position.Y)),
		signed(int32(surface.Variant.Width)),
		signed(int32(surface.Variant.Height)),
		app.window,
		0,
		app.instance,
		0,
	)
	if window == 0 {
		return callError("CreateWindowExW", lastErr)
	}
	auxiliary.Handle = window
	actualDPI := app.windowDPI(window)
	if actualDPI != dpi {
		surface, err = app.loadComposedSurface(
			app.currentSurface.Manifest,
			auxiliary.SurfaceID,
			actualDPI,
		)
		if err != nil {
			procDestroyWindow.Call(window)
			auxiliary.Handle = 0
			return err
		}
		geometry := clampGeometry(
			windowGeometry{
				X:      position.X,
				Y:      position.Y,
				Width:  surface.Variant.Width,
				Height: surface.Variant.Height,
			},
			enumerateWorkAreas(),
		)
		position = geometryPoint{X: geometry.X, Y: geometry.Y}
	}
	if err := updateLayeredWindow(window, surface.Image, position); err != nil {
		procDestroyWindow.Call(window)
		auxiliary.Handle = 0
		return err
	}
	auxiliary.CurrentSurface = surface
	auxiliary.Visibility = auxiliaryHidden
	auxiliary.Dirty = false
	log.Printf(
		"created %s hwnd=0x%x at %.0f%%, page BUILD %s, static %s",
		auxiliary.SurfaceID,
		window,
		surface.Variant.Scale*100,
		surface.Manifest.Version.Build,
		surface.Manifest.Version.StaticVersion,
	)
	return nil
}

func (app *nativeApp) refreshAuxiliaryWindow(auxiliary *auxiliaryWindow) error {
	if auxiliary == nil || auxiliary.Handle == 0 || app.currentSurface == nil {
		return errors.New("the auxiliary window is unavailable")
	}
	surface, err := app.loadComposedSurface(
		app.currentSurface.Manifest,
		auxiliary.SurfaceID,
		app.windowDPI(auxiliary.Handle),
	)
	if err != nil {
		return err
	}
	position := app.currentWindowPosition(auxiliary.Handle)
	alpha := byte(255)
	if auxiliary.Role == windowRoleUsageToast && app.usageToastAnimation.active {
		alpha = app.usageToastAnimation.currentAlpha
	}
	if err := updateLayeredWindowWithAlpha(
		auxiliary.Handle,
		surface.Image,
		position,
		alpha,
	); err != nil {
		return err
	}
	auxiliary.CurrentSurface = surface
	auxiliary.Dirty = false
	return nil
}

func (app *nativeApp) toggleStatistics() {
	app.expandImmediately()
	if err := app.ensureAuxiliaryWindow(&app.statisticsWindow); err != nil {
		log.Printf("showing statistics: %v", err)
		return
	}
	if app.statisticsWindow.Visibility != auxiliaryHidden ||
		nativeWindowVisible(app.statisticsWindow.Handle) {
		app.hideAuxiliaryWindow(&app.statisticsWindow)
		return
	}
	app.repositionAuxiliaryWindows(windowRoleStatistics)
	procShowWindow.Call(app.statisticsWindow.Handle, swShow)
	app.statisticsWindow.Visibility = auxiliaryVisible
	app.repositionAuxiliaryWindows(windowRoleUnknown)
}

func (app *nativeApp) showUsageToast() {
	if app.accountExpiryToastActive {
		app.restoreAccountExpiryToast()
		if app.usageToastWindow.Visibility != auxiliaryHidden {
			app.adoptRenderedUsageToastAsVisible()
		}
	}
	app.displayUsageToast()
}

func (app *nativeApp) displayUsageToast() {
	app.expandImmediately()
	if err := app.ensureAuxiliaryWindow(&app.usageToastWindow); err != nil {
		log.Printf("showing usage toast: %v", err)
		return
	}
	if app.accountExpiryToastActive &&
		app.usageToastWindow.Visibility != auxiliaryHidden {
		app.adoptRenderedUsageToastAsVisible()
		return
	}
	app.startUsageToastShow(time.Now())
}

func (app *nativeApp) adoptRenderedUsageToastAsVisible() {
	toast := &app.usageToastWindow
	if toast.Handle == 0 {
		return
	}
	procKillTimer.Call(toast.Handle, usageToastAnimationTimerID)
	position := app.currentWindowPosition(toast.Handle)
	toast.Visibility = auxiliaryVisible
	app.usageToastAnimation = toastWindowAnimation{
		stablePosition:  position,
		currentPosition: position,
		currentAlpha:    255,
	}
	procShowWindow.Call(toast.Handle, swShowNoActivate)
	app.startUsageToastAutoHideTimer()
}

func (app *nativeApp) toggleUsageToast() {
	if app.usageToastWindow.Visibility == auxiliaryShowing ||
		app.usageToastWindow.Visibility == auxiliaryVisible {
		app.hideAuxiliaryWindow(&app.usageToastWindow)
		return
	}
	app.showUsageToast()
}

func (app *nativeApp) hideAuxiliaryWindow(auxiliary *auxiliaryWindow) {
	if auxiliary.Handle == 0 {
		return
	}
	if auxiliary.Role == windowRoleUsageToast {
		app.startUsageToastHide(time.Now())
		return
	}
	procShowWindow.Call(auxiliary.Handle, swHide)
	auxiliary.Visibility = auxiliaryHidden
	app.repositionAuxiliaryWindows(windowRoleUnknown)
}

func (app *nativeApp) startUsageToastShow(now time.Time) {
	toast := &app.usageToastWindow
	if toast.Handle == 0 || toast.CurrentSurface == nil {
		return
	}
	procKillTimer.Call(toast.Handle, usageToastTimerID)
	nextVisibility := transitionToastVisibility(toast.Visibility, toastShowRequested)
	if nextVisibility == toast.Visibility {
		switch toast.Visibility {
		case auxiliaryVisible:
			app.startUsageToastAutoHideTimer()
		case auxiliaryShowing:
		}
		return
	}

	stable := app.usageToastAnimation.stablePosition
	start := app.currentWindowPosition(toast.Handle)
	startAlpha := byte(0)
	if toast.Visibility == auxiliaryHiding && app.usageToastAnimation.active {
		start = app.usageToastAnimation.currentPosition
		startAlpha = app.usageToastAnimation.currentAlpha
	} else {
		app.repositionAuxiliaryWindows(windowRoleUsageToast)
		stable = app.currentWindowPosition(toast.Handle)
		if !app.animationsEnabled {
			app.showUsageToastImmediately(stable)
			return
		}
		distance := max(
			1,
			int(float64(usageToastOffsetLogical)*toast.CurrentSurface.Variant.Scale+0.5),
		)
		mainGeometry, ok := app.currentGeometry()
		if ok {
			start = toastShowStartPosition(
				stable,
				mainGeometry,
				geometrySize{
					Width:  toast.CurrentSurface.Variant.Width,
					Height: toast.CurrentSurface.Variant.Height,
				},
				distance,
			)
		} else {
			start = stable
		}
		if err := updateLayeredWindowWithAlpha(
			toast.Handle,
			toast.CurrentSurface.Image,
			start,
			0,
		); err != nil {
			log.Printf("preparing usage toast animation: %v", err)
			app.showUsageToastImmediately(stable)
			return
		}
		procShowWindow.Call(toast.Handle, swShowNoActivate)
	}

	if !app.animationsEnabled {
		app.showUsageToastImmediately(stable)
		return
	}
	app.beginUsageToastAnimation(toastAnimationRequest{
		startedAt:      now,
		startPosition:  start,
		targetPosition: stable,
		stablePosition: stable,
		startAlpha:     startAlpha,
		targetAlpha:    255,
		targetState:    auxiliaryVisible,
		baseDuration:   usageToastShowDuration,
	})
}

func (app *nativeApp) startUsageToastHide(now time.Time) {
	toast := &app.usageToastWindow
	if toast.Handle == 0 ||
		transitionToastVisibility(toast.Visibility, toastHideRequested) == toast.Visibility {
		return
	}
	procKillTimer.Call(toast.Handle, usageToastTimerID)
	position := app.currentWindowPosition(toast.Handle)
	alpha := byte(255)
	stable := position
	if app.usageToastAnimation.active {
		position = app.usageToastAnimation.currentPosition
		alpha = app.usageToastAnimation.currentAlpha
		stable = app.usageToastAnimation.stablePosition
	}
	if !app.animationsEnabled {
		app.hideUsageToastImmediately()
		return
	}
	app.beginUsageToastAnimation(toastAnimationRequest{
		startedAt:      now,
		startPosition:  position,
		targetPosition: position,
		stablePosition: stable,
		startAlpha:     alpha,
		targetAlpha:    0,
		targetState:    auxiliaryHidden,
		baseDuration:   usageToastHideDuration,
	})
}

func (app *nativeApp) beginUsageToastAnimation(request toastAnimationRequest) {
	duration := scaledAlphaDuration(
		request.baseDuration,
		request.startAlpha,
		request.targetAlpha,
	)
	app.usageToastAnimation = toastWindowAnimation{
		active:          true,
		startedAt:       request.startedAt,
		duration:        duration,
		startPosition:   request.startPosition,
		targetPosition:  request.targetPosition,
		stablePosition:  request.stablePosition,
		currentPosition: request.startPosition,
		startAlpha:      request.startAlpha,
		targetAlpha:     request.targetAlpha,
		currentAlpha:    request.startAlpha,
		targetState:     request.targetState,
	}
	if request.targetState == auxiliaryVisible {
		app.usageToastWindow.Visibility = transitionToastVisibility(
			app.usageToastWindow.Visibility,
			toastShowRequested,
		)
	} else {
		app.usageToastWindow.Visibility = transitionToastVisibility(
			app.usageToastWindow.Visibility,
			toastHideRequested,
		)
	}
	interval := uint32(usageToastAnimationTick / time.Millisecond)
	if !setNativeTimer(app.usageToastWindow.Handle, usageToastAnimationTimerID, interval) {
		app.settleUsageToastAnimation()
	}
}

func scaledAlphaDuration(base time.Duration, start byte, target byte) time.Duration {
	distance := abs(int(target) - int(start))
	if distance == 0 {
		return 0
	}
	return max(time.Millisecond, time.Duration(int64(base)*int64(distance)/255))
}

func (app *nativeApp) advanceUsageToastAnimation() {
	app.advanceUsageToastAnimationAt(time.Now())
}

func (app *nativeApp) advanceUsageToastAnimationAt(now time.Time) {
	animation := app.usageToastAnimation
	if !animation.active {
		procKillTimer.Call(app.usageToastWindow.Handle, usageToastAnimationTimerID)
		return
	}
	position, alpha, complete := toastAnimationFrame(animation, now)
	if app.usageToastWindow.CurrentSurface == nil {
		app.settleUsageToastAnimation()
		return
	}
	if err := updateLayeredWindowWithAlpha(
		app.usageToastWindow.Handle,
		app.usageToastWindow.CurrentSurface.Image,
		position,
		alpha,
	); err != nil {
		log.Printf("animating usage toast: %v", err)
		app.settleUsageToastAnimation()
		return
	}
	app.usageToastAnimation.currentPosition = position
	app.usageToastAnimation.currentAlpha = alpha
	if complete {
		app.settleUsageToastAnimation()
	}
}

func toastAnimationFrame(
	animation toastWindowAnimation,
	now time.Time,
) (geometryPoint, byte, bool) {
	progress := timedAnimationProgress(animation.startedAt, animation.duration, now)
	position := interpolateAnimationPosition(
		animation.startPosition,
		animation.targetPosition,
		progress,
	)
	alpha := interpolateAnimationAlpha(animation.startAlpha, animation.targetAlpha, progress)
	return position, alpha, progress >= 1
}

func interpolateAnimationAlpha(start byte, target byte, progress float64) byte {
	progress = max(0, min(progress, 1))
	value := float64(start) + float64(int(target)-int(start))*progress
	return byte(max(0, min(255, int(value+0.5))))
}

func toastShowStartPosition(
	stable geometryPoint,
	anchor windowGeometry,
	toast geometrySize,
	distance int,
) geometryPoint {
	distance = max(0, distance)
	anchorCenterX := anchor.X*2 + anchor.Width
	anchorCenterY := anchor.Y*2 + anchor.Height
	toastCenterX := stable.X*2 + toast.Width
	toastCenterY := stable.Y*2 + toast.Height
	deltaX := toastCenterX - anchorCenterX
	deltaY := toastCenterY - anchorCenterY
	start := stable
	if abs(deltaX) > abs(deltaY) {
		if deltaX < 0 {
			start.X += distance
		} else {
			start.X -= distance
		}
		return start
	}
	if deltaY < 0 {
		start.Y += distance
	} else {
		start.Y -= distance
	}
	return start
}

func (app *nativeApp) settleUsageToastAnimation() {
	if !app.usageToastAnimation.active {
		return
	}
	if app.usageToastAnimation.targetState == auxiliaryVisible {
		app.showUsageToastImmediately(app.usageToastAnimation.stablePosition)
		return
	}
	app.hideUsageToastImmediately()
}

func (app *nativeApp) showUsageToastImmediately(position geometryPoint) {
	toast := &app.usageToastWindow
	procKillTimer.Call(toast.Handle, usageToastAnimationTimerID)
	if toast.Handle == 0 || toast.CurrentSurface == nil {
		toast.Visibility = auxiliaryHidden
		app.usageToastAnimation = toastWindowAnimation{}
		return
	}
	if toast.Dirty && !app.accountExpiryToastActive {
		if err := app.refreshAuxiliaryWindow(toast); err != nil {
			log.Printf("refreshing usage toast before show completion: %v", err)
		}
	}
	if err := updateLayeredWindowWithAlpha(
		toast.Handle,
		toast.CurrentSurface.Image,
		position,
		255,
	); err != nil {
		log.Printf("finishing usage toast show: %v", err)
	}
	procShowWindow.Call(toast.Handle, swShowNoActivate)
	toast.Visibility = auxiliaryVisible
	app.usageToastAnimation = toastWindowAnimation{
		stablePosition:  position,
		currentPosition: position,
		currentAlpha:    255,
	}
	app.startUsageToastAutoHideTimer()
}

func (app *nativeApp) hideUsageToastImmediately() {
	toast := &app.usageToastWindow
	if toast.Handle == 0 {
		toast.Visibility = auxiliaryHidden
		app.usageToastAnimation = toastWindowAnimation{}
		return
	}
	procKillTimer.Call(toast.Handle, usageToastAnimationTimerID)
	procKillTimer.Call(toast.Handle, usageToastTimerID)
	procShowWindow.Call(toast.Handle, swHide)
	toast.Visibility = auxiliaryHidden
	app.usageToastAnimation = toastWindowAnimation{}
	if app.accountExpiryToastActive {
		app.restoreAccountExpiryToast()
		if !app.accountExpiryToastActive {
			toast.Dirty = false
		}
	}
	app.repositionAuxiliaryWindows(windowRoleUnknown)
}

func (app *nativeApp) startUsageToastAutoHideTimer() {
	if app.usageToastWindow.Handle == 0 ||
		app.usageToastWindow.Visibility != auxiliaryVisible {
		return
	}
	procKillTimer.Call(app.usageToastWindow.Handle, usageToastTimerID)
	setNativeTimer(
		app.usageToastWindow.Handle,
		usageToastTimerID,
		uint32(usageToastVisibleDuration/time.Millisecond),
	)
}

func (app *nativeApp) hideAllWindows() {
	app.manuallyHidden = true
	app.hideWindows()
}

func (app *nativeApp) hideForCodexUnavailable() {
	app.hideWindows()
}

func (app *nativeApp) hideWindows() {
	app.expandImmediately()
	app.hideAuxiliaryWindow(&app.statisticsWindow)
	app.hideUsageToastImmediately()
	app.syncAutoCollapseTimer(false)
	procShowWindow.Call(app.window, swHide)
}

func (app *nativeApp) showMainWindow() {
	app.manuallyHidden = false
	app.expandImmediately()
	procShowWindow.Call(app.window, swRestore)
	procShowWindow.Call(app.window, swShow)
	app.syncAutoCollapseTimer(nativeWindowVisible(app.window))
	procSetForegroundWindow.Call(app.window)
}

func (app *nativeApp) showMainWindowForCodex() {
	if app.manuallyHidden {
		return
	}
	app.expandImmediately()
	procShowWindow.Call(app.window, swShowNoActivate)
	app.syncAutoCollapseTimer(nativeWindowVisible(app.window))
}

func (app *nativeApp) applyCodexProcessStatus(running bool, visible bool) {
	stateKnown := app.codexStateKnown
	wasAvailable := app.codexWasRunning && app.codexWasVisible
	available := running && visible
	app.codexStateKnown = true
	app.codexWasRunning = running
	app.codexWasVisible = visible
	log.Printf("Codex desktop state: running=%t visible=%t", running, visible)
	if !app.followCodexEnabled {
		return
	}
	switch codexProcessVisibilityAction(
		stateKnown,
		wasAvailable,
		available,
		app.manuallyHidden,
	) {
	case codexVisibilityShow:
		app.showMainWindowForCodex()
	case codexVisibilityHide:
		app.hideForCodexUnavailable()
	}
}

func (app *nativeApp) destroyAuxiliaryWindows() {
	for _, auxiliary := range []*auxiliaryWindow{
		&app.statisticsWindow,
		&app.usageToastWindow,
	} {
		if auxiliary.Handle == 0 {
			continue
		}
		if auxiliary.Role == windowRoleUsageToast {
			procKillTimer.Call(auxiliary.Handle, usageToastAnimationTimerID)
			procKillTimer.Call(auxiliary.Handle, usageToastTimerID)
			auxiliary.Visibility = auxiliaryHidden
			app.usageToastAnimation = toastWindowAnimation{}
		}
		procDestroyWindow.Call(auxiliary.Handle)
	}
}

func (app *nativeApp) repositionAuxiliaryWindows(pending windowRole) {
	if app.updatingWindows || app.window == 0 || app.currentSurface == nil {
		return
	}
	mainGeometry, ok := app.currentGeometry()
	if !ok {
		return
	}
	area, ok := app.currentWorkArea()
	if !ok {
		return
	}
	positions := app.dockedAuxiliaryPositions(
		mainGeometry,
		area,
		app.currentSurface,
		app.statisticsWindow.CurrentSurface,
		app.usageToastWindow.CurrentSurface,
		pending,
	)
	for role, geometry := range positions {
		auxiliary := app.auxiliaryForRole(role)
		if auxiliary == nil || auxiliary.Handle == 0 {
			continue
		}
		position := geometryPoint{X: geometry.X, Y: geometry.Y}
		if role == windowRoleUsageToast && app.usageToastAnimation.active {
			app.retargetUsageToastAnimation(position)
			continue
		}
		procSetWindowPos.Call(
			auxiliary.Handle,
			0,
			signed(int32(geometry.X)),
			signed(int32(geometry.Y)),
			0,
			0,
			swpNoSize|swpNoZOrder|swpNoActivate,
		)
		if role == windowRoleUsageToast && auxiliary.Visibility == auxiliaryVisible {
			app.usageToastAnimation.stablePosition = position
			app.usageToastAnimation.currentPosition = position
		}
	}
}

func (app *nativeApp) retargetUsageToastAnimation(stable geometryPoint) {
	animation := &app.usageToastAnimation
	if !animation.active {
		return
	}
	delta := geometryPoint{
		X: stable.X - animation.stablePosition.X,
		Y: stable.Y - animation.stablePosition.Y,
	}
	animation.stablePosition = stable
	animation.startPosition.X += delta.X
	animation.startPosition.Y += delta.Y
	animation.targetPosition.X += delta.X
	animation.targetPosition.Y += delta.Y
	animation.currentPosition.X += delta.X
	animation.currentPosition.Y += delta.Y
	if app.usageToastWindow.CurrentSurface == nil {
		return
	}
	if err := updateLayeredWindowWithAlpha(
		app.usageToastWindow.Handle,
		app.usageToastWindow.CurrentSurface.Image,
		animation.currentPosition,
		animation.currentAlpha,
	); err != nil {
		log.Printf("retargeting usage toast animation: %v", err)
	}
}

func (app *nativeApp) dockedAuxiliaryPositions(
	mainGeometry windowGeometry,
	area workArea,
	mainSurface *renderedSurface,
	statisticsSurface *renderedSurface,
	usageToastSurface *renderedSurface,
	pending windowRole,
) map[windowRole]windowGeometry {
	roles := []windowRole{}
	sizes := []geometrySize{}
	appendSurface := func(
		auxiliary *auxiliaryWindow,
		surface *renderedSurface,
	) {
		if surface == nil {
			return
		}
		if auxiliary.Role == windowRoleStatistics && app.statisticsDetached {
			return
		}
		visible := auxiliary.Handle != 0 && auxiliary.Visibility != auxiliaryHidden
		if !visible && auxiliary.Role != pending {
			return
		}
		roles = append(roles, auxiliary.Role)
		sizes = append(sizes, geometrySize{
			Width:  surface.Variant.Width,
			Height: surface.Variant.Height,
		})
	}
	appendSurface(&app.statisticsWindow, statisticsSurface)
	appendSurface(&app.usageToastWindow, usageToastSurface)

	positions := make(map[windowRole]windowGeometry, len(roles))
	if len(roles) == 0 {
		return positions
	}
	gap := auxiliaryGapLogical
	vertical := false
	if mainSurface != nil {
		gap = max(1, int(float64(auxiliaryGapLogical)*mainSurface.Variant.Scale+0.5))
		vertical = strings.HasPrefix(mainSurface.Surface.ID, "main-vertical")
	}
	geometries := dockAuxiliaryStack(mainGeometry, sizes, area, vertical, gap)
	for index, role := range roles {
		positions[role] = geometries[index]
	}
	return positions
}

func (app *nativeApp) auxiliaryForRole(role windowRole) *auxiliaryWindow {
	switch role {
	case windowRoleStatistics:
		return &app.statisticsWindow
	case windowRoleUsageToast:
		return &app.usageToastWindow
	default:
		return nil
	}
}

func (app *nativeApp) auxiliaryForWindow(window uintptr) *auxiliaryWindow {
	if window != 0 && window == app.statisticsWindow.Handle {
		return &app.statisticsWindow
	}
	if window != 0 && window == app.usageToastWindow.Handle {
		return &app.usageToastWindow
	}
	return nil
}

func (app *nativeApp) roleForWindow(window uintptr) windowRole {
	if window != 0 && window == app.window {
		return windowRoleMain
	}
	if auxiliary := app.auxiliaryForWindow(window); auxiliary != nil {
		return auxiliary.Role
	}
	return windowRoleUnknown
}

func (app *nativeApp) surfaceForWindow(window uintptr) *renderedSurface {
	if app.roleForWindow(window) == windowRoleMain {
		return app.currentSurface
	}
	if auxiliary := app.auxiliaryForWindow(window); auxiliary != nil {
		return auxiliary.CurrentSurface
	}
	return nil
}

func (app *nativeApp) windowDPI(window uintptr) uint32 {
	if window != 0 {
		if value, _, _ := procGetDpiForWindow.Call(window); value != 0 {
			return uint32(value)
		}
	}
	return 96
}

func (app *nativeApp) currentWindowPosition(window uintptr) geometryPoint {
	geometry, ok := app.currentGeometryForWindow(window)
	if !ok {
		return geometryPoint{}
	}
	return geometryPoint{X: geometry.X, Y: geometry.Y}
}

func (app *nativeApp) currentWindowPositionOr(
	window uintptr,
	fallback geometryPoint,
) geometryPoint {
	geometry, ok := app.currentGeometryForWindow(window)
	if !ok {
		return fallback
	}
	return geometryPoint{X: geometry.X, Y: geometry.Y}
}

func (app *nativeApp) watchBundle() {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	manifestPath := filepath.Join(app.bundleRoot, "manifest.json")

	for {
		select {
		case <-ticker.C:
			state, err := statFile(manifestPath)
			if err != nil || state == app.lastManifestState {
				continue
			}
			app.lastManifestState = state
			procPostMessageW.Call(app.window, wmNativeReload, 0, 0)
		case <-app.stopWatching:
			return
		}
	}
}

func (app *nativeApp) startBundleWatcher() {
	if !app.watchBundleEnabled {
		return
	}
	manifestPath := filepath.Join(app.bundleRoot, "manifest.json")
	if state, err := statFile(manifestPath); err == nil {
		app.lastManifestState = state
	}
	go app.watchBundle()
}

func (app *nativeApp) stopWatcher() {
	app.stopOnce.Do(func() {
		close(app.stopWatching)
	})
}

func statFile(path string) (fileState, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileState{}, err
	}
	return fileState{Size: info.Size(), ModifiedAt: info.ModTime()}, nil
}

func (app *nativeApp) addTrayIcon() error {
	icon, err := app.loadApplicationIcon()
	if err != nil {
		return err
	}

	app.tray = notifyIconData{
		Window:          app.window,
		ID:              1,
		Flags:           nifMessage | nifIcon | nifTip,
		CallbackMessage: wmNativeTray,
		Icon:            icon,
	}
	copy(app.tray.Tip[:], windows.StringToUTF16(appidentity.ProductName))
	sizes := []uint32{
		uint32(unsafe.Sizeof(notifyIconData{})),
		uint32(unsafe.Offsetof(notifyIconData{}.BalloonIcon)),
		uint32(unsafe.Offsetof(notifyIconData{}.ItemGUID)),
	}
	var lastErr error
	for _, size := range sizes {
		app.tray.Size = size
		result, _, callErr := procShellNotifyIconW.Call(
			nimAdd,
			uintptr(unsafe.Pointer(&app.tray)),
		)
		if result != 0 {
			app.tray.Version = notifyVersion4
			versionResult, _, _ := procShellNotifyIconW.Call(
				nimSetVersion,
				uintptr(unsafe.Pointer(&app.tray)),
			)
			app.trayVersion4 = versionResult != 0
			return nil
		}
		lastErr = callErr
	}
	return callError("Shell_NotifyIconW(NIM_ADD)", lastErr)
}

func (app *nativeApp) loadApplicationIcon() (uintptr, error) {
	icon, _, lastErr := procLoadIconW.Call(
		app.instance,
		applicationIconResourceID,
	)
	if icon == 0 {
		return 0, callError("LoadIconW(application icon)", lastErr)
	}
	return icon, nil
}

func mainWindowExtendedStyle() uintptr {
	return wsExLayered | wsExTopmost | wsExToolWindow
}

func auxiliaryWindowExtendedStyle(role windowRole) uintptr {
	style := uintptr(wsExLayered | wsExTopmost | wsExToolWindow)
	if role == windowRoleUsageToast {
		style |= wsExNoActivate
	}
	return style
}

func (app *nativeApp) deleteTrayIcon() {
	procKillTimer.Call(app.window, trayRetryTimerID)
	if app.tray.Window == 0 {
		return
	}
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&app.tray)))
	app.trayVersion4 = false
}

func (app *nativeApp) ensureTrayIcon() error {
	err := app.addTrayIcon()
	if err == nil {
		app.trayRetryAttempts = 0
		procKillTimer.Call(app.window, trayRetryTimerID)
		return nil
	}
	app.trayRetryAttempts++
	if app.trayRetryAttempts < trayRetryLimit {
		setNativeTimer(app.window, trayRetryTimerID, 1000)
	} else {
		procKillTimer.Call(app.window, trayRetryTimerID)
	}
	return err
}

func (app *nativeApp) handleTrayEvent(event uint32) {
	if app.trayVersion4 {
		switch event {
		case wmContextMenu:
			app.showTrayMenu()
		case ninSelect, ninKeySelect, wmLButtonDblClk:
			app.showMainWindow()
		}
		return
	}

	switch event {
	case wmRButtonUp:
		app.showTrayMenu()
	case wmLButtonDblClk:
		app.showMainWindow()
	}
}

func (app *nativeApp) showTrayMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	appendMenu(menu, mfString, trayCommandRefreshStatus, "刷新状态")
	appendMenu(menu, mfString, trayCommandReloadUI, "重载 UI")
	appendMenu(menu, mfString, trayCommandCopyStatus, "复制当前状态")
	appendMenu(
		menu,
		checkedMenuFlags(nativeWindowVisible(app.window)),
		trayCommandVisible,
		"显示/隐藏窗口",
	)
	appendMenu(menu, mfString, trayCommandOpenConfig, "打开配置文件")
	appendMenu(menu, mfString, trayCommandOpenChatGPT, "打开 ChatGPT 账户页")
	appendMenu(menu, mfString, trayCommandOpenBilling, "打开 Billing 页面")
	appendMenu(
		menu,
		mfString,
		trayCommandAccountExpiryDate,
		accountExpiryMenuText(app.accountExpiryDate),
	)
	appendMenu(
		menu,
		checkedMenuFlags(app.accountExpiryReminderEnabled),
		trayCommandAccountExpiryReminder,
		"到期提醒",
	)
	appendMenu(menu, mfString, trayCommandOpenAPIUsage, "打开 API 用量页面")
	appendMenu(menu, mfString, trayCommandOpenAPIKeys, "打开 API Keys 页面")
	appendMenu(menu, mfString, trayCommandOpenGitHub, "打开 GitHub 仓库")
	appendMenu(menu, mfSeparator, 0, "")
	app.appendAppearanceMenus(menu)
	appendMenu(
		menu,
		checkedMenuFlags(app.autoCollapseEnabled),
		trayCommandAutoCollapse,
		"自动收起",
	)
	appendMenu(
		menu,
		checkedMenuFlags(app.followCodexEnabled),
		trayCommandFollowCodex,
		"跟随 Codex",
	)
	appendMenu(
		menu,
		checkedMenuFlags(app.startupEnabled()),
		trayCommandStartup,
		"开机自启动",
	)
	appendMenu(menu, mfSeparator, 0, "")
	appendMenu(
		menu,
		mfString|mfDisabled|mfGrayed,
		0,
		formatTrayBuild(app.startedAt),
	)
	appendMenu(menu, mfString, trayCommandExit, "退出")

	var cursor winPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor)))
	procSetForegroundWindow.Call(app.window)
	command, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmRightButton|tpmReturnCmd,
		signed(cursor.X),
		signed(cursor.Y),
		0,
		app.window,
		0,
	)
	procPostMessageW.Call(app.window, wmNull, 0, 0)
	procShellNotifyIconW.Call(nimSetFocus, uintptr(unsafe.Pointer(&app.tray)))

	app.executeTrayCommand(command)
}

func (app *nativeApp) appendAppearanceMenus(menu uintptr) {
	theme := appsettings.ThemeDark
	scale := 1.0
	if app.appearance != nil {
		theme = app.appearance.current.Theme
		scale = app.appearance.current.Scale
	}

	themeMenu, _, _ := procCreatePopupMenu.Call()
	if themeMenu != 0 {
		appendMenu(
			themeMenu,
			checkedMenuFlags(theme == appsettings.ThemeDark),
			trayCommandThemeDark,
			"黑色磨砂",
		)
		appendMenu(
			themeMenu,
			checkedMenuFlags(theme == appsettings.ThemeLight),
			trayCommandThemeLight,
			"灰白清爽",
		)
		if !appendMenu(menu, mfString|mfPopup, themeMenu, "配色") {
			procDestroyMenu.Call(themeMenu)
		}
	}

	layout := app.currentLayout()
	layoutMenu, _, _ := procCreatePopupMenu.Call()
	if layoutMenu != 0 {
		appendMenu(
			layoutMenu,
			checkedMenuFlags(layout == appsettings.LayoutHorizontal),
			trayCommandLayoutHorizontal,
			"横版",
		)
		appendMenu(
			layoutMenu,
			checkedMenuFlags(layout == appsettings.LayoutVertical),
			trayCommandLayoutVertical,
			"竖版",
		)
		if !appendMenu(menu, mfString|mfPopup, layoutMenu, "布局") {
			procDestroyMenu.Call(layoutMenu)
		}
	}

	scaleMenu, _, _ := procCreatePopupMenu.Call()
	if scaleMenu != 0 {
		appendMenu(
			scaleMenu,
			checkedMenuFlags(scaleNear(scale, 0.9)),
			trayCommandScale90,
			"小 90%",
		)
		appendMenu(
			scaleMenu,
			checkedMenuFlags(scaleNear(scale, 1.0)),
			trayCommandScale100,
			"标准 100%",
		)
		appendMenu(
			scaleMenu,
			checkedMenuFlags(scaleNear(scale, 1.1)),
			trayCommandScale110,
			"大 110%",
		)
		if !appendMenu(menu, mfString|mfPopup, scaleMenu, "缩放") {
			procDestroyMenu.Call(scaleMenu)
		}
	}
}

func checkedMenuFlags(checked bool) uintptr {
	flags := uintptr(mfString)
	if checked {
		flags |= mfChecked
	}
	return flags
}

func scaleNear(value float64, target float64) bool {
	const tolerance = 0.001
	return value >= target-tolerance && value <= target+tolerance
}

func (app *nativeApp) executeTrayCommand(command uintptr) {
	switch command {
	case trayCommandRefreshStatus:
		app.refreshStatus()
	case trayCommandReloadUI:
		app.reloadSurface()
	case trayCommandCopyStatus:
		app.copyCurrentStatus()
	case trayCommandVisible:
		app.toggleVisible()
	case trayCommandOpenConfig:
		if !openCodexConfigFile(app.window) {
			log.Print("opening Codex config from the tray failed")
		}
	case trayCommandOpenChatGPT:
		app.openExternalPage(externalPageChatGPT)
	case trayCommandOpenBilling:
		app.openExternalPage(externalPageBilling)
	case trayCommandAccountExpiryDate:
		app.editAccountExpiryDate()
	case trayCommandAccountExpiryReminder:
		app.toggleAccountExpiryReminder()
	case trayCommandOpenAPIUsage:
		app.openExternalPage(externalPageAPIUsage)
	case trayCommandOpenAPIKeys:
		app.openExternalPage(externalPageAPIKeys)
	case trayCommandOpenGitHub:
		app.openExternalPage(externalPageGitHub)
	case trayCommandThemeDark:
		app.setTheme(appsettings.ThemeDark)
	case trayCommandThemeLight:
		app.setTheme(appsettings.ThemeLight)
	case trayCommandLayoutHorizontal:
		app.setLayout(appsettings.LayoutHorizontal)
	case trayCommandLayoutVertical:
		app.setLayout(appsettings.LayoutVertical)
	case trayCommandScale90:
		app.setScale(0.9)
	case trayCommandScale100:
		app.setScale(1.0)
	case trayCommandScale110:
		app.setScale(1.1)
	case trayCommandAutoCollapse:
		app.toggleAutoCollapse()
	case trayCommandFollowCodex:
		app.toggleFollowCodex()
	case trayCommandStartup:
		app.toggleStartup()
	case trayCommandExit:
		procDestroyWindow.Call(app.window)
	}
}

func (app *nativeApp) refreshStatus() {
	if app.status == nil || !app.status.refresh() {
		log.Print("refreshing status: Codex data monitor is unavailable")
	}
}

func (app *nativeApp) copyCurrentStatus() {
	text := app.status.clipboardText(app.codexWasRunning)
	if err := writeClipboardText(app.window, text); err != nil {
		app.reportTrayError("复制当前状态", err)
		return
	}
	app.showTrayInfo("当前状态已复制到剪贴板。")
}

func (app *nativeApp) openExternalPage(page externalPage) {
	if !openExternalPage(app.window, page) {
		log.Printf("opening external page %d from the tray failed", page)
	}
}

func (app *nativeApp) startupEnabled() bool {
	enabled, err := newStartupService().IsEnabled()
	if err != nil {
		log.Printf("checking startup setting: %v", err)
		return false
	}
	return enabled
}

func (app *nativeApp) toggleStartup() {
	service := newStartupService()
	enabled, err := service.IsEnabled()
	if err != nil {
		app.reportTrayError("读取开机自启动状态", err)
		return
	}
	if err := service.SetEnabled(!enabled); err != nil {
		app.reportTrayError("修改开机自启动", err)
	}
}

func (app *nativeApp) reportTrayError(action string, err error) {
	log.Printf("%s: %v", action, err)
	showSystemActionError(app.window, fmt.Sprintf("%s失败：%v", action, err))
}

func (app *nativeApp) showTrayInfo(message string) {
	if app.tray.Window == 0 {
		return
	}
	notification := app.tray
	notification.Flags = nifInfo
	notification.Version = 1200
	notification.InfoFlags = niifInfo
	copy(notification.InfoTitle[:], windows.StringToUTF16("CodexFloatingBar"))
	copy(notification.Info[:], windows.StringToUTF16(message))
	result, _, lastErr := procShellNotifyIconW.Call(
		nimModify,
		uintptr(unsafe.Pointer(&notification)),
	)
	if result == 0 {
		log.Printf("showing tray notification: %v", lastErr)
	}
}

func (app *nativeApp) toggleVisible() {
	visible, _, _ := procIsWindowVisible.Call(app.window)
	if visible != 0 {
		app.hideAllWindows()
		return
	}
	app.showMainWindow()
}

func (app *nativeApp) actionAt(clientX int32, clientY int32) string {
	return actionAtSurface(app.currentSurface, clientX, clientY)
}

func (app *nativeApp) actionAtWindow(window uintptr, clientX int32, clientY int32) string {
	action := actionAtSurface(app.surfaceForWindow(window), clientX, clientY)
	role := app.roleForWindow(window)
	if role == windowRoleUsageToast &&
		toastAnimationBlocksInput(app.usageToastWindow.Visibility) {
		return ""
	}
	if role == windowRoleStatistics &&
		strings.HasPrefix(action, "statistics-select-day-") &&
		!app.statisticsDateActionEnabled(action) {
		return ""
	}
	return action
}

func toastAnimationBlocksInput(visibility auxiliaryVisibility) bool {
	return visibility == auxiliaryShowing || visibility == auxiliaryHiding
}

func (app *nativeApp) statisticsDateActionEnabled(action string) bool {
	if app.status == nil {
		return false
	}
	selection := normalizeStatisticsSelection(
		app.status.current,
		app.status.statistics,
	)
	_, valid := app.status.statisticsDayForAction(selection, action)
	return valid
}

func actionAtSurface(surface *renderedSurface, clientX int32, clientY int32) string {
	if surface == nil {
		return ""
	}

	scale := surface.Variant.Scale
	logicalX := float64(clientX) / scale
	logicalY := float64(clientY) / scale
	for _, region := range surface.Surface.HitRegions {
		insideX := logicalX >= region.X && logicalX < region.X+region.Width
		insideY := logicalY >= region.Y && logicalY < region.Y+region.Height
		if insideX && insideY {
			return region.Action
		}
	}
	return ""
}

func (app *nativeApp) executeAction(action string) {
	app.executeWindowAction(app.window, action)
}

func (app *nativeApp) executeWindowAction(window uintptr, action string) {
	if app.roleForWindow(window) == windowRoleUsageToast &&
		toastAnimationBlocksInput(app.usageToastWindow.Visibility) {
		return
	}
	if strings.HasPrefix(action, "statistics-select-day-") {
		if app.status != nil && app.status.applyStatisticsAction(action) {
			app.reloadSurface()
		}
		return
	}
	switch action {
	case "toggle-theme":
		app.toggleTheme()
	case "toggle-layout":
		layout := appsettings.LayoutHorizontal
		if app.currentLayout() == appsettings.LayoutHorizontal {
			layout = appsettings.LayoutVertical
		}
		app.setLayout(layout)
	case "hide":
		if app.roleForWindow(window) == windowRoleMain {
			app.hideAllWindows()
			return
		}
		if auxiliary := app.auxiliaryForWindow(window); auxiliary != nil {
			app.hideAuxiliaryWindow(auxiliary)
		}
	case "toggle-statistics":
		app.toggleStatistics()
	case "toggle-toast":
		app.toggleUsageToast()
	case "show-toast":
		app.showUsageToast()
	case "hide-toast":
		app.hideAuxiliaryWindow(&app.usageToastWindow)
	case "toggle-collapse":
		app.toggleAutoCollapse()
	case "statistics-view-month",
		"statistics-view-week",
		"statistics-view-cumulative",
		"statistics-view-detail",
		"statistics-previous-month",
		"statistics-next-month":
		if app.status != nil && app.status.applyStatisticsAction(action) {
			app.reloadSurface()
		}
	}
}

func (app *nativeApp) toggleAutoCollapse() {
	app.autoCollapseEnabled = !app.autoCollapseEnabled
	app.awayPolls = 0
	if app.autoCollapseEnabled {
		if !app.syncAutoCollapseTimer(nativeWindowVisible(app.window)) {
			return
		}
		log.Print("automatic edge collapse enabled")
		app.persistAutoCollapse()
		return
	}

	app.syncAutoCollapseTimer(nativeWindowVisible(app.window))
	if app.collapsed || app.animation.active {
		app.startExpandAnimation()
	}
	log.Print("automatic edge collapse disabled")
	app.persistAutoCollapse()
}

func (app *nativeApp) syncAutoCollapseTimer(mainVisible bool) bool {
	action := autoCollapseTimerSyncAction(
		app.autoCollapseEnabled,
		mainVisible,
		app.autoCollapseTimerRunning,
	)
	switch action {
	case timerSyncStart:
		if setNativeTimer(app.window, pollTimerID, 250) {
			app.autoCollapseTimerRunning = true
			return true
		}
		app.autoCollapseEnabled = false
		if app.appearance != nil {
			app.appearance.setAutoCollapse(false)
		}
		return false
	case timerSyncStop:
		procKillTimer.Call(app.window, pollTimerID)
		app.autoCollapseTimerRunning = false
	}
	return true
}

func (app *nativeApp) persistAutoCollapse() {
	if app.appearance == nil {
		return
	}
	app.appearance.setAutoCollapse(app.autoCollapseEnabled)
	if err := app.appearance.save(); err != nil {
		log.Printf("saving automatic collapse setting: %v", err)
	}
}

func (app *nativeApp) toggleFollowCodex() {
	app.followCodexEnabled = !app.followCodexEnabled
	app.persistFollowCodex()
	log.Printf("following Codex enabled=%t", app.followCodexEnabled)
	if !app.followCodexEnabled || !app.codexStateKnown {
		return
	}
	if app.codexWasRunning && app.codexWasVisible {
		app.showMainWindowForCodex()
		return
	}
	app.hideForCodexUnavailable()
}

func (app *nativeApp) persistFollowCodex() {
	if app.appearance == nil {
		return
	}
	app.appearance.setFollowCodex(app.followCodexEnabled)
	if err := app.appearance.save(); err != nil {
		log.Printf("saving follow Codex setting: %v", err)
	}
}

func (app *nativeApp) currentLayout() appsettings.Layout {
	if app.appearance != nil {
		return app.appearance.current.Layout
	}
	if strings.Contains(app.surfaceID, "vertical") {
		return appsettings.LayoutVertical
	}
	return appsettings.LayoutHorizontal
}

func (app *nativeApp) setLayout(layout appsettings.Layout) {
	if layout != appsettings.LayoutHorizontal && layout != appsettings.LayoutVertical {
		return
	}
	currentLayout := app.currentLayout()
	if layout == currentLayout {
		return
	}
	app.expandImmediately()
	app.surfaceOverride = false
	if app.appearance != nil {
		if geometry, ok := app.currentGeometry(); ok {
			app.appearance.setMainPositionForLayout(
				currentLayout,
				geometryPoint{X: geometry.X, Y: geometry.Y},
			)
		}
		targetPosition := app.appearance.mainPosition(layout)
		app.appearance.setLayout(layout)
		if targetPosition != nil {
			procSetWindowPos.Call(
				app.window,
				0,
				signed(int32(targetPosition.X)),
				signed(int32(targetPosition.Y)),
				0,
				0,
				swpNoSize|swpNoZOrder|swpNoActivate,
			)
		}
	}
	app.reloadSurface()
	app.savePlacement()
}

func (app *nativeApp) setTheme(theme appsettings.Theme) {
	if theme != appsettings.ThemeDark && theme != appsettings.ThemeLight {
		return
	}
	if app.appearance != nil {
		app.appearance.setTheme(theme)
	}
	app.surfaceOverride = false
	app.reloadSurface()
	app.savePlacement()
}

func (app *nativeApp) toggleTheme() {
	theme := appsettings.ThemeDark
	if app.appearance != nil {
		theme = app.appearance.current.Theme
	}
	if theme == appsettings.ThemeDark {
		app.setTheme(appsettings.ThemeLight)
		return
	}
	app.setTheme(appsettings.ThemeDark)
}

func (app *nativeApp) setScale(scale float64) {
	if app.appearance == nil {
		return
	}
	app.expandImmediately()
	app.appearance.setScale(scale)
	app.reloadSurface()
	app.savePlacement()
}

func (app *nativeApp) handleTimer(timerID uintptr) {
	switch timerID {
	case pollTimerID:
		app.pollAutoCollapse()
	case animationTimerID:
		app.advanceAnimation()
	case selfTestTimerID:
		if app.selfTest != nil {
			app.selfTest.handleWakeTimeout(app)
		}
	case trayRetryTimerID:
		if err := app.ensureTrayIcon(); err != nil {
			log.Printf(
				"retrying tray icon (%d/%d): %v",
				app.trayRetryAttempts,
				trayRetryLimit,
				err,
			)
		}
	case accountExpiryReminderTimerID:
		app.handleAccountExpiryReminderTimer(time.Now())
	}
}

func setNativeTimer(window uintptr, timerID uintptr, interval uint32) bool {
	result, _, lastErr := procSetTimer.Call(
		window,
		timerID,
		uintptr(interval),
		0,
	)
	if result != 0 {
		return true
	}
	log.Printf(
		"setting timer %d for hwnd=0x%x: %v",
		timerID,
		window,
		callError("SetTimer", lastErr),
	)
	return false
}

func (app *nativeApp) pollAutoCollapse() {
	if !app.autoCollapseEnabled {
		return
	}
	if !nativeWindowVisible(app.window) {
		app.awayPolls = 0
		return
	}
	if nativeWindowVisible(app.statisticsWindow.Handle) ||
		nativeWindowVisible(app.usageToastWindow.Handle) {
		app.awayPolls = 0
		return
	}

	geometry, ok := app.currentGeometry()
	if !ok {
		return
	}
	var cursor winPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor)))
	inside := int(cursor.X) >= geometry.X && int(cursor.X) < geometry.X+geometry.Width
	inside = inside && int(cursor.Y) >= geometry.Y && int(cursor.Y) < geometry.Y+geometry.Height
	if app.animation.active {
		if app.animation.endsCollapsed {
			if inside {
				app.startExpandAnimation()
			}
			return
		}
		if inside {
			app.awayPolls = 0
			return
		}
		app.awayPolls++
		if app.awayPolls < 6 {
			return
		}
		area, ok := app.currentWorkArea()
		if ok {
			app.startCollapseAnimation(geometry, area)
		}
		return
	}
	if app.collapsed {
		if inside {
			app.startExpandAnimation()
		}
		return
	}
	if inside {
		app.awayPolls = 0
		return
	}

	area, ok := app.currentWorkArea()
	if !ok || !isDocked(geometry, area) {
		app.awayPolls = 0
		return
	}
	app.awayPolls++
	if app.awayPolls >= 6 {
		app.startCollapseAnimation(geometry, area)
	}
}

func (app *nativeApp) startCollapseAnimation(geometry windowGeometry, area workArea) {
	if (app.collapsed && !app.animation.active) ||
		(app.animation.active && app.animation.endsCollapsed) {
		return
	}
	target, ok := collapsedPositionForWorkAreas(
		geometry,
		area,
		enumerateWorkAreas(),
	)
	if !ok {
		return
	}
	expandedPosition := geometryPoint{X: geometry.X, Y: geometry.Y}
	if app.hasExpandedPosition && app.animation.active {
		expandedPosition = app.expandedPosition
	}
	if !app.animationsEnabled {
		app.expandedPosition = expandedPosition
		app.hasExpandedPosition = true
		app.moveMainWindow(target)
		app.animation = windowAnimation{}
		app.collapsed = true
		app.awayPolls = 0
		return
	}
	animation := windowAnimation{
		active:    true,
		start:     expandedPosition,
		target:    target,
		startedAt: time.Now(),
		duration: scaledWindowAnimationDuration(
			geometryPoint{X: geometry.X, Y: geometry.Y},
			target,
			expandedPosition,
			target,
			mainWindowAnimationDuration,
		),
		endsCollapsed: true,
	}
	animation.start = geometryPoint{X: geometry.X, Y: geometry.Y}
	if !setNativeTimer(app.window, animationTimerID, uint32(mainWindowAnimationTick/time.Millisecond)) {
		return
	}
	app.expandedPosition = expandedPosition
	app.hasExpandedPosition = true
	app.animation = animation
	app.awayPolls = 0
}

func (app *nativeApp) startExpandAnimation() {
	if !app.hasExpandedPosition || (app.animation.active && !app.animation.endsCollapsed) {
		return
	}
	geometry, ok := app.currentGeometry()
	if !ok {
		return
	}
	start := geometryPoint{X: geometry.X, Y: geometry.Y}
	collapsedTarget := start
	if app.animation.active && app.animation.endsCollapsed {
		collapsedTarget = app.animation.target
	}
	if !app.animationsEnabled {
		app.moveMainWindow(app.expandedPosition)
		app.animation = windowAnimation{}
		app.collapsed = false
		app.awayPolls = 0
		app.savePlacement()
		return
	}
	animation := windowAnimation{
		active:    true,
		start:     start,
		target:    app.expandedPosition,
		startedAt: time.Now(),
		duration: scaledWindowAnimationDuration(
			start,
			app.expandedPosition,
			collapsedTarget,
			app.expandedPosition,
			mainWindowAnimationDuration,
		),
		endsCollapsed: false,
	}
	if !setNativeTimer(app.window, animationTimerID, uint32(mainWindowAnimationTick/time.Millisecond)) {
		return
	}
	app.animation = animation
	app.awayPolls = 0
}

func (app *nativeApp) advanceAnimation() {
	app.advanceAnimationAt(time.Now())
}

func (app *nativeApp) advanceAnimationAt(now time.Time) {
	if !app.animation.active {
		procKillTimer.Call(app.window, animationTimerID)
		return
	}

	position, complete := windowAnimationFrame(app.animation, now)
	app.moveMainWindow(position)
	if !complete {
		return
	}

	app.collapsed = app.animation.endsCollapsed
	app.animation = windowAnimation{}
	procKillTimer.Call(app.window, animationTimerID)
	if !app.collapsed {
		app.savePlacement()
	}
}

func windowAnimationFrame(animation windowAnimation, now time.Time) (geometryPoint, bool) {
	progress := timedAnimationProgress(animation.startedAt, animation.duration, now)
	return interpolateAnimationPosition(animation.start, animation.target, progress), progress >= 1
}

func timedAnimationProgress(startedAt time.Time, duration time.Duration, now time.Time) float64 {
	if duration <= 0 || !now.Before(startedAt.Add(duration)) {
		return 1
	}
	if !now.After(startedAt) {
		return 0
	}
	return float64(now.Sub(startedAt)) / float64(duration)
}

func scaledWindowAnimationDuration(
	start geometryPoint,
	target geometryPoint,
	fullStart geometryPoint,
	fullTarget geometryPoint,
	fullDuration time.Duration,
) time.Duration {
	if fullDuration <= 0 {
		return 0
	}
	fullDistance := windowAnimationDistance(fullStart, fullTarget)
	remainingDistance := windowAnimationDistance(start, target)
	if fullDistance <= 0 || remainingDistance <= 0 {
		return 0
	}
	duration := time.Duration(
		int64(fullDuration) * int64(remainingDistance) / int64(fullDistance),
	)
	return max(time.Millisecond, min(fullDuration, duration))
}

func windowAnimationDistance(start geometryPoint, target geometryPoint) int {
	return max(abs(target.X-start.X), abs(target.Y-start.Y))
}

func interpolateAnimationPosition(
	start geometryPoint,
	target geometryPoint,
	progress float64,
) geometryPoint {
	progress = max(0, min(progress, 1))
	eased := 1 - (1-progress)*(1-progress)*(1-progress)
	return geometryPoint{
		X: start.X + int(float64(target.X-start.X)*eased+roundingBias(target.X-start.X)),
		Y: start.Y + int(float64(target.Y-start.Y)*eased+roundingBias(target.Y-start.Y)),
	}
}

func roundingBias(delta int) float64 {
	if delta < 0 {
		return -0.5
	}
	return 0.5
}

func (app *nativeApp) moveMainWindow(position geometryPoint) {
	procSetWindowPos.Call(
		app.window,
		0,
		signed(int32(position.X)),
		signed(int32(position.Y)),
		0,
		0,
		swpNoSize|swpNoZOrder|swpNoActivate,
	)
}

func (app *nativeApp) expandImmediately() {
	if !app.collapsed && !app.animation.active {
		return
	}
	if !app.hasExpandedPosition {
		app.animation = windowAnimation{}
		app.collapsed = false
		return
	}
	procKillTimer.Call(app.window, animationTimerID)
	app.moveMainWindow(app.expandedPosition)
	app.animation = windowAnimation{}
	app.collapsed = false
}

func (app *nativeApp) currentGeometry() (windowGeometry, bool) {
	return app.currentGeometryForWindow(app.window)
}

func (app *nativeApp) currentGeometryForWindow(window uintptr) (windowGeometry, bool) {
	if window == 0 {
		return windowGeometry{}, false
	}
	var rect winRect
	result, _, _ := procGetWindowRect.Call(window, uintptr(unsafe.Pointer(&rect)))
	if result == 0 {
		return windowGeometry{}, false
	}
	return windowGeometry{
		X:      int(rect.Left),
		Y:      int(rect.Top),
		Width:  int(rect.Right - rect.Left),
		Height: int(rect.Bottom - rect.Top),
	}, true
}

func (app *nativeApp) currentWorkArea() (workArea, bool) {
	const monitorDefaultToNearest = 2
	monitor, _, _ := procMonitorFromWindow.Call(app.window, monitorDefaultToNearest)
	if monitor == 0 {
		return workArea{}, false
	}
	info := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	result, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		return workArea{}, false
	}
	return workArea{
		X:      int(info.Work.Left),
		Y:      int(info.Work.Top),
		Width:  int(info.Work.Right - info.Work.Left),
		Height: int(info.Work.Bottom - info.Work.Top),
	}, true
}

func isDocked(geometry windowGeometry, area workArea) bool {
	const tolerance = 16
	left := abs(geometry.X-area.X) <= tolerance
	right := abs((geometry.X+geometry.Width)-(area.X+area.Width)) <= tolerance
	top := abs(geometry.Y-area.Y) <= tolerance
	bottom := abs((geometry.Y+geometry.Height)-(area.Y+area.Height)) <= tolerance
	return left || right || top || bottom
}

func (app *nativeApp) savePlacement() {
	if app.window == 0 || app.placementDisabled {
		return
	}
	var rect winRect
	result, _, _ := procGetWindowRect.Call(app.window, uintptr(unsafe.Pointer(&rect)))
	if result == 0 {
		return
	}
	x := int(rect.Left)
	y := int(rect.Top)
	if (app.collapsed || app.animation.active) && app.hasExpandedPosition {
		x = app.expandedPosition.X
		y = app.expandedPosition.Y
	}
	layout := string(app.currentLayout())
	if app.appearance != nil {
		app.appearance.setLayout(app.currentLayout())
		app.appearance.setMainPositionForLayout(
			app.currentLayout(),
			geometryPoint{X: x, Y: y},
		)
		if app.statisticsDetached {
			if statistics, ok := app.currentGeometryForWindow(app.statisticsWindow.Handle); ok {
				app.appearance.setStatisticsPosition(
					geometryPoint{X: statistics.X, Y: statistics.Y},
				)
			}
		}
		if err := app.appearance.save(); err != nil {
			log.Printf("saving appearance: %v", err)
		}
	}
	if err := app.placement.save(windowPlacement{
		X:      x,
		Y:      y,
		Layout: layout,
	}); err != nil {
		log.Printf("saving window placement: %v", err)
	}
}

func appendMenu(menu uintptr, flags uintptr, command uintptr, label string) bool {
	var labelPointer uintptr
	if label != "" {
		labelPointer = uintptr(unsafe.Pointer(utf16Pointer(label)))
	}
	result, _, _ := procAppendMenuW.Call(menu, flags, command, labelPointer)
	return result != 0
}

func nativeWindowProc(window uintptr, message uint32, wParam uintptr, lParam uintptr) uintptr {
	app := activeNativeApp
	if app == nil {
		result, _, _ := procDefWindowProcW.Call(window, uintptr(message), wParam, lParam)
		return result
	}
	role := app.roleForWindow(window)
	isMain := role == windowRoleMain

	if isMain && app.taskbarCreatedMessage != 0 && message == app.taskbarCreatedMessage {
		app.trayRetryAttempts = 0
		if err := app.ensureTrayIcon(); err != nil {
			log.Printf("restoring tray icon: %v", err)
		}
		return 0
	}

	switch message {
	case wmNativeInitialize:
		if !isMain {
			break
		}
		trayErr := app.ensureTrayIcon()
		if trayErr != nil {
			log.Printf("adding tray icon: %v", trayErr)
		}
		if app.selfTest != nil {
			app.selfTest.start(app, trayErr)
		}
		return 0
	case wmNativeReload:
		if !isMain {
			break
		}
		app.reloadSurface()
		return 0
	case wmNativeStatusChanged:
		if !isMain {
			break
		}
		if app.status != nil && app.status.acceptPending() {
			showUsageToast := app.status.shouldShowUsageToast()
			app.reloadStatusSurfaces()
			if showUsageToast {
				app.showUsageToast()
			}
		}
		return 0
	case wmNativeCodexChanged:
		if !isMain {
			break
		}
		if app.process != nil {
			if status, ok := app.process.acceptPending(); ok {
				app.applyCodexProcessStatus(status.Running, status.Visible)
			}
		}
		return 0
	case wmNativeOcclusionChanged:
		if !isMain {
			break
		}
		app.refreshCodexOcclusion()
		return 0
	case wmNativeShow:
		if !isMain {
			break
		}
		if app.selfTest != nil {
			app.selfTest.observeWakeMessage()
		}
		app.showMainWindow()
		return 0
	case wmNativeSelfTestComplete:
		if !isMain {
			break
		}
		if app.selfTest != nil {
			app.selfTest.completeWakeProbe(app)
		}
		return 0
	case wmNativeTray:
		if !isMain {
			break
		}
		event := uint32(lParam & 0xffff)
		app.handleTrayEvent(event)
		return 0
	case wmNCHitTest:
		if role == windowRoleUnknown {
			break
		}
		if role == windowRoleUsageToast &&
			toastAnimationBlocksInput(app.usageToastWindow.Visibility) {
			return htTransparent
		}
		var windowRect winRect
		result, _, _ := procGetWindowRect.Call(window, uintptr(unsafe.Pointer(&windowRect)))
		if result == 0 {
			return htClient
		}
		screenX := int32(int16(lParam & 0xffff))
		screenY := int32(int16((lParam >> 16) & 0xffff))
		if app.actionAtWindow(
			window,
			screenX-windowRect.Left,
			screenY-windowRect.Top,
		) != "" {
			return htClient
		}
		if isMain {
			return htCaption
		}
		if role == windowRoleStatistics {
			return htCaption
		}
		return htClient
	case wmLButtonUp:
		clientX := int32(int16(lParam & 0xffff))
		clientY := int32(int16((lParam >> 16) & 0xffff))
		app.executeWindowAction(
			window,
			app.actionAtWindow(window, clientX, clientY),
		)
		return 0
	case wmRButtonUp, wmNCRButtonUp:
		if role != windowRoleUnknown {
			app.showTrayMenu()
			return 0
		}
	case wmKeyDown:
		if wParam == vkEscape {
			if isMain {
				app.hideAllWindows()
			} else if auxiliary := app.auxiliaryForWindow(window); auxiliary != nil {
				app.hideAuxiliaryWindow(auxiliary)
			}
			return 0
		}
	case wmTimer:
		if isMain {
			app.handleTimer(wParam)
			return 0
		}
		if role == windowRoleUsageToast {
			switch wParam {
			case usageToastAnimationTimerID:
				app.advanceUsageToastAnimation()
				return 0
			case usageToastTimerID:
				app.hideAuxiliaryWindow(&app.usageToastWindow)
				return 0
			}
		}
	case wmMouseMove, wmNCMouseMove:
		if isMain && (app.collapsed ||
			(app.animation.active && app.animation.endsCollapsed)) {
			app.startExpandAnimation()
		}
	case wmDisplayChange:
		if isMain {
			app.queueOcclusionRefresh()
		}
	case wmDPIChanged:
		if isMain {
			if lParam != 0 {
				var suggested winRect
				readErr := windows.ReadProcessMemory(
					windows.CurrentProcess(),
					lParam,
					(*byte)(unsafe.Pointer(&suggested)),
					unsafe.Sizeof(suggested),
					nil,
				)
				if readErr == nil {
					procSetWindowPos.Call(
						window,
						0,
						signed(suggested.Left),
						signed(suggested.Top),
						0,
						0,
						swpNoSize|swpNoZOrder|swpNoActivate,
					)
				}
			}
			app.reloadSurfaceAtDPI(uint32(wParam & 0xffff))
			return 0
		}
		if auxiliary := app.auxiliaryForWindow(window); auxiliary != nil {
			if auxiliary.Role == windowRoleUsageToast {
				app.settleUsageToastAnimation()
			}
			if lParam != 0 {
				var suggested winRect
				readErr := windows.ReadProcessMemory(
					windows.CurrentProcess(),
					lParam,
					(*byte)(unsafe.Pointer(&suggested)),
					unsafe.Sizeof(suggested),
					nil,
				)
				if readErr == nil {
					procSetWindowPos.Call(
						window,
						0,
						signed(suggested.Left),
						signed(suggested.Top),
						0,
						0,
						swpNoSize|swpNoZOrder|swpNoActivate,
					)
				}
			}
			procPostMessageW.Call(app.window, wmNativeReload, 0, 0)
		}
		return 0
	case wmWindowPosChanged:
		if isMain && !app.updatingWindows {
			app.repositionAuxiliaryWindows(windowRoleUnknown)
		}
	case wmEnterSizeMove:
		if role == windowRoleStatistics {
			app.statisticsDetached = true
			return 0
		}
	case wmExitSizeMove:
		if isMain {
			app.savePlacement()
			app.repositionAuxiliaryWindows(windowRoleUnknown)
			return 0
		}
		if role == windowRoleStatistics {
			app.statisticsDetached = true
			app.savePlacement()
			return 0
		}
	case wmClose:
		if isMain {
			app.hideAllWindows()
			return 0
		}
		if auxiliary := app.auxiliaryForWindow(window); auxiliary != nil {
			app.hideAuxiliaryWindow(auxiliary)
			return 0
		}
	case wmDestroy:
		log.Printf("WM_DESTROY hwnd=0x%x role=%d", window, role)
		if !isMain {
			if role == windowRoleUsageToast {
				procKillTimer.Call(window, usageToastAnimationTimerID)
				procKillTimer.Call(window, usageToastTimerID)
				app.usageToastWindow.Visibility = auxiliaryHidden
				app.usageToastAnimation = toastWindowAnimation{}
			}
			return 0
		}
		procKillTimer.Call(window, pollTimerID)
		app.autoCollapseTimerRunning = false
		procKillTimer.Call(window, animationTimerID)
		procKillTimer.Call(window, selfTestTimerID)
		procKillTimer.Call(window, trayRetryTimerID)
		procKillTimer.Call(window, accountExpiryReminderTimerID)
		app.stopOcclusionHooks()
		app.savePlacement()
		app.destroyAuxiliaryWindows()
		app.stopWatcher()
		app.stopStatusMonitor()
		app.stopProcessMonitor()
		procPostQuitMessage.Call(0)
		return 0
	case wmNCDestroy:
		log.Printf("WM_NCDESTROY hwnd=0x%x role=%d", window, role)
		if auxiliary := app.auxiliaryForWindow(window); auxiliary != nil {
			auxiliary.Handle = 0
			auxiliary.CurrentSurface = nil
			auxiliary.Visibility = auxiliaryHidden
			auxiliary.Dirty = false
		}
	}

	result, _, _ := procDefWindowProcW.Call(window, uintptr(message), wParam, lParam)
	return result
}

func runMessageLoop() error {
	var message winMessage
	for {
		result, _, lastErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) == -1 {
			return callError("GetMessageW", lastErr)
		}
		if result == 0 {
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func setProcessDPIAware() {
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is the signed pointer value -4.
	procSetProcessDpiAwarenessContext.Call(^uintptr(3))
}

func systemDPI() uint32 {
	dpi, _, _ := procGetDpiForSystem.Call()
	if dpi == 0 {
		return 96
	}
	return uint32(dpi)
}

func wakeExistingWindow(windowClass string, windowTitle string, timeout time.Duration) bool {
	className := utf16Pointer(windowClass)
	title := utf16Pointer(windowTitle)
	deadline := time.Now().Add(timeout)
	for {
		window, _, _ := procFindWindowW.Call(
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(title)),
		)
		if window != 0 {
			posted, _, _ := procPostMessageW.Call(window, wmNativeShow, 0, 0)
			if posted != 0 {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}
