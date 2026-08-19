//go:build windows && workbench

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkbenchIndexUsesOneStartupVersion(t *testing.T) {
	startedAt := time.Date(2026, 8, 9, 12, 34, 56, 0, time.Local)
	server := &workbenchServer{
		startedAt:     startedAt,
		workbenchRoot: "ui/workbench",
		bundleRoot:    t.TempDir(),
		exportToken:   "test-token",
	}
	request := httptest.NewRequest("GET", "/", nil)
	response := httptest.NewRecorder()

	server.serveIndex(response, request)

	if response.Code != 200 {
		t.Fatalf("got status %d, want 200", response.Code)
	}
	if policy := response.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "img-src 'self' data: blob:") {
		t.Fatalf("content security policy does not allow native preview blobs: %q", policy)
	}
	body := response.Body.String()
	for _, expected := range []string{
		"更新 2026-08-09",
		"BUILD 2026-08-09 12:34:56",
		"b0908 12:56",
		"styles.css?v=codexfloatingbar-",
		"app.js?v=codexfloatingbar-",
		"window.__WORKBENCH_TOKEN__ = \"test-token\"",
		"window.__AUTO_EXPORT__ = false",
		"id=\"statisticsSurface\"",
		"id=\"usageToastSurface\"",
		"data-action=\"toggle-toast\"",
		"data-action=\"toggle-theme\"",
		"data-action=\"statistics-view-week\"",
		"data-action=\"statistics-previous-month\"",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("workbench index does not contain %q", expected)
		}
	}
	stageStart := strings.Index(body, `<main class="stage">`)
	stageEnd := strings.Index(body, `<aside class="editing-note">`)
	if stageStart < 0 || stageEnd <= stageStart {
		t.Fatal("workbench surface stage was not found")
	}
	stage := body[stageStart:stageEnd]
	for _, forbidden := range []string{
		"更新 2026-08-09",
		"BUILD 2026-08-09",
		">C<",
		"data-action=\"toggle-collapse\"",
	} {
		if strings.Contains(stage, forbidden) {
			t.Fatalf("exportable surface stage contains %q", forbidden)
		}
	}
	autoServer := &workbenchServer{
		startedAt:     startedAt,
		workbenchRoot: "ui/workbench",
		bundleRoot:    t.TempDir(),
		exportToken:   "auto-token",
		autoExport:    true,
		autoReports:   make(chan autoExportReport, 1),
	}
	autoResponse := httptest.NewRecorder()
	autoServer.serveIndex(autoResponse, httptest.NewRequest(http.MethodGet, "/", nil))
	if autoResponse.Code != http.StatusOK ||
		!strings.Contains(autoResponse.Body.String(), "window.__AUTO_EXPORT__ = true") {
		t.Fatalf("automatic workbench index = %d %q", autoResponse.Code, autoResponse.Body.String())
	}
	select {
	case report := <-autoServer.autoReports:
		t.Fatalf("GET / produced an automatic export report: %+v", report)
	default:
	}
	if autoServer.autoReported || autoServer.autoSuccess != nil {
		t.Fatal("GET / changed automatic export state")
	}
}

func TestRenderNativePreviewUsesCommittedDynamicSlots(t *testing.T) {
	bundleRoot := t.TempDir()
	manifest := bundleManifest{
		Schema:         bundleSchema,
		Project:        projectID,
		DefaultSurface: "main-horizontal",
		Version: pageVersion{
			Update:        "2026-08-10",
			Build:         "2026-08-10 12:00:00",
			StaticVersion: "codexfloatingbar-testfixture",
		},
		Surfaces: []bundleSurface{{
			ID:            "main-horizontal",
			LogicalWidth:  200,
			LogicalHeight: 48,
			Dynamic: dynamicSlots{Text: []textSlot{{
				Bind:       "runtime.model",
				Rect:       slotRect{X: 4, Y: 4, Width: 192, Height: 40},
				FontFamily: "Microsoft YaHei UI",
				FontSize:   18,
				FontWeight: 600,
				Color:      "#ffffff",
				Align:      "center",
			}}},
			Variants: []bundleVariant{{
				Scale:  1,
				File:   "main-horizontal.png",
				Width:  200,
				Height: 48,
			}},
		}},
	}
	manifestContents, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(bundleRoot, "manifest.json"),
		manifestContents,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(bundleRoot, "main-horizontal.png"),
		testPNG(t, 200, 48, false),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	server := &workbenchServer{bundleRoot: bundleRoot}
	contents, err := server.renderNativePreview(nativePreviewRequest{
		SurfaceID:      "main-horizontal",
		Text:           map[string]string{"runtime.model": "GPT-5.6 模型"},
		Progress:       map[string]int{},
		Cells:          map[string][]int{},
		Tone:           quotaToneGood,
		StatisticsView: statisticsViewMonth,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	var visible int
	for y := range decoded.Bounds().Dy() {
		for x := range decoded.Bounds().Dx() {
			_, _, _, alpha := decoded.At(x, y).RGBA()
			if alpha != 0 {
				visible++
			}
		}
	}
	if visible == 0 {
		t.Fatal("native preview contains no composed text pixels")
	}
}

func TestValidateNativePreviewPresentationRejectsUnsafeBounds(t *testing.T) {
	err := validateNativePreviewPresentation(nativePreviewRequest{
		SurfaceID:      "main-horizontal",
		Text:           map[string]string{"runtime.model": strings.Repeat("x", 513)},
		Tone:           quotaToneGood,
		StatisticsView: statisticsViewMonth,
	})
	if err == nil {
		t.Fatal("oversized native preview text was accepted")
	}
}

func TestWorkbenchExporterScopesLiveStylesToEachSurface(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("ui", "workbench", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	app := string(contents)
	for _, expected := range []string{
		"collectSurfaceStylesheet(stylesheets, clone)",
		"selectorAffectsExportTree(rule.selectorText, element)",
		"element.matches(selector) || element.querySelector(selector) !== null",
		"fontFamilies = parsedFontFamilies(style.fontFamily)",
		"fontFamilies,",
		"requestExportPreflight(defaultSurface)",
		"version: pageVersion",
		"requireEdgeWorkbenchExport()",
		"renderer,",
		"if (autoExport)",
		"runAutomaticExport()",
		`fetch("/api/export/result"`,
		"setExportFreeze(true)",
		`class=\"export-freeze\"`,
		"prefers-reduced-motion: reduce",
	} {
		if !strings.Contains(app, expected) {
			t.Fatalf("workbench exporter does not contain %q", expected)
		}
	}
	preflight := strings.Index(app, "await requestExportPreflight(defaultSurface)")
	stylesheetCollection := strings.Index(app, "await collectStylesheets()")
	if preflight < 0 || stylesheetCollection < 0 || preflight >= stylesheetCollection {
		t.Fatal("export preflight does not run before HTML/style snapshot construction")
	}
	if strings.Contains(app, "auto-export") || strings.Contains(app, "URLSearchParams") {
		t.Fatal("workbench still enables automatic export through a GET query parameter")
	}
}

func TestWorkbenchExportAuthorization(t *testing.T) {
	server := &workbenchServer{
		origin:      "http://127.0.0.1:9315",
		exportToken: "secret",
	}
	request := httptest.NewRequest(http.MethodPost, server.origin+"/api/export", bytes.NewReader(nil))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Origin", server.origin)
	request.Header.Set("X-Codex-Workbench-Token", "secret")

	if err := server.authorizeExport(request); err != nil {
		t.Fatalf("authorized local request was rejected: %v", err)
	}
	request.Header.Del("X-Codex-Workbench-Token")
	if err := server.authorizeExport(request); err == nil {
		t.Fatal("request without the workbench token was accepted")
	}

	makeReportRequest := func(
		server *workbenchServer,
		token string,
		reported autoExportReport,
	) (*httptest.ResponseRecorder, *http.Request) {
		t.Helper()
		contents, err := json.Marshal(reported)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(
			http.MethodPost,
			server.origin+"/api/export/result",
			bytes.NewReader(contents),
		)
		request.RemoteAddr = "127.0.0.1:54321"
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", server.origin)
		request.Header.Set("X-Codex-Workbench-Token", token)
		return httptest.NewRecorder(), request
	}
	defaultResponse, defaultRequest := makeReportRequest(
		server,
		"secret",
		autoExportReport{OK: false, Error: "must remain disabled"},
	)
	server.receiveAutoExportReport(defaultResponse, defaultRequest)
	if defaultResponse.Code != http.StatusNotFound {
		t.Fatalf("default automatic report status = %d, want 404", defaultResponse.Code)
	}

	newAutoServer := func() *workbenchServer {
		return &workbenchServer{
			origin:      "http://127.0.0.1:9315",
			exportToken: "secret",
			autoExport:  true,
			autoReports: make(chan autoExportReport, 1),
		}
	}
	verified := exportResult{OK: true, UpToDate: true, Files: 56, Reused: 56}
	verifiedReport := autoExportReport{
		OK: true, UpToDate: true, Files: 56, Reused: 56,
	}
	autoServer := newAutoServer()
	autoServer.recordAutoExportSuccess(verified)
	deniedResponse, deniedRequest := makeReportRequest(autoServer, "wrong", verifiedReport)
	autoServer.receiveAutoExportReport(deniedResponse, deniedRequest)
	if deniedResponse.Code != http.StatusForbidden {
		t.Fatalf("unauthorized automatic report status = %d", deniedResponse.Code)
	}
	select {
	case reported := <-autoServer.autoReports:
		t.Fatalf("unauthorized automatic report became terminal: %+v", reported)
	default:
	}

	successResponse, successRequest := makeReportRequest(autoServer, "secret", verifiedReport)
	autoServer.receiveAutoExportReport(successResponse, successRequest)
	if successResponse.Code != http.StatusOK {
		t.Fatalf("verified automatic success status = %d: %s", successResponse.Code, successResponse.Body)
	}
	if reported := <-autoServer.autoReports; reported != verifiedReport {
		t.Fatalf("automatic success report = %+v, want %+v", reported, verifiedReport)
	}
	duplicateResponse, duplicateRequest := makeReportRequest(autoServer, "secret", verifiedReport)
	autoServer.receiveAutoExportReport(duplicateResponse, duplicateRequest)
	if duplicateResponse.Code != http.StatusConflict {
		t.Fatalf("duplicate automatic report status = %d, want 409", duplicateResponse.Code)
	}
	committedServer := newAutoServer()
	committed := exportResult{OK: true, Files: 56, Rendered: 56, Atlases: 4}
	committedReport := autoExportReport{OK: true, Files: 56, Rendered: 56, Atlases: 4}
	committedServer.recordAutoExportSuccess(committed)
	committedResponse, committedRequest := makeReportRequest(
		committedServer,
		"secret",
		committedReport,
	)
	committedServer.receiveAutoExportReport(committedResponse, committedRequest)
	if committedResponse.Code != http.StatusOK {
		t.Fatalf(
			"committed automatic success status = %d: %s",
			committedResponse.Code,
			committedResponse.Body,
		)
	}
	if reported := <-committedServer.autoReports; reported != committedReport {
		t.Fatalf("committed automatic report = %+v, want %+v", reported, committedReport)
	}

	forgedServer := newAutoServer()
	forgedResponse, forgedRequest := makeReportRequest(forgedServer, "secret", verifiedReport)
	forgedServer.receiveAutoExportReport(forgedResponse, forgedRequest)
	if forgedResponse.Code != http.StatusBadRequest {
		t.Fatalf("unverified automatic success status = %d, want 400", forgedResponse.Code)
	}
	failureServer := newAutoServer()
	failureReport := autoExportReport{OK: false, Error: "client DOM measurement failed"}
	failureResponse, failureRequest := makeReportRequest(failureServer, "secret", failureReport)
	failureServer.receiveAutoExportReport(failureResponse, failureRequest)
	if failureResponse.Code != http.StatusOK {
		t.Fatalf("automatic failure status = %d: %s", failureResponse.Code, failureResponse.Body)
	}
	if reported := <-failureServer.autoReports; reported != failureReport {
		t.Fatalf("automatic failure report = %+v, want %+v", reported, failureReport)
	}
}

func TestWorkbenchAddressMustBeLoopback(t *testing.T) {
	if err := requireLoopbackAddress("127.0.0.1:9315"); err != nil {
		t.Fatal(err)
	}
	if err := requireLoopbackAddress("0.0.0.0:9315"); err == nil {
		t.Fatal("non-loopback workbench address was accepted")
	}
}

func TestWorkbenchExportOnceMode(t *testing.T) {
	defaults, err := parseWorkbenchOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.exportOnce || !defaults.openBrowser ||
		defaults.listenAddress != "127.0.0.1:9315" {
		t.Fatalf("default workbench options = %+v", defaults)
	}
	configured, err := parseWorkbenchOptions([]string{
		"--export-once",
		"--listen-address=127.0.0.1:0",
		"--ui-root=D:\\fixture-ui",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !configured.exportOnce || configured.openBrowser ||
		configured.listenAddress != "127.0.0.1:0" ||
		configured.uiRoot != `D:\fixture-ui` {
		t.Fatalf("automatic workbench options = %+v", configured)
	}
	if _, err := parseWorkbenchOptions([]string{"unexpected"}); err == nil {
		t.Fatal("unexpected positional workbench argument was accepted")
	}

	arguments := exportOnceEdgeArguments(
		"http://127.0.0.1:12345/",
		`D:\profile`,
	)
	if !slices.Contains(arguments, "--headless=new") ||
		!slices.Contains(arguments, `--user-data-dir=D:\profile`) ||
		arguments[len(arguments)-1] != "http://127.0.0.1:12345/" ||
		slices.Contains(arguments, "--disable-javascript") {
		t.Fatalf("automatic Edge arguments = %q", arguments)
	}

	successReports := make(chan autoExportReport, 1)
	successReports <- autoExportReport{OK: true, Files: 56, Rendered: 56, Atlases: 4}
	success := waitForExportOnceTerminal(
		successReports,
		make(chan error),
		time.Second,
	)
	if success.kind != exportOnceReported || !success.report.OK {
		t.Fatalf("automatic success terminal = %+v", success)
	}
	failureReports := make(chan autoExportReport, 1)
	failureReports <- autoExportReport{OK: false, Error: "client failure"}
	failure := waitForExportOnceTerminal(
		failureReports,
		make(chan error),
		time.Second,
	)
	if failure.kind != exportOnceReported || failure.report.OK ||
		failure.report.Error != "client failure" {
		t.Fatalf("automatic failure terminal = %+v", failure)
	}
	processDone := make(chan error, 1)
	processFailure := errors.New("injected early Edge exit")
	processDone <- processFailure
	exited := waitForExportOnceTerminal(
		make(chan autoExportReport),
		processDone,
		time.Second,
	)
	if exited.kind != exportOnceEdgeExited || !errors.Is(exited.processErr, processFailure) {
		t.Fatalf("early Edge terminal = %+v", exited)
	}
	expired := waitForExportOnceTerminal(
		make(chan autoExportReport),
		make(chan error),
		5*time.Millisecond,
	)
	if expired.kind != exportOnceExpired {
		t.Fatalf("automatic timeout terminal = %+v", expired)
	}

	running, err := startWorkbenchServerWithOptions(
		"127.0.0.1:0",
		time.Date(2026, 8, 19, 12, 0, 0, 0, time.Local),
		t.TempDir(),
		t.TempDir(),
		workbenchServerOptions{AutoExport: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if running.AutoReports == nil {
		t.Fatal("automatic workbench server has no result channel")
	}
	listenAddress := strings.TrimSuffix(
		strings.TrimPrefix(running.URL, "http://"),
		"/",
	)
	if err := running.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		t.Fatalf("workbench server did not release %q: %v", listenAddress, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRejectsExternalResources(t *testing.T) {
	safe := `<!doctype html><html><head><style></style></head><body><div></div></body></html>`
	if err := validateSnapshotHTML(safe); err != nil {
		t.Fatal(err)
	}
	unsafe := `<!doctype html><html><head><style>` +
		`.x{background:url(https://example.test/a)}` +
		`</style></head><body></body></html>`
	if err := validateSnapshotHTML(unsafe); err == nil {
		t.Fatal("external HTML resource was accepted")
	}
}

func TestEdgeRasterizerIntegration(t *testing.T) {
	if os.Getenv("CODEXFLOATINGBAR_TEST_EDGE") != "1" {
		t.Skip("set CODEXFLOATINGBAR_TEST_EDGE=1 to run the system Edge exporter")
	}
	identity, err := installedEdgeRendererIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if identity.Version == "" || !isLowerHex(identity.Fingerprint, exportRendererStampLength) {
		t.Fatalf("invalid installed Edge identity: %+v", identity)
	}
	contents, err := rasterizeHTML(exportFile{
		Name:   "test.png",
		Width:  64,
		Height: 32,
		HTML: `<!doctype html><html><head><style>` +
			`html,body{width:64px;height:32px;margin:0;background:transparent}` +
			`.surface{width:64px;height:32px;background:#1177cc}` +
			`</style></head><body><div class="surface"></div></body></html>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 64 || config.Height != 32 {
		t.Fatalf("unexpected rasterized dimensions: %dx%d", config.Width, config.Height)
	}
	rendered, _, err := image.Decode(bytes.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	red, green, blue, alpha := rendered.At(32, 16).RGBA()
	if blue <= red || blue <= green || alpha < 0xff00 {
		opaquePixels := 0
		minX, minY := rendered.Bounds().Max.X, rendered.Bounds().Max.Y
		maxX, maxY := rendered.Bounds().Min.X, rendered.Bounds().Min.Y
		for y := rendered.Bounds().Min.Y; y < rendered.Bounds().Max.Y; y++ {
			for x := rendered.Bounds().Min.X; x < rendered.Bounds().Max.X; x++ {
				_, _, _, pixelAlpha := rendered.At(x, y).RGBA()
				if pixelAlpha != 0 {
					opaquePixels++
					minX = min(minX, x)
					minY = min(minY, y)
					maxX = max(maxX, x)
					maxY = max(maxY, y)
				}
			}
		}
		t.Fatalf(
			"HTML content was not painted: rgba=%d,%d,%d,%d opaque=%d bounds=%d,%d-%d,%d",
			red,
			green,
			blue,
			alpha,
			opaquePixels,
			minX,
			minY,
			maxX,
			maxY,
		)
	}

	exported := validWorkbenchExport()
	jobs := make([]exportRenderJob, 0, len(exported.Files))
	expectedColors := make(map[string]color.NRGBA, len(exported.Files))
	for index, file := range exported.Files {
		expected := color.NRGBA{
			R: uint8(32 + index*37%192),
			G: uint8(32 + index*53%192),
			B: uint8(32 + index*71%192),
			A: 255,
		}
		file.HTML = solidColorSnapshot(
			file.Width,
			file.Height,
			fmt.Sprintf("#%02x%02x%02x", expected.R, expected.G, expected.B),
		)
		expectedColors[file.Name] = expected
		jobs = append(jobs, exportRenderJob{
			order: index, fileIndex: index, file: file,
		})
	}
	var atlasCalls atomic.Int32
	cropped, atlases, fallback, err := rasterizeExportAtlases(
		jobs,
		exported.Manifest,
		func(file exportFile) ([]byte, error) {
			atlasCalls.Add(1)
			return rasterizeHTMLAttempt(file)
		},
		exportRasterizerWorkers,
	)
	if err != nil {
		t.Fatal(err)
	}
	if atlasCalls.Load() != int32(len(workbenchVariantScales)) ||
		atlases != len(workbenchVariantScales) || fallback != 0 ||
		len(cropped) != len(exported.Files) {
		t.Fatalf(
			"real Edge atlas calls/atlases/fallback/crops = %d/%d/%d/%d",
			atlasCalls.Load(),
			atlases,
			fallback,
			len(cropped),
		)
	}
	for _, outcome := range cropped {
		rendered, err := png.Decode(bytes.NewReader(outcome.contents))
		if err != nil {
			t.Fatal(err)
		}
		if rendered.Bounds().Dx() != outcome.job.file.Width ||
			rendered.Bounds().Dy() != outcome.job.file.Height {
			t.Fatalf(
				"atlas crop %q is %v, want %dx%d",
				outcome.job.file.Name,
				rendered.Bounds(),
				outcome.job.file.Width,
				outcome.job.file.Height,
			)
		}
		r, g, b, a := rendered.At(rendered.Bounds().Dx()/2, rendered.Bounds().Dy()/2).RGBA()
		expected := expectedColors[outcome.job.file.Name]
		if uint8(r>>8) != expected.R || uint8(g>>8) != expected.G ||
			uint8(b>>8) != expected.B || a < 0xff00 {
			t.Fatalf(
				"atlas crop %q center = %d,%d,%d,%d, want %+v",
				outcome.job.file.Name,
				r>>8,
				g>>8,
				b>>8,
				a>>8,
				expected,
			)
		}
	}
}

func solidColorSnapshot(width int, height int, color string) string {
	return fmt.Sprintf(
		`<!doctype html><html><head><style>`+
			`html,body{width:%dpx;height:%dpx;margin:0;background:transparent}`+
			`.surface{width:%dpx;height:%dpx;background:%s}`+
			`</style></head><body><div class="surface"></div></body></html>`,
		width,
		height,
		width,
		height,
		color,
	)
}

func TestEdgeRasterizerArgumentsPreserveBrowserRendering(t *testing.T) {
	file := exportFile{Width: 320, Height: 180}
	arguments := edgeRasterizerArguments(
		file,
		"file:///surface.html",
		`C:\export\surface.png`,
		`C:\export\profile`,
	)

	required := []string{
		"--headless=new",
		"--force-device-scale-factor=1",
		"--window-size=320,600",
		`--screenshot=C:\export\surface.png`,
		`--user-data-dir=C:\export\profile`,
	}
	for _, argument := range required {
		if !slices.Contains(arguments, argument) {
			t.Errorf("Edge arguments do not contain %q", argument)
		}
	}
	forbidden := []string{
		"--disable-gpu",
		"--disable-gpu-compositing",
		"--disable-gpu-rasterization",
		"--disable-gpu-sandbox",
		"--no-sandbox",
	}
	for _, argument := range forbidden {
		if slices.Contains(arguments, argument) {
			t.Errorf("Edge arguments unexpectedly contain %q", argument)
		}
	}
	if arguments[len(arguments)-1] != "file:///surface.html" {
		t.Fatalf("last Edge argument = %q, want the snapshot URL", arguments[len(arguments)-1])
	}
	largeArguments := edgeRasterizerArguments(
		exportFile{Width: 320, Height: 720},
		"file:///large.html",
		`C:\export\large.png`,
		`C:\export\large-profile`,
	)
	if !slices.Contains(largeArguments, "--window-size=320,720") {
		t.Fatal("Edge arguments did not preserve a target height above the minimum")
	}
}

func TestWriteAtomicReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeAtomic(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "second" {
		t.Fatalf("got %q want second", contents)
	}
}

func TestExportFailureKeepsPreviousBundle(t *testing.T) {
	workbenchRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workbenchRoot, "index.html"), []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundleRoot := t.TempDir()
	manifestPath := filepath.Join(bundleRoot, "manifest.json")
	previousManifest := []byte(`{"previous":true}`)
	if err := os.WriteFile(manifestPath, previousManifest, 0o600); err != nil {
		t.Fatal(err)
	}

	server := &workbenchServer{
		startedAt:     time.Date(2026, 8, 9, 12, 0, 0, 0, time.Local),
		workbenchRoot: workbenchRoot,
		bundleRoot:    bundleRoot,
	}
	exported := validWorkbenchExportForServer(t, server)
	failedFile := exported.Files[1].Name
	err := server.writeExportWithRasterizer(exported, func(file exportFile) ([]byte, error) {
		if file.Name == failedFile {
			return nil, errors.New("injected rasterizer failure")
		}
		return testPNG(t, file.Width, file.Height, true), nil
	})
	if err == nil {
		t.Fatal("export unexpectedly succeeded")
	}
	contents, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(contents, previousManifest) {
		t.Fatalf("previous manifest changed after failed export: %q", contents)
	}
	assets, readErr := os.ReadDir(filepath.Join(bundleRoot, "assets"))
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
	if len(assets) != 0 {
		t.Fatalf("failed export committed %d asset generations", len(assets))
	}
}

func TestManifestSwitchFailuresKeepPreviousManifest(t *testing.T) {
	workbenchRoot := t.TempDir()
	writeTestFile(t, filepath.Join(workbenchRoot, "index.html"))
	bundleRoot := t.TempDir()
	server := &workbenchServer{
		startedAt:     time.Date(2026, 8, 9, 12, 0, 0, 0, time.Local),
		workbenchRoot: workbenchRoot,
		bundleRoot:    bundleRoot,
	}
	if err := server.writeExportWithRasterizer(
		validWorkbenchExportForServer(t, server),
		func(file exportFile) ([]byte, error) {
			return testPNG(t, file.Width, file.Height, true), nil
		},
	); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(bundleRoot, "manifest.json")
	previous, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := readManifest(bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	manifest.DefaultSurface = "main-vertical-light"

	writeFailure := errors.New("injected manifest write failure")
	err = commitWorkbenchManifestWithOperations(
		bundleRoot,
		manifest,
		previous,
		true,
		func(string, []byte) error { return writeFailure },
		func() error {
			t.Fatal("post-write validation ran after a failed manifest write")
			return nil
		},
	)
	if !errors.Is(err, writeFailure) {
		t.Fatalf("manifest write failure = %v, want %v", err, writeFailure)
	}
	assertFileContents(t, manifestPath, previous)

	readFailure := errors.New("injected manifest read failure")
	err = commitWorkbenchManifestWithOperations(
		bundleRoot,
		manifest,
		previous,
		true,
		writeAtomic,
		func() error { return readFailure },
	)
	if !errors.Is(err, readFailure) {
		t.Fatalf("manifest read failure = %v, want %v", err, readFailure)
	}
	assertFileContents(t, manifestPath, previous)

	mismatchedRenderer := validWorkbenchExportForServer(t, server)
	mismatchedRenderer.Renderer = "150.0.0.0"
	rasterCalls := 0
	err = server.writeExportWithRasterizer(
		mismatchedRenderer,
		func(file exportFile) ([]byte, error) {
			rasterCalls++
			return testPNG(t, file.Width, file.Height, true), nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "does not match export renderer") {
		t.Fatalf("mismatched Edge request error = %v", err)
	}
	if rasterCalls != 0 {
		t.Fatalf("mismatched Edge request started %d raster jobs", rasterCalls)
	}
	assertFileContents(t, manifestPath, previous)

	stale := validWorkbenchExportForServer(t, server)
	if err := os.WriteFile(
		filepath.Join(workbenchRoot, "index.html"),
		[]byte("changed before export"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	rasterCalls = 0
	err = server.writeExportWithRasterizer(
		stale,
		func(file exportFile) ([]byte, error) {
			rasterCalls++
			return testPNG(t, file.Width, file.Height, true), nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("stale export request error = %v", err)
	}
	if rasterCalls != 0 {
		t.Fatalf("stale export request started %d raster jobs", rasterCalls)
	}
	assertFileContents(t, manifestPath, previous)

	beforeGenerations, err := os.ReadDir(filepath.Join(bundleRoot, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	racing := validWorkbenchExportForServer(t, server)
	for index := range racing.Files {
		racing.Files[index].HTML += "<!-- static source race -->"
	}
	staticChanged := false
	err = server.writeExportWithRasterizer(
		racing,
		func(file exportFile) ([]byte, error) {
			if !staticChanged {
				staticChanged = true
				if err := os.WriteFile(
					filepath.Join(workbenchRoot, "index.html"),
					[]byte("changed during export"),
					0o600,
				); err != nil {
					return nil, err
				}
			}
			return testPNGWithCoverage(t, file.Width, file.Height, 1, 254), nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "source changed during export") {
		t.Fatalf("static source race error = %v", err)
	}
	assertFileContents(t, manifestPath, previous)
	afterGenerations, err := os.ReadDir(filepath.Join(bundleRoot, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	if len(afterGenerations) != len(beforeGenerations) {
		t.Fatalf(
			"static source race left %d generations, want %d",
			len(afterGenerations),
			len(beforeGenerations),
		)
	}
}

func TestValidateExportRequiresFixedWorkbenchSurfaces(t *testing.T) {
	exported := validWorkbenchExport()
	if err := validateExport(exported); err != nil {
		t.Fatalf("valid fourteen-surface export was rejected: %v", err)
	}

	missingSurface := validWorkbenchExport()
	missingSurface.Manifest.Surfaces = missingSurface.Manifest.Surfaces[:13]
	if err := validateExport(missingSurface); err == nil {
		t.Fatal("export without all fourteen surfaces was accepted")
	}

	missingAction := validWorkbenchExport()
	mainSurface := &missingAction.Manifest.Surfaces[0]
	mainSurface.HitRegions = mainSurface.HitRegions[1:]
	if err := validateExport(missingAction); err == nil {
		t.Fatal("main surface without toggle-theme was accepted")
	}

	missingStatisticsAction := validWorkbenchExport()
	statistics := surfaceByID(t, &missingStatisticsAction, "statistics")
	statistics.HitRegions = statistics.HitRegions[1:]
	if err := validateExport(missingStatisticsAction); err == nil {
		t.Fatal("statistics surface without statistics-view-month was accepted")
	}
}

func TestValidateExportRequiresExactScalesAndUniqueFiles(t *testing.T) {
	missingScale := validWorkbenchExport()
	missingScale.Manifest.Surfaces[0].Variants = missingScale.Manifest.Surfaces[0].Variants[:3]
	if err := validateExport(missingScale); err == nil {
		t.Fatal("surface with three scale variants was accepted")
	}

	unexpectedScale := validWorkbenchExport()
	unexpectedScale.Manifest.Surfaces[0].Variants[0].Scale = 1.1
	if err := validateExport(unexpectedScale); err == nil {
		t.Fatal("surface with an unexpected scale was accepted")
	}

	wrongScaledSize := validWorkbenchExport()
	variant := &wrongScaledSize.Manifest.Surfaces[0].Variants[0]
	variant.Width++
	file := exportFileByName(t, &wrongScaledSize, variant.File)
	file.Width++
	if err := validateExport(wrongScaledSize); err == nil {
		t.Fatal("variant whose dimensions do not match its scale was accepted")
	}

	reusedFile := validWorkbenchExport()
	reusedFile.Manifest.Surfaces[0].Variants[1].File =
		reusedFile.Manifest.Surfaces[0].Variants[0].File
	if err := validateExport(reusedFile); err == nil {
		t.Fatal("asset referenced by two variants was accepted")
	}

	missingFile := validWorkbenchExport()
	missingFile.Files = missingFile.Files[:len(missingFile.Files)-1]
	if err := validateExport(missingFile); err == nil {
		t.Fatal("export with fewer than 56 files was accepted")
	}
}

func TestValidateExportRequiresSurfaceDynamicContracts(t *testing.T) {
	missingMainText := validWorkbenchExport()
	mainSurface := surfaceByID(t, &missingMainText, "main-horizontal")
	mainSurface.Dynamic.Text = mainSurface.Dynamic.Text[1:]
	if err := validateExport(missingMainText); err == nil {
		t.Fatal("main surface without all required text bindings was accepted")
	}

	shortStatisticsGrid := validWorkbenchExport()
	statistics := surfaceByID(t, &shortStatisticsGrid, "statistics")
	statistics.Dynamic.Cells[0].Rects = statistics.Dynamic.Cells[0].Rects[:41]
	if err := validateExport(shortStatisticsGrid); err == nil {
		t.Fatal("statistics surface without 42 cells was accepted")
	}

	wrongToastText := validWorkbenchExport()
	toast := surfaceByID(t, &wrongToastText, "usage-toast")
	toast.Dynamic.Text[0].Bind = "runtime.model"
	if err := validateExport(wrongToastText); err == nil {
		t.Fatal("toast surface with a non-toast binding was accepted")
	}
}

func TestValidateExportRequiresMatchingThemeSlotGeometry(t *testing.T) {
	exported := validWorkbenchExport()
	light := surfaceByID(t, &exported, "main-horizontal-light")
	light.Dynamic.Text[0].Rect.X++
	if err := validateExport(exported); err == nil {
		t.Fatal("light theme with mismatched slot geometry was accepted")
	}
}

func TestNormalizeRenderedPNGRequiresCompleteVisibleImage(t *testing.T) {
	visible := testPNG(t, 8, 4, true)
	if _, err := normalizeRenderedPNG(visible, 8, 4); err != nil {
		t.Fatalf("valid PNG was rejected: %v", err)
	}
	if _, err := normalizeRenderedPNG(visible, 7, 4); err == nil {
		t.Fatal("PNG with unexpected width was accepted")
	}
	if _, err := normalizeRenderedPNG(testPNG(t, 8, 3, true), 8, 4); err == nil {
		t.Fatal("PNG shorter than the target was accepted")
	}
	tall := testPNGWithCoverage(
		t,
		8,
		8,
		0.5,
		255,
	)
	cropped, err := normalizeRenderedPNG(tall, 8, 4)
	if err != nil {
		t.Fatalf("tall PNG with a complete top-left target was rejected: %v", err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(cropped))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 8 || config.Height != 4 {
		t.Fatalf("cropped PNG is %dx%d, want 8x4", config.Width, config.Height)
	}
	if _, err := normalizeRenderedPNG(testPNG(t, 8, 4, false), 8, 4); err == nil {
		t.Fatal("fully transparent PNG was accepted")
	}
	weakAlpha := testPNGWithCoverage(
		t,
		8,
		4,
		1,
		21,
	)
	if _, err := normalizeRenderedPNG(weakAlpha, 8, 4); err == nil {
		t.Fatal("PNG without a strongly visible pixel was accepted")
	}
	partial := testPNGWithCoverage(
		t,
		20,
		20,
		0.1,
		255,
	)
	if _, err := normalizeRenderedPNG(partial, 20, 20); err == nil {
		t.Fatal("partially rendered PNG below the coverage gate was accepted")
	}
	if _, err := normalizeRenderedPNG([]byte("not a PNG"), 8, 4); err == nil {
		t.Fatal("invalid PNG was accepted")
	}
}

func TestValidateRenderedScaleCoverageRejectsPartialVariant(t *testing.T) {
	exported := validWorkbenchExport()
	renderedFiles := make([]renderedExportFile, 0, len(exported.Files))
	for _, file := range exported.Files {
		renderedFiles = append(renderedFiles, renderedExportFile{
			Name:   file.Name,
			Width:  file.Width,
			Height: file.Height,
			Contents: testPNGWithCoverage(
				t,
				file.Width,
				file.Height,
				0.6,
				255,
			),
		})
	}
	if err := validateRenderedScaleCoverage(exported.Manifest, renderedFiles); err != nil {
		t.Fatalf("consistent scale coverage was rejected: %v", err)
	}

	badVariant := exported.Manifest.Surfaces[0].Variants[3]
	for index := range renderedFiles {
		file := &renderedFiles[index]
		if file.Name == badVariant.File {
			file.Contents = testPNGWithCoverage(
				t,
				file.Width,
				file.Height,
				0.4,
				255,
			)
			break
		}
	}
	if err := validateRenderedScaleCoverage(exported.Manifest, renderedFiles); err == nil {
		t.Fatal("partially rendered scale variant was accepted")
	}
}

func TestRasterizeExportFileRetriesInvalidImages(t *testing.T) {
	file := exportFile{Name: "retry.png", Width: 8, Height: 4}
	calls := 0
	contents, err := rasterizeExportFile(file, func(exportFile) ([]byte, error) {
		calls++
		switch calls {
		case 1:
			return []byte("invalid PNG"), nil
		case 2:
			return testPNG(t, 8, 4, false), nil
		default:
			return testPNG(t, 8, 4, true), nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != rasterizeAttempts {
		t.Fatalf("rasterizer calls = %d, want %d", calls, rasterizeAttempts)
	}
	if _, _, err := image.Decode(bytes.NewReader(contents)); err != nil {
		t.Fatalf("retried rasterizer returned invalid PNG: %v", err)
	}
}

func TestRasterizeExportFileStopsAfterFiniteFailures(t *testing.T) {
	file := exportFile{Name: "failure.png", Width: 8, Height: 4}
	cause := errors.New("fixture rasterizer failure")
	calls := 0
	_, err := rasterizeExportFile(file, func(exportFile) ([]byte, error) {
		calls++
		return nil, cause
	})
	if !errors.Is(err, cause) {
		t.Fatalf("rasterizer error = %v, want %v", err, cause)
	}
	if calls != rasterizeAttempts {
		t.Fatalf("rasterizer calls = %d, want %d", calls, rasterizeAttempts)
	}
}

func TestExportGenerationChangesWithRenderedAsset(t *testing.T) {
	files := []renderedExportFile{
		{Name: "surface-b.png", Width: 10, Height: 10, Contents: []byte("first")},
		{Name: "surface-a.png", Width: 20, Height: 5, Contents: []byte("stable")},
	}
	first := exportGeneration(files)
	files[0].Contents = []byte("second")
	second := exportGeneration(files)
	if first == second {
		t.Fatal("export generation did not change with the rendered asset")
	}
	files[0].Contents = []byte("first")
	slices.Reverse(files)
	if reordered := exportGeneration(files); reordered != first {
		t.Fatalf("export generation changed with input order: %q != %q", reordered, first)
	}
}

func TestIncrementalExportReusesUnchangedRenderedFiles(t *testing.T) {
	workbenchRoot := t.TempDir()
	writeTestFile(t, filepath.Join(workbenchRoot, "index.html"))
	server := &workbenchServer{
		startedAt:     time.Date(2026, 8, 9, 12, 0, 0, 0, time.Local),
		workbenchRoot: workbenchRoot,
		bundleRoot:    t.TempDir(),
	}

	firstCalls := 0
	firstResult, err := server.writeExportWithRasterizerResult(
		validWorkbenchExportForServer(t, server),
		func(file exportFile) ([]byte, error) {
			firstCalls++
			return testPNG(t, file.Width, file.Height, true), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedFiles := len(workbenchSurfaceIDs) * len(workbenchVariantScales)
	if firstCalls != expectedFiles || firstResult.Rendered != expectedFiles || firstResult.Reused != 0 {
		t.Fatalf(
			"first export calls/rendered/reused = %d/%d/%d, want %d/%d/0",
			firstCalls,
			firstResult.Rendered,
			firstResult.Reused,
			expectedFiles,
			expectedFiles,
		)
	}

	changed := validWorkbenchExportForServer(t, server)
	changed.Files[0].HTML = `<!doctype html><html><head></head><body><div><span></span></div></body></html>`
	secondCalls := 0
	secondResult, err := server.writeExportWithRasterizerResult(
		changed,
		func(file exportFile) ([]byte, error) {
			secondCalls++
			return testPNG(t, file.Width, file.Height, true), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if secondCalls != 1 || secondResult.Rendered != 1 || secondResult.Reused != expectedFiles-1 {
		t.Fatalf(
			"incremental export calls/rendered/reused = %d/%d/%d, want 1/1/%d",
			secondCalls,
			secondResult.Rendered,
			secondResult.Reused,
			expectedFiles-1,
		)
	}

	manifest, err := readManifest(server.bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range manifest.Surfaces {
		for _, variant := range surface.Variants {
			if !isLowerHex(variant.SourceHash, 64) {
				t.Fatalf("surface %q has invalid source hash %q", surface.ID, variant.SourceHash)
			}
		}
	}
}

func TestIncrementalExportInvalidatesOnlyStatisticsSurfaces(t *testing.T) {
	workbenchRoot := t.TempDir()
	writeTestFile(t, filepath.Join(workbenchRoot, "index.html"))
	server := &workbenchServer{
		startedAt:     time.Date(2026, 8, 9, 12, 0, 0, 0, time.Local),
		workbenchRoot: workbenchRoot,
		bundleRoot:    t.TempDir(),
	}

	rasterizer := func(file exportFile) ([]byte, error) {
		return testPNG(t, file.Width, file.Height, true), nil
	}
	if err := server.writeExportWithRasterizer(
		validWorkbenchExportForServer(t, server),
		rasterizer,
	); err != nil {
		t.Fatal(err)
	}

	changed := validWorkbenchExportForServer(t, server)
	for index := range changed.Files {
		name := changed.Files[index].Name
		isStatistics := strings.HasPrefix(name, "statistics@") ||
			strings.HasPrefix(name, "statistics-light@")
		if isStatistics {
			changed.Files[index].HTML += "<!-- statistics style changed -->"
		}
	}
	calls := 0
	result, err := server.writeExportWithRasterizerResult(
		changed,
		func(file exportFile) ([]byte, error) {
			calls++
			return testPNG(t, file.Width, file.Height, true), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedRendered := 2 * len(workbenchVariantScales)
	expectedFiles := len(workbenchSurfaceIDs) * len(workbenchVariantScales)
	if calls != expectedRendered || result.Rendered != expectedRendered {
		t.Fatalf(
			"statistics export calls/rendered = %d/%d, want %d/%d",
			calls,
			result.Rendered,
			expectedRendered,
			expectedRendered,
		)
	}
	if result.Reused != expectedFiles-expectedRendered {
		t.Fatalf(
			"statistics export reused = %d, want %d",
			result.Reused,
			expectedFiles-expectedRendered,
		)
	}
}

func TestParallelExportUsesThreeRasterizerWorkers(t *testing.T) {
	workbenchRoot := t.TempDir()
	writeTestFile(t, filepath.Join(workbenchRoot, "index.html"))
	server := &workbenchServer{
		startedAt:     time.Date(2026, 8, 9, 12, 0, 0, 0, time.Local),
		workbenchRoot: workbenchRoot,
		bundleRoot:    t.TempDir(),
	}
	exported := validWorkbenchExportForServer(t, server)
	pngBySize := map[string][]byte{}
	for _, file := range exported.Files {
		key := fmt.Sprintf("%dx%d", file.Width, file.Height)
		if _, found := pngBySize[key]; !found {
			pngBySize[key] = testPNG(t, file.Width, file.Height, true)
		}
	}

	started := make(chan struct{}, len(exported.Files))
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	done := make(chan struct{})
	var result exportResult
	var exportErr error
	go func() {
		result, exportErr = server.writeExportWithRasterizerWorkers(
			exported,
			func(file exportFile) ([]byte, error) {
				current := active.Add(1)
				defer active.Add(-1)
				for {
					observed := maximum.Load()
					if current <= observed || maximum.CompareAndSwap(observed, current) {
						break
					}
				}
				started <- struct{}{}
				<-release
				return pngBySize[fmt.Sprintf("%dx%d", file.Width, file.Height)], nil
			},
			exportRasterizerWorkers,
		)
		close(done)
	}()

	for range exportRasterizerWorkers {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("three rasterizer workers did not start concurrently")
		}
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("parallel export did not finish")
	}
	if exportErr != nil {
		t.Fatal(exportErr)
	}
	if maximum.Load() != exportRasterizerWorkers {
		t.Fatalf(
			"maximum concurrent rasterizers = %d, want %d",
			maximum.Load(),
			exportRasterizerWorkers,
		)
	}
	expectedFiles := len(workbenchSurfaceIDs) * len(workbenchVariantScales)
	if result.Rendered != expectedFiles || result.Reused != 0 {
		t.Fatalf("parallel export result = %+v", result)
	}
}

func TestAtlasExportRendersAtMostOneScreenshotPerScale(t *testing.T) {
	workbenchRoot := t.TempDir()
	writeTestFile(t, filepath.Join(workbenchRoot, "index.html"))
	server := &workbenchServer{
		startedAt:     time.Date(2026, 8, 9, 12, 0, 0, 0, time.Local),
		workbenchRoot: workbenchRoot,
		bundleRoot:    t.TempDir(),
	}
	var calls atomic.Int32
	result, err := server.writeExportWithAtlasRasterizerResult(
		validWorkbenchExportForServer(t, server),
		func(file exportFile) ([]byte, error) {
			calls.Add(1)
			if !strings.HasPrefix(file.Name, "atlas@") {
				return nil, fmt.Errorf("cold atlas export rasterized individual file %q", file.Name)
			}
			if got := strings.Count(file.HTML, "<iframe "); got != len(workbenchSurfaceIDs) {
				return nil, fmt.Errorf(
					"atlas %q contains %d surfaces, want %d",
					file.Name,
					got,
					len(workbenchSurfaceIDs),
				)
			}
			return testPNG(t, file.Width, file.Height, true), nil
		},
		exportRasterizerWorkers,
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedFiles := len(workbenchSurfaceIDs) * len(workbenchVariantScales)
	if calls.Load() != int32(len(workbenchVariantScales)) ||
		result.Atlases != len(workbenchVariantScales) ||
		result.Fallback != 0 || result.Rendered != expectedFiles {
		t.Fatalf("atlas export calls/result = %d/%+v", calls.Load(), result)
	}
	t.Run("individual fallback is reported", testAtlasExportReportsIndividualFallback)
}

func testAtlasExportReportsIndividualFallback(t *testing.T) {
	workbenchRoot := t.TempDir()
	writeTestFile(t, filepath.Join(workbenchRoot, "index.html"))
	server := &workbenchServer{
		startedAt:     time.Date(2026, 8, 9, 12, 0, 0, 0, time.Local),
		workbenchRoot: workbenchRoot,
		bundleRoot:    t.TempDir(),
	}
	result, err := server.writeExportWithAtlasRasterizerResult(
		validWorkbenchExportForServer(t, server),
		func(file exportFile) ([]byte, error) {
			if strings.HasPrefix(file.Name, "atlas@") {
				return nil, errors.New("injected atlas failure")
			}
			return testPNG(t, file.Width, file.Height, true), nil
		},
		exportRasterizerWorkers,
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedFiles := len(workbenchSurfaceIDs) * len(workbenchVariantScales)
	if result.Atlases != 0 || result.Fallback != expectedFiles ||
		result.Rendered != expectedFiles {
		t.Fatalf("atlas fallback result = %+v", result)
	}
}

func TestExportPreflightSkipsCurrentValidBundle(t *testing.T) {
	workbenchRoot := t.TempDir()
	staticPath := filepath.Join(workbenchRoot, "index.html")
	writeTestFile(t, staticPath)
	server := &workbenchServer{
		startedAt:     time.Date(2026, 8, 9, 12, 0, 0, 0, time.Local),
		workbenchRoot: workbenchRoot,
		bundleRoot:    t.TempDir(),
	}
	if err := server.writeExportWithRasterizer(
		validWorkbenchExportForServer(t, server),
		func(file exportFile) ([]byte, error) {
			return testPNG(t, file.Width, file.Height, true), nil
		},
	); err != nil {
		t.Fatal(err)
	}

	clientVersion, err := server.currentVersion()
	if err != nil {
		t.Fatal(err)
	}
	result, err := server.preflightExport("main-vertical-light", clientVersion)
	if err != nil {
		t.Fatal(err)
	}
	expectedFiles := len(workbenchSurfaceIDs) * len(workbenchVariantScales)
	if !result.UpToDate || result.Rendered != 0 || result.Reused != expectedFiles {
		t.Fatalf("preflight result = %+v", result)
	}
	manifest, err := readManifest(server.bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.DefaultSurface != "main-vertical-light" {
		t.Fatalf("default surface = %q, want main-vertical-light", manifest.DefaultSurface)
	}
	server.resolveEdge = func() (edgeRendererIdentity, error) {
		return edgeRendererIdentity{
			Version:     "152.0.0.0",
			Fingerprint: "fedcba9876543210",
		}, nil
	}
	result, err = server.preflightExport("main-vertical-light", clientVersion)
	if err != nil {
		t.Fatal(err)
	}
	if result.UpToDate {
		t.Fatal("preflight reused pixels from a different Edge renderer")
	}
	server.resolveEdge = fixedTestEdgeRendererIdentity

	if err := os.WriteFile(staticPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = server.preflightExport("main-horizontal", clientVersion)
	if err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("preflight accepted a stale page version: result=%+v err=%v", result, err)
	}
	currentVersion, err := server.currentVersion()
	if err != nil {
		t.Fatal(err)
	}
	result, err = server.preflightExport("main-horizontal", currentVersion)
	if err != nil {
		t.Fatal(err)
	}
	if result.UpToDate {
		t.Fatal("preflight skipped export after the static fingerprint changed")
	}
}

func TestExportSourceHashCoversEveryRenderInput(t *testing.T) {
	file := exportFile{Name: "surface.png", HTML: "<div></div>", Width: 10, Height: 20}
	original := exportSourceHash(file)
	changes := []exportFile{
		{Name: "renamed.png", HTML: file.HTML, Width: file.Width, Height: file.Height},
		{Name: file.Name, HTML: "<span></span>", Width: file.Width, Height: file.Height},
		{Name: file.Name, HTML: file.HTML, Width: file.Width + 1, Height: file.Height},
		{Name: file.Name, HTML: file.HTML, Width: file.Width, Height: file.Height + 1},
	}
	for _, changed := range changes {
		if exportSourceHash(changed) == original {
			t.Fatalf("source hash did not change for %+v", changed)
		}
	}
	firstRenderer := exportSourceHashForRenderer(file, "0123456789abcdef")
	secondRenderer := exportSourceHashForRenderer(file, "fedcba9876543210")
	if firstRenderer == secondRenderer ||
		!strings.HasPrefix(firstRenderer, "0123456789abcdef") ||
		!strings.HasPrefix(secondRenderer, "fedcba9876543210") {
		t.Fatal("source hash does not identify the Edge renderer")
	}
}

func TestExistingExportGenerationIsFullyRevalidated(t *testing.T) {
	workbenchRoot := t.TempDir()
	writeTestFile(t, filepath.Join(workbenchRoot, "index.html"))
	bundleRoot := t.TempDir()
	server := &workbenchServer{
		startedAt:     time.Date(2026, 8, 9, 12, 0, 0, 0, time.Local),
		workbenchRoot: workbenchRoot,
		bundleRoot:    bundleRoot,
	}
	exported := validWorkbenchExportForServer(t, server)
	rendered := make([]renderedExportFile, 0, len(exported.Files))
	for _, file := range exported.Files {
		contents, err := normalizeRenderedPNG(
			testPNG(t, file.Width, file.Height, true),
			file.Width,
			file.Height,
		)
		if err != nil {
			t.Fatal(err)
		}
		rendered = append(rendered, renderedExportFile{
			Name: file.Name, Width: file.Width, Height: file.Height, Contents: contents,
		})
	}
	generation := exportGeneration(rendered)
	assetRoot := filepath.Join(bundleRoot, "assets", generation)
	for _, file := range rendered {
		if err := os.MkdirAll(assetRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(assetRoot, file.Name), file.Contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(assetRoot, rendered[0].Name),
		[]byte("corrupt generation collision"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	err := server.writeExportWithRasterizer(
		exported,
		func(file exportFile) ([]byte, error) {
			return testPNG(t, file.Width, file.Height, true), nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "validating existing export generation") {
		t.Fatalf("corrupt existing generation error = %v", err)
	}
	assertPathMissing(t, filepath.Join(bundleRoot, "manifest.json"))
}

func TestSuccessfulExportsKeepCurrentAndPreviousGeneration(t *testing.T) {
	workbenchRoot := t.TempDir()
	writeTestFile(t, filepath.Join(workbenchRoot, "index.html"))
	bundleRoot := t.TempDir()
	server := &workbenchServer{
		startedAt:     time.Date(2026, 8, 9, 12, 0, 0, 0, time.Local),
		workbenchRoot: workbenchRoot,
		bundleRoot:    bundleRoot,
	}
	exportGenerationWithAlpha := func(marker string, alpha uint8) string {
		t.Helper()
		exported := validWorkbenchExportForServer(t, server)
		for index := range exported.Files {
			exported.Files[index].HTML += "<!-- " + marker + " -->"
		}
		if err := server.writeExportWithRasterizer(
			exported,
			func(file exportFile) ([]byte, error) {
				return testPNGWithCoverage(t, file.Width, file.Height, 1, alpha), nil
			},
		); err != nil {
			t.Fatal(err)
		}
		manifest, err := readManifest(bundleRoot)
		if err != nil {
			t.Fatal(err)
		}
		generations, err := referencedExportGenerations(manifest)
		if err != nil {
			t.Fatal(err)
		}
		for generation := range generations {
			return generation
		}
		t.Fatal("committed export references no generation")
		return ""
	}

	first := exportGenerationWithAlpha("first", 255)
	second := exportGenerationWithAlpha("second", 254)
	if first == second {
		t.Fatal("fixture exports produced the same generation")
	}
	assertPathExists(t, filepath.Join(bundleRoot, "assets", first))
	assertPathExists(t, filepath.Join(bundleRoot, "assets", second))

	sameSecond := exportGenerationWithAlpha("second-source-only-change", 254)
	if sameSecond != second {
		t.Fatalf("source-only change generation = %q, want %q", sameSecond, second)
	}
	assertPathExists(t, filepath.Join(bundleRoot, "assets", first))
	assertPathExists(t, filepath.Join(bundleRoot, "assets", second))

	third := exportGenerationWithAlpha("third", 253)
	if second == third {
		t.Fatal("fixture exports produced the same generation")
	}
	assertPathMissing(t, filepath.Join(bundleRoot, "assets", first))
	assertPathExists(t, filepath.Join(bundleRoot, "assets", second))
	assertPathExists(t, filepath.Join(bundleRoot, "assets", third))
	entries, err := os.ReadDir(filepath.Join(bundleRoot, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("asset generations = %d, want current and previous", len(entries))
	}
}

func TestCleanupCommittedExportArtifactsKeepsReferencedGeneration(t *testing.T) {
	bundleRoot := t.TempDir()
	currentGeneration := "1111111111111111"
	staleGeneration := "2222222222222222"
	writeCommittedTestManifest(t, bundleRoot, currentGeneration)
	writeTestFile(
		t,
		filepath.Join(bundleRoot, "assets", currentGeneration, "keep.png"),
	)
	writeTestFile(
		t,
		filepath.Join(bundleRoot, "assets", staleGeneration, "remove.png"),
	)
	writeTestFile(
		t,
		filepath.Join(bundleRoot, "assets", "manual-assets", "keep.png"),
	)
	legacyPath := filepath.Join(bundleRoot, "main-horizontal@100.png")
	writeTestFile(t, legacyPath)
	unrelatedPNG := filepath.Join(bundleRoot, "user-preview@100.png")
	writeTestFile(t, unrelatedPNG)
	stagingPath := filepath.Join(bundleRoot, ".export-staging-123456")
	writeTestFile(t, filepath.Join(stagingPath, "remove.png"))
	manualStaging := filepath.Join(bundleRoot, ".export-staging-manual")
	writeTestFile(t, filepath.Join(manualStaging, "keep.png"))

	if err := cleanupCommittedExportArtifacts(bundleRoot); err != nil {
		t.Fatal(err)
	}
	assertPathExists(
		t,
		filepath.Join(bundleRoot, "assets", currentGeneration, "keep.png"),
	)
	assertPathMissing(t, filepath.Join(bundleRoot, "assets", staleGeneration))
	assertPathExists(
		t,
		filepath.Join(bundleRoot, "assets", "manual-assets", "keep.png"),
	)
	assertPathMissing(t, legacyPath)
	assertPathExists(t, unrelatedPNG)
	assertPathMissing(t, stagingPath)
	assertPathExists(t, filepath.Join(manualStaging, "keep.png"))
}

func TestCleanupCommittedExportArtifactsRequiresReadableManifest(t *testing.T) {
	bundleRoot := t.TempDir()
	staleGeneration := filepath.Join(bundleRoot, "assets", "2222222222222222")
	writeTestFile(t, filepath.Join(staleGeneration, "keep.png"))
	legacyPath := filepath.Join(bundleRoot, "main-horizontal@100.png")
	writeTestFile(t, legacyPath)
	if err := os.WriteFile(
		filepath.Join(bundleRoot, "manifest.json"),
		[]byte("not JSON"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if err := cleanupCommittedExportArtifacts(bundleRoot); err == nil {
		t.Fatal("cleanup accepted an unreadable committed manifest")
	}
	assertPathExists(t, filepath.Join(staleGeneration, "keep.png"))
	assertPathExists(t, legacyPath)
}

func TestSuccessfulExportCleansOnlyStaleGeneratedArtifacts(t *testing.T) {
	workbenchRoot := t.TempDir()
	writeTestFile(t, filepath.Join(workbenchRoot, "index.html"))
	bundleRoot := t.TempDir()
	staleGeneration := filepath.Join(bundleRoot, "assets", "2222222222222222")
	writeTestFile(t, filepath.Join(staleGeneration, "remove.png"))
	legacyPath := filepath.Join(bundleRoot, "main-horizontal@100.png")
	writeTestFile(t, legacyPath)
	stagingPath := filepath.Join(bundleRoot, ".export-staging-987654")
	writeTestFile(t, filepath.Join(stagingPath, "remove.png"))

	server := &workbenchServer{
		startedAt:     time.Date(2026, 8, 9, 12, 0, 0, 0, time.Local),
		workbenchRoot: workbenchRoot,
		bundleRoot:    bundleRoot,
	}
	err := server.writeExportWithRasterizer(
		validWorkbenchExportForServer(t, server),
		func(file exportFile) ([]byte, error) {
			return testPNG(t, file.Width, file.Height, true), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := readManifest(bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := referencedExportGenerations(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(protected) != 1 {
		t.Fatalf("committed manifest references %d generations, want 1", len(protected))
	}
	for generation := range protected {
		assertPathExists(t, filepath.Join(bundleRoot, "assets", generation))
	}
	assertPathMissing(t, staleGeneration)
	assertPathMissing(t, legacyPath)
	assertPathMissing(t, stagingPath)
}

func TestCleanupDirectChildPathRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"", ".", "..", `..\outside`, "nested/child"} {
		if _, err := cleanupDirectChildPath(root, name); err == nil {
			t.Errorf("cleanup child %q was accepted", name)
		}
	}
	path, err := cleanupDirectChildPath(root, "expected")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(root, "expected") {
		t.Fatalf("cleanup path = %q, want direct child", path)
	}
}

func TestRemoveTreeWithRetryWaitsForRelease(t *testing.T) {
	cause := errors.New("file still in use")
	calls := 0
	err := removeTreeWithRetry(
		"temporary-export",
		4,
		0,
		func(string) error {
			calls++
			if calls < 3 {
				return cause
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("remove calls = %d, want 3", calls)
	}
}

func TestRemoveTreeWithRetryReportsFinalFailure(t *testing.T) {
	cause := errors.New("file remains in use")
	calls := 0
	err := removeTreeWithRetry(
		"temporary-export",
		3,
		0,
		func(string) error {
			calls++
			return cause
		},
	)
	if !errors.Is(err, cause) {
		t.Fatalf("cleanup error = %v, want %v", err, cause)
	}
	if calls != 3 {
		t.Fatalf("remove calls = %d, want 3", calls)
	}
}

func TestOwnedTemporaryExportPathRequiresDirectGeneratedChild(t *testing.T) {
	expected := filepath.Join(os.TempDir(), "codexfloatingbar-export-123456")
	path, err := ownedTemporaryExportPath(expected)
	if err != nil {
		t.Fatal(err)
	}
	if path != expected {
		t.Fatalf("owned temporary path = %q, want %q", path, expected)
	}
	nested := filepath.Join(t.TempDir(), "codexfloatingbar-export-123456")
	if _, err := ownedTemporaryExportPath(nested); err == nil {
		t.Fatal("nested temporary export path was accepted")
	}
	if _, err := ownedTemporaryExportPath(
		filepath.Join(os.TempDir(), "unrelated-123456"),
	); err == nil {
		t.Fatal("unrelated temporary path was accepted")
	}
}

func TestExportedBundleMatchesWorkbenchFingerprint(t *testing.T) {
	fingerprint, err := fingerprintTree("ui/workbench")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := readManifest("ui/dist")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version.StaticVersion != fingerprint {
		t.Fatalf(
			"bundle fingerprint %q does not match workbench %q; export the UI again",
			manifest.Version.StaticVersion,
			fingerprint,
		)
	}
}

func TestExportedUsageToastSlotsUseStableTextColumn(t *testing.T) {
	manifest, err := readManifest("ui/dist")
	if err != nil {
		t.Fatal(err)
	}

	var expected slotRect
	var expectedFont string
	var expectedFontFamilies []string
	var found int
	for _, surface := range manifest.Surfaces {
		if !strings.HasPrefix(surface.ID, "usage-toast") {
			continue
		}
		if len(surface.Dynamic.Text) != 2 {
			t.Fatalf("surface %q has %d text slots, want 2", surface.ID, len(surface.Dynamic.Text))
		}
		for _, slot := range surface.Dynamic.Text {
			if strings.TrimSpace(slot.FontFamily) == "" {
				t.Fatalf("surface %q slot %q has an empty font", surface.ID, slot.Bind)
			}
			if found == 0 {
				expectedFont = slot.FontFamily
				expectedFontFamilies = slot.FontFamilies
			} else if slot.FontFamily != expectedFont {
				t.Fatalf(
					"surface %q slot %q font = %q, want %q",
					surface.ID,
					slot.Bind,
					slot.FontFamily,
					expectedFont,
				)
			}
			if len(slot.FontFamilies) == 0 ||
				slot.FontFamilies[0] != slot.FontFamily ||
				!slices.Equal(slot.FontFamilies, expectedFontFamilies) {
				t.Fatalf(
					"surface %q slot %q font candidates = %q, want %q",
					surface.ID,
					slot.Bind,
					slot.FontFamilies,
					expectedFontFamilies,
				)
			}
			textColumn := slotRect{X: slot.Rect.X, Width: slot.Rect.Width}
			if found == 0 {
				expected = textColumn
			} else if math.Abs(textColumn.X-expected.X) > 0.001 ||
				math.Abs(textColumn.Width-expected.Width) > 0.001 {
				t.Fatalf(
					"surface %q slot %q column = %+v, want %+v",
					surface.ID,
					slot.Bind,
					textColumn,
					expected,
				)
			}
			found++
		}
	}
	if found == 0 {
		t.Fatal("usage toast text slots were not found")
	}
}

func TestExportedStatisticsBaseDoesNotBakeMonthCells(t *testing.T) {
	manifest, err := readManifest("ui/dist")
	if err != nil {
		t.Fatal(err)
	}
	var statistics *bundleSurface
	for index := range manifest.Surfaces {
		if manifest.Surfaces[index].ID == "statistics" {
			statistics = &manifest.Surfaces[index]
			break
		}
	}
	if statistics == nil {
		t.Fatal("statistics surface was not found")
	}
	if len(statistics.Dynamic.Cells) != 1 || len(statistics.Dynamic.Cells[0].Rects) < 2 {
		t.Fatal("statistics surface does not contain the month cell slot")
	}
	var variant bundleVariant
	for _, candidate := range statistics.Variants {
		if candidate.Scale == 1 {
			variant = candidate
			break
		}
	}
	if variant.File == "" {
		t.Fatal("statistics surface does not contain the 100% variant")
	}

	file, err := os.Open(filepath.Join("ui/dist", filepath.FromSlash(variant.File)))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	base, _, err := image.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	first := statistics.Dynamic.Cells[0].Rects[0]
	second := statistics.Dynamic.Cells[0].Rects[1]
	center := image.Pt(
		int(first.X+first.Width/2),
		int(first.Y+first.Height/2),
	)
	gap := image.Pt(
		int((first.X+first.Width+second.X)/2),
		center.Y,
	)
	cellColor := color.NRGBAModel.Convert(base.At(center.X, center.Y)).(color.NRGBA)
	backgroundColor := color.NRGBAModel.Convert(base.At(gap.X, gap.Y)).(color.NRGBA)
	if cellColor != backgroundColor {
		t.Fatalf(
			"statistics base contains a baked month cell at %v: got %+v, background %+v; export the UI again",
			center,
			cellColor,
			backgroundColor,
		)
	}
}

func TestExportedStatisticsDetailSlotsUseCardWidth(t *testing.T) {
	manifest, err := readManifest("ui/dist")
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range manifest.Surfaces {
		if surface.ID != "statistics" {
			continue
		}
		found := 0
		for _, slot := range surface.Dynamic.Text {
			if !strings.HasPrefix(slot.Bind, "statistics.detail") {
				continue
			}
			minimum := 70.0
			if slot.Bind == "statistics.detailCost" {
				minimum = 150
			}
			if slot.Rect.Width < minimum {
				t.Fatalf("%s width = %.2f, want at least %.2f", slot.Bind, slot.Rect.Width, minimum)
			}
			found++
		}
		if found != 7 {
			t.Fatalf("detail value slots = %d, want 7", found)
		}
		return
	}
	t.Fatal("statistics surface was not found")
}

func validWorkbenchExport() exportRequest {
	surfaces := make([]bundleSurface, 0, len(workbenchSurfaceIDs))
	files := make(
		[]exportFile,
		0,
		len(workbenchSurfaceIDs)*len(workbenchVariantScales),
	)
	for _, surfaceID := range workbenchSurfaceIDs {
		actions := []string{"hide"}
		switch {
		case isMainWorkbenchSurfaceID(surfaceID):
			actions = []string{
				"toggle-theme",
				"toggle-statistics",
				"toggle-toast",
				"toggle-layout",
				"hide",
			}
		case strings.TrimSuffix(surfaceID, "-light") == "statistics":
			actions = []string{
				"statistics-view-month",
				"statistics-view-week",
				"statistics-view-cumulative",
				"statistics-previous-month",
				"statistics-next-month",
			}
		}
		hitRegions := make([]hitRegion, 0, len(actions))
		for actionIndex, action := range actions {
			hitRegions = append(hitRegions, hitRegion{
				Action: action,
				X:      float64(actionIndex),
				Y:      0,
				Width:  1,
				Height: 1,
			})
		}
		variants := make([]bundleVariant, 0, len(workbenchVariantScales))
		for _, scale := range workbenchVariantScales {
			percentage := int(scale*100 + 0.5)
			fileName := fmt.Sprintf("%s@%d.png", surfaceID, percentage)
			width := int(100 * scale)
			height := int(80 * scale)
			variants = append(variants, bundleVariant{
				Scale:  scale,
				File:   fileName,
				Width:  width,
				Height: height,
			})
			files = append(files, exportFile{
				Name:   fileName,
				HTML:   `<!doctype html><html><head></head><body><div></div></body></html>`,
				Width:  width,
				Height: height,
			})
		}
		surfaces = append(surfaces, bundleSurface{
			ID:            surfaceID,
			LogicalWidth:  100,
			LogicalHeight: 80,
			HitRegions:    hitRegions,
			Dynamic:       validWorkbenchDynamicSlots(surfaceID),
			Variants:      variants,
		})
	}

	return exportRequest{
		Manifest: bundleManifest{
			Schema:         bundleSchema,
			Project:        projectID,
			DefaultSurface: "main-horizontal",
			Surfaces:       surfaces,
		},
		Files: files,
	}
}

func validWorkbenchExportForServer(
	t *testing.T,
	server *workbenchServer,
) exportRequest {
	t.Helper()
	if server.resolveEdge == nil {
		server.resolveEdge = fixedTestEdgeRendererIdentity
	}
	exported := validWorkbenchExport()
	version, err := server.currentVersion()
	if err != nil {
		t.Fatal(err)
	}
	exported.Manifest.Version = version
	renderer, err := server.currentEdgeRendererIdentity()
	if err != nil {
		t.Fatal(err)
	}
	exported.Renderer = renderer.Version
	return exported
}

func fixedTestEdgeRendererIdentity() (edgeRendererIdentity, error) {
	return edgeRendererIdentity{
		Version:     "151.0.4129.86",
		Fingerprint: "0123456789abcdef",
	}, nil
}

func validWorkbenchDynamicSlots(surfaceID string) dynamicSlots {
	baseID := strings.TrimSuffix(surfaceID, "-light")
	newTextSlots := func(bindings []string) []textSlot {
		slots := make([]textSlot, 0, len(bindings))
		for index, binding := range bindings {
			slots = append(slots, textSlot{
				Bind: binding,
				Rect: slotRect{
					X:      float64(1 + index%4*24),
					Y:      float64(1 + index/4*9),
					Width:  22,
					Height: 6,
				},
				FontFamily: "Segoe UI",
				FontSize:   5,
				FontWeight: 400,
				Color:      "#112233",
				Align:      "left",
			})
		}
		return slots
	}

	switch {
	case strings.HasPrefix(baseID, "main-"):
		text := newTextSlots(mainWorkbenchTextBindings[:])
		for index := range text {
			if text[index].Bind == "quota.remaining" {
				text[index].ToneColors = testToneColors()
			}
		}
		return dynamicSlots{
			Text: text,
			Progress: []progressSlot{{
				Bind:       "quota.progress",
				Rect:       slotRect{X: 1, Y: 45, Width: 50, Height: 4},
				Color:      "#112233",
				ToneColors: testToneColors(),
			}},
			Cells: []cellSlot{},
		}
	case baseID == "statistics":
		cellRects := make([]slotRect, 0, 42)
		for index := range 42 {
			cellRects = append(cellRects, slotRect{
				X:      float64(1 + index%7*3),
				Y:      float64(45 + index/7*3),
				Width:  2,
				Height: 2,
			})
		}
		return dynamicSlots{
			Text:     newTextSlots(statisticsWorkbenchTextBindings[:]),
			Progress: []progressSlot{},
			Cells: []cellSlot{{
				Bind:   "statistics.monthCells",
				Rects:  cellRects,
				Colors: []string{"#000000", "#111111", "#222222", "#333333", "#444444"},
			}},
		}
	default:
		return dynamicSlots{
			Text:     newTextSlots(toastWorkbenchTextBindings[:]),
			Progress: []progressSlot{},
			Cells:    []cellSlot{},
		}
	}
}

func testToneColors() toneColors {
	return toneColors{
		Good:    "#112233",
		Warn:    "#223344",
		Danger:  "#334455",
		Offline: "#445566",
	}
}

func surfaceByID(
	t *testing.T,
	exported *exportRequest,
	surfaceID string,
) *bundleSurface {
	t.Helper()
	for index := range exported.Manifest.Surfaces {
		surface := &exported.Manifest.Surfaces[index]
		if surface.ID == surfaceID {
			return surface
		}
	}
	t.Fatalf("surface %q was not found", surfaceID)
	return nil
}

func exportFileByName(
	t *testing.T,
	exported *exportRequest,
	name string,
) *exportFile {
	t.Helper()
	for index := range exported.Files {
		file := &exported.Files[index]
		if file.Name == name {
			return file
		}
	}
	t.Fatalf("export file %q was not found", name)
	return nil
}

func writeCommittedTestManifest(t *testing.T, bundleRoot string, generation string) {
	t.Helper()
	manifest := validWorkbenchExport().Manifest
	for surfaceIndex := range manifest.Surfaces {
		surface := &manifest.Surfaces[surfaceIndex]
		for variantIndex := range surface.Variants {
			variant := &surface.Variants[variantIndex]
			variant.File = filepath.ToSlash(
				filepath.Join("assets", generation, variant.File),
			)
		}
	}
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(
		filepath.Join(bundleRoot, "manifest.json"),
		contents,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %q to exist: %v", path, err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %q to be absent, got %v", path, err)
	}
}

func assertFileContents(t *testing.T, path string, expected []byte) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, expected) {
		t.Fatalf("file %q changed: %q", path, contents)
	}
}

func testPNG(t *testing.T, width int, height int, visible bool) []byte {
	t.Helper()
	coverage := 0.0
	if visible {
		coverage = 1
	}
	return testPNGWithCoverage(
		t,
		width,
		height,
		coverage,
		255,
	)
}

func testPNGWithCoverage(
	t *testing.T,
	width int,
	height int,
	coverage float64,
	alpha uint8,
) []byte {
	t.Helper()
	if coverage < 0 || coverage > 1 {
		t.Fatalf("invalid test coverage %.4f", coverage)
	}
	rendered := image.NewNRGBA(image.Rect(0, 0, width, height))
	strongPixels := int(float64(width*height)*coverage + 0.5)
	pixel := color.NRGBA{R: 17, G: 119, B: 204, A: alpha}
	for index := range strongPixels {
		offset := index * 4
		rendered.Pix[offset] = pixel.R
		rendered.Pix[offset+1] = pixel.G
		rendered.Pix[offset+2] = pixel.B
		rendered.Pix[offset+3] = pixel.A
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, rendered); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
