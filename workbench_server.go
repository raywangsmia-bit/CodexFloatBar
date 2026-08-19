//go:build windows && workbench

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"image"
	"image/draw"
	"image/png"
	"io"
	"log"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	maxExportSize              = 32 << 20
	maxExportPreflightSize     = 1 << 10
	maxAutoExportReportSize    = 8 << 10
	maxExportFiles             = 64
	maxExportPixels            = 64 << 20
	maxNativePreviewSize       = 64 << 10
	maxSurfaceDimension        = 4096
	rasterizeAttempts          = 3
	exportRasterizerWorkers    = 3
	temporaryCleanupAttempts   = 6
	temporaryCleanupRetryDelay = 100 * time.Millisecond
	headlessMinimumHeight      = 600
	minimumStrongAlphaCoverage = 0.25
	maximumScaleCoverageDelta  = 0.08
	exportAtlasGutter          = 8
	maxExportAtlasDimension    = 16384
	maxExportAtlasHTML         = 64 << 20
	exportRendererStampLength  = 16
)

var workbenchVariantScales = [...]float64{1, 1.25, 1.5, 2}

var mainWorkbenchTextBindings = [...]string{
	"runtime.model",
	"runtime.effort",
	"runtime.speed",
	"quota.plan",
	"quota.remaining",
	"quota.reset",
}

var statisticsWorkbenchTextBindings = [...]string{
	"statistics.total",
	"statistics.peak",
	"statistics.duration",
	"statistics.currentStreak",
	"statistics.longestStreak",
	"statistics.month",
	"statistics.previousMonth",
	"statistics.nextMonth",
	"statistics.viewMonth",
	"statistics.viewWeek",
	"statistics.viewCumulative",
	"statistics.viewDetail",
	"statistics.detailInput",
	"statistics.detailOutput",
	"statistics.detailTotal",
	"statistics.detailCached",
	"statistics.detailReasoning",
	"statistics.detailCacheHit",
	"statistics.detailCost",
	"statistics.labelInput",
	"statistics.labelOutput",
	"statistics.labelTotal",
	"statistics.labelCached",
	"statistics.labelReasoning",
	"statistics.labelCacheHit",
	"statistics.labelCost",
}

var toastWorkbenchTextBindings = [...]string{
	"toast.title",
	"toast.message",
}

var workbenchSurfaceIDs = [...]string{
	"main-horizontal",
	"main-vertical",
	"statistics",
	"usage-toast",
	"usage-toast-good",
	"usage-toast-danger",
	"usage-toast-offline",
	"main-horizontal-light",
	"main-vertical-light",
	"statistics-light",
	"usage-toast-light",
	"usage-toast-good-light",
	"usage-toast-danger-light",
	"usage-toast-offline-light",
}

type workbenchServer struct {
	startedAt     time.Time
	workbenchRoot string
	bundleRoot    string
	origin        string
	exportToken   string
	exportMu      sync.Mutex
	resolveEdge   func() (edgeRendererIdentity, error)
	autoExport    bool
	autoReportMu  sync.Mutex
	autoReported  bool
	autoReports   chan autoExportReport
	autoSuccess   *exportResult
}

type workbenchServerOptions struct {
	AutoExport bool
}

type runningWorkbenchServer struct {
	URL         string
	AutoReports <-chan autoExportReport
	httpServer  *http.Server
	serveDone   <-chan error
}

type exportRequest struct {
	Manifest bundleManifest `json:"manifest"`
	Files    []exportFile   `json:"files"`
	Renderer string         `json:"renderer"`
}

type edgeRendererIdentity struct {
	Version     string
	Fingerprint string
}

type exportFile struct {
	Name   string `json:"name"`
	HTML   string `json:"html"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type renderedExportFile struct {
	Name     string
	Width    int
	Height   int
	Contents []byte
}

type exportRenderJob struct {
	order     int
	fileIndex int
	file      exportFile
}

type exportRenderOutcome struct {
	job      exportRenderJob
	contents []byte
	err      error
}

type exportAtlasPlacement struct {
	job  exportRenderJob
	rect image.Rectangle
}

type exportAtlas struct {
	scaleIndex int
	file       exportFile
	placements []exportAtlasPlacement
}

type exportAtlasOutcome struct {
	atlas    exportAtlas
	contents []exportRenderOutcome
	err      error
}

type exportResult struct {
	OK       bool `json:"ok"`
	UpToDate bool `json:"upToDate"`
	Surfaces int  `json:"surfaces"`
	Files    int  `json:"files"`
	Rendered int  `json:"rendered"`
	Reused   int  `json:"reused"`
	Atlases  int  `json:"atlases"`
	Fallback int  `json:"fallback"`
}

type autoExportReport struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	UpToDate bool   `json:"upToDate,omitempty"`
	Files    int    `json:"files,omitempty"`
	Rendered int    `json:"rendered,omitempty"`
	Reused   int    `json:"reused,omitempty"`
	Atlases  int    `json:"atlases,omitempty"`
	Fallback int    `json:"fallback,omitempty"`
}

type exportPreflightRequest struct {
	DefaultSurface string      `json:"defaultSurface"`
	Version        pageVersion `json:"version"`
}

type nativePreviewRequest struct {
	SurfaceID      string            `json:"surfaceId"`
	Text           map[string]string `json:"text"`
	Progress       map[string]int    `json:"progress"`
	Cells          map[string][]int  `json:"cells"`
	Tone           quotaTone         `json:"tone"`
	StatisticsView statisticsView    `json:"statisticsView"`
	ChartValues    []int64           `json:"chartValues"`
}

type cachedExportAsset struct {
	Path       string
	SourceHash string
	Width      int
	Height     int
}

type renderedAlphaMetrics struct {
	strongPixels int
	totalPixels  int
	maxAlpha     uint32
}

func startWorkbenchServer(
	address string,
	startedAt time.Time,
	workbenchRoot string,
	bundleRoot string,
) (string, error) {
	running, err := startWorkbenchServerWithOptions(
		address,
		startedAt,
		workbenchRoot,
		bundleRoot,
		workbenchServerOptions{},
	)
	if err != nil {
		return "", err
	}
	return running.URL, nil
}

func startWorkbenchServerWithOptions(
	address string,
	startedAt time.Time,
	workbenchRoot string,
	bundleRoot string,
	options workbenchServerOptions,
) (*runningWorkbenchServer, error) {
	if err := requireLoopbackAddress(address); err != nil {
		return nil, err
	}
	token, err := newWorkbenchToken()
	if err != nil {
		return nil, err
	}
	server := &workbenchServer{
		startedAt:     startedAt,
		workbenchRoot: workbenchRoot,
		bundleRoot:    bundleRoot,
		exportToken:   token,
		autoExport:    options.AutoExport,
	}
	if options.AutoExport {
		server.autoReports = make(chan autoExportReport, 1)
	}

	mux := server.routes()

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("starting workbench listener: %w", err)
	}
	server.origin = "http://" + listener.Addr().String()

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveDone := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		if err != nil {
			log.Printf("workbench server stopped: %v", err)
		}
		serveDone <- err
	}()

	return &runningWorkbenchServer{
		URL:         server.origin + "/",
		AutoReports: server.autoReports,
		httpServer:  httpServer,
		serveDone:   serveDone,
	}, nil
}

func (running *runningWorkbenchServer) Close() error {
	if running == nil || running.httpServer == nil {
		return nil
	}
	closeErr := running.httpServer.Close()
	if errors.Is(closeErr, http.ErrServerClosed) {
		closeErr = nil
	}
	serveErr := <-running.serveDone
	if err := errors.Join(closeErr, serveErr); err != nil {
		return fmt.Errorf("closing workbench server: %w", err)
	}
	return nil
}

func (server *workbenchServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", server.serveIndex)
	mux.HandleFunc("GET /api/meta", server.serveMeta)
	mux.HandleFunc("POST /api/export/preflight", server.receiveExportPreflight)
	mux.HandleFunc("POST /api/export", server.receiveExport)
	mux.HandleFunc("POST /api/native-preview", server.receiveNativePreview)
	if server.autoExport {
		mux.HandleFunc("POST /api/export/result", server.receiveAutoExportReport)
	}
	mux.Handle(
		"GET /assets/",
		http.StripPrefix("/assets/", noCacheFiles(http.Dir(server.workbenchRoot))),
	)
	return mux
}

func (server *workbenchServer) serveIndex(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(response, request)
		return
	}

	contents, err := os.ReadFile(filepath.Join(server.workbenchRoot, "index.html"))
	if err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
		return
	}
	version, err := server.currentVersion()
	if err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
		return
	}
	encodedVersion, err := json.Marshal(version)
	if err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
		return
	}
	encodedToken, err := json.Marshal(server.exportToken)
	if err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
		return
	}

	replacer := strings.NewReplacer(
		"{{UPDATE}}", version.Update,
		"{{BUILD}}", version.Build,
		"{{TRAY_BUILD}}", formatTrayBuild(server.startedAt),
		"{{STATIC_VERSION}}", url.QueryEscape(version.StaticVersion),
		"{{PAGE_VERSION_JSON}}", string(encodedVersion),
		"{{WORKBENCH_TOKEN_JSON}}", string(encodedToken),
		"{{AUTO_EXPORT_JSON}}", strconv.FormatBool(server.autoExport),
	)
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set(
		"Content-Security-Policy",
		"default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; "+
			"script-src 'self' 'unsafe-inline'; connect-src 'self'; object-src 'none'",
	)
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(response, replacer.Replace(string(contents)))
}

func (server *workbenchServer) serveMeta(response http.ResponseWriter, _ *http.Request) {
	version, err := server.currentVersion()
	if err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(response, version)
}

func (server *workbenchServer) receiveExportPreflight(
	response http.ResponseWriter,
	request *http.Request,
) {
	if err := server.authorizeExport(request); err != nil {
		http.Error(response, err.Error(), http.StatusForbidden)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxExportPreflightSize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()

	var requested exportPreflightRequest
	if err := decoder.Decode(&requested); err != nil {
		http.Error(response, "invalid export preflight: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(
			response,
			"invalid export preflight: request contains trailing data",
			http.StatusBadRequest,
		)
		return
	}

	result, err := server.preflightExport(requested.DefaultSurface, requested.Version)
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	if result.UpToDate {
		server.recordAutoExportSuccess(result)
	}
	writeJSON(response, result)
}

func (server *workbenchServer) preflightExport(
	defaultSurface string,
	clientVersion pageVersion,
) (exportResult, error) {
	result := exportResult{OK: true}
	if !isMainWorkbenchSurfaceID(defaultSurface) {
		return result, fmt.Errorf("invalid default surface %q", defaultSurface)
	}

	server.exportMu.Lock()
	defer server.exportMu.Unlock()

	version, err := server.currentVersion()
	if err != nil {
		return result, err
	}
	if err := validateWorkbenchPageVersion(clientVersion, version); err != nil {
		return result, err
	}
	manifest, err := readManifest(server.bundleRoot)
	if err != nil || manifest.Schema != bundleSchema ||
		manifest.Version.StaticVersion != version.StaticVersion {
		return result, nil
	}
	if err := validateCommittedWorkbenchBundle(server.bundleRoot, manifest); err != nil {
		return result, nil
	}
	renderer, err := server.currentEdgeRendererIdentity()
	if err != nil {
		return result, err
	}
	if !manifestUsesRenderer(manifest, renderer.Fingerprint) {
		return result, nil
	}
	manifestPath := filepath.Join(server.bundleRoot, "manifest.json")
	previous, hadPrevious, err := readOptionalFile(manifestPath)
	if err != nil {
		return result, err
	}

	manifest.DefaultSurface = defaultSurface
	manifest.Version = version
	commitVersion, err := server.currentVersion()
	if err != nil {
		return result, fmt.Errorf("checking workbench source before preflight commit: %w", err)
	}
	if commitVersion != version {
		return result, fmt.Errorf(
			"workbench source changed during export preflight (%q to %q); reload and retry",
			version.StaticVersion,
			commitVersion.StaticVersion,
		)
	}
	if err := commitValidatedWorkbenchManifest(
		server.bundleRoot,
		manifest,
		previous,
		hadPrevious,
	); err != nil {
		return result, err
	}

	result.UpToDate = true
	result.Surfaces = len(manifest.Surfaces)
	result.Files = len(workbenchSurfaceIDs) * len(workbenchVariantScales)
	result.Reused = result.Files
	return result, nil
}

func (server *workbenchServer) receiveExport(response http.ResponseWriter, request *http.Request) {
	if err := server.authorizeExport(request); err != nil {
		http.Error(response, err.Error(), http.StatusForbidden)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxExportSize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()

	var exported exportRequest
	if err := decoder.Decode(&exported); err != nil {
		http.Error(response, "invalid export: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(response, "invalid export: request contains trailing data", http.StatusBadRequest)
		return
	}
	result, err := server.writeExport(exported)
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	server.recordAutoExportSuccess(result)
	writeJSON(response, result)
}

func (server *workbenchServer) recordAutoExportSuccess(result exportResult) {
	if !server.autoExport || server.autoReports == nil {
		return
	}
	server.autoReportMu.Lock()
	defer server.autoReportMu.Unlock()
	if server.autoReported {
		return
	}
	copy := result
	server.autoSuccess = &copy
}

func (server *workbenchServer) receiveAutoExportReport(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !server.autoExport || server.autoReports == nil {
		http.NotFound(response, request)
		return
	}
	if err := server.authorizeExport(request); err != nil {
		http.Error(response, err.Error(), http.StatusForbidden)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxAutoExportReportSize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var reported autoExportReport
	if err := decoder.Decode(&reported); err != nil {
		http.Error(response, "invalid automatic export report: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(
			response,
			"invalid automatic export report: request contains trailing data",
			http.StatusBadRequest,
		)
		return
	}

	server.autoReportMu.Lock()
	defer server.autoReportMu.Unlock()
	if server.autoReported {
		http.Error(response, "automatic export already reported a terminal result", http.StatusConflict)
		return
	}
	if err := validateAutoExportReport(reported, server.autoSuccess); err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	server.autoReported = true
	server.autoReports <- reported
	writeJSON(response, struct {
		OK bool `json:"ok"`
	}{OK: true})
}

func validateAutoExportReport(reported autoExportReport, success *exportResult) error {
	if reported.OK {
		if strings.TrimSpace(reported.Error) != "" {
			return errors.New("successful automatic export report contains an error")
		}
		if success == nil {
			return errors.New("automatic export success was not verified by the server")
		}
		if reported.UpToDate != success.UpToDate ||
			reported.Files != success.Files ||
			reported.Rendered != success.Rendered ||
			reported.Reused != success.Reused ||
			reported.Atlases != success.Atlases ||
			reported.Fallback != success.Fallback {
			return errors.New("automatic export report does not match the verified server result")
		}
		return nil
	}
	if strings.TrimSpace(reported.Error) == "" {
		return errors.New("failed automatic export report is missing an error summary")
	}
	return nil
}

func (server *workbenchServer) receiveNativePreview(
	response http.ResponseWriter,
	request *http.Request,
) {
	if err := server.authorizeExport(request); err != nil {
		http.Error(response, err.Error(), http.StatusForbidden)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxNativePreviewSize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()

	var requested nativePreviewRequest
	if err := decoder.Decode(&requested); err != nil {
		http.Error(response, "invalid native preview: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(response, "invalid native preview: request contains trailing data", http.StatusBadRequest)
		return
	}
	contents, err := server.renderNativePreview(requested)
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "image/png")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = response.Write(contents)
}

func (server *workbenchServer) renderNativePreview(
	requested nativePreviewRequest,
) ([]byte, error) {
	if !isWorkbenchSurfaceID(requested.SurfaceID) {
		return nil, fmt.Errorf("invalid native preview surface %q", requested.SurfaceID)
	}
	if err := validateNativePreviewPresentation(requested); err != nil {
		return nil, err
	}

	server.exportMu.Lock()
	defer server.exportMu.Unlock()
	rendered, err := loadRenderedSurface(server.bundleRoot, requested.SurfaceID, 96)
	if err != nil {
		return nil, fmt.Errorf("loading native preview surface: %w", err)
	}
	composed, err := composeRenderedSurfaceWithPresentation(rendered, uiPresentation{
		Text:           requested.Text,
		Progress:       requested.Progress,
		Cells:          requested.Cells,
		Tone:           requested.Tone,
		StatisticsView: requested.StatisticsView,
		ChartValues:    requested.ChartValues,
	})
	if err != nil {
		return nil, fmt.Errorf("composing native preview: %w", err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, composed.Image); err != nil {
		return nil, fmt.Errorf("encoding native preview: %w", err)
	}
	return encoded.Bytes(), nil
}

func validateNativePreviewPresentation(requested nativePreviewRequest) error {
	if len(requested.Text) > 64 || len(requested.Progress) > 8 || len(requested.Cells) > 8 {
		return errors.New("native preview contains too many dynamic fields")
	}
	for binding, value := range requested.Text {
		if len(binding) > 64 || len(value) > 512 {
			return fmt.Errorf("invalid native preview text field %q", binding)
		}
	}
	for binding, value := range requested.Progress {
		if len(binding) > 64 || value < 0 || value > 100 {
			return fmt.Errorf("invalid native preview progress field %q", binding)
		}
	}
	for binding, values := range requested.Cells {
		if len(binding) > 64 || len(values) > 366 {
			return fmt.Errorf("invalid native preview cell field %q", binding)
		}
		for _, value := range values {
			if value < hiddenMonthCellLevel || value > 5 {
				return fmt.Errorf("invalid native preview cell value for %q", binding)
			}
		}
	}
	switch requested.Tone {
	case quotaToneGood, quotaToneWarn, quotaToneDanger, quotaToneOffline:
	default:
		return fmt.Errorf("invalid native preview tone %q", requested.Tone)
	}
	if !validStatisticsView(requested.StatisticsView) {
		return fmt.Errorf("invalid native preview statistics view %q", requested.StatisticsView)
	}
	if len(requested.ChartValues) > 366 {
		return errors.New("native preview contains too many chart values")
	}
	return nil
}

func (server *workbenchServer) writeExport(exported exportRequest) (exportResult, error) {
	return server.writeExportWithRasterizerMode(
		exported,
		rasterizeHTMLAttempt,
		exportRasterizerWorkers,
		true,
	)
}

func (server *workbenchServer) writeExportWithRasterizer(
	exported exportRequest,
	rasterizer func(exportFile) ([]byte, error),
) error {
	_, err := server.writeExportWithRasterizerWorkers(exported, rasterizer, 1)
	return err
}

func (server *workbenchServer) writeExportWithRasterizerResult(
	exported exportRequest,
	rasterizer func(exportFile) ([]byte, error),
) (exportResult, error) {
	return server.writeExportWithRasterizerWorkers(exported, rasterizer, 1)
}

func (server *workbenchServer) writeExportWithRasterizerWorkers(
	exported exportRequest,
	rasterizer func(exportFile) ([]byte, error),
	workerLimit int,
) (exportResult, error) {
	return server.writeExportWithRasterizerMode(exported, rasterizer, workerLimit, false)
}

func (server *workbenchServer) writeExportWithAtlasRasterizerResult(
	exported exportRequest,
	rasterizer func(exportFile) ([]byte, error),
	workerLimit int,
) (exportResult, error) {
	return server.writeExportWithRasterizerMode(exported, rasterizer, workerLimit, true)
}

func (server *workbenchServer) writeExportWithRasterizerMode(
	exported exportRequest,
	rasterizer func(exportFile) ([]byte, error),
	workerLimit int,
	useAtlases bool,
) (exportResult, error) {
	result := exportResult{OK: true}
	server.exportMu.Lock()
	defer server.exportMu.Unlock()

	if err := validateExport(exported); err != nil {
		return result, err
	}

	entryVersion, err := server.currentVersion()
	if err != nil {
		return result, err
	}
	if err := validateWorkbenchPageVersion(exported.Manifest.Version, entryVersion); err != nil {
		return result, err
	}
	entryRenderer, err := server.currentEdgeRendererIdentity()
	if err != nil {
		return result, err
	}
	if exported.Renderer != entryRenderer.Version {
		return result, fmt.Errorf(
			"workbench browser Edge %q does not match export renderer Edge %q; open this workbench in the same Microsoft Edge installation and retry",
			exported.Renderer,
			entryRenderer.Version,
		)
	}
	manifestPath := filepath.Join(server.bundleRoot, "manifest.json")
	previousManifest, hadPreviousManifest, err := readOptionalFile(manifestPath)
	if err != nil {
		return result, err
	}
	previousGenerations := referencedGenerationsFromContents(previousManifest)
	cachedAssets := server.loadCachedExportAssets()

	if err := os.MkdirAll(server.bundleRoot, 0o755); err != nil {
		return result, fmt.Errorf("creating bundle directory: %w", err)
	}
	stagingRoot, err := os.MkdirTemp(server.bundleRoot, ".export-staging-")
	if err != nil {
		return result, fmt.Errorf("creating export staging directory: %w", err)
	}
	defer os.RemoveAll(stagingRoot)
	renderedFiles := make([]renderedExportFile, len(exported.Files))
	stagingPaths := make([]string, len(exported.Files))
	renderJobs := make([]exportRenderJob, 0, len(exported.Files))
	sourceHashes := make(map[string]string, len(exported.Files))
	for fileIndex, file := range exported.Files {
		path, err := safeBundlePath(stagingRoot, file.Name)
		if err != nil {
			return result, err
		}
		stagingPaths[fileIndex] = path
		sourceHash := exportSourceHashForRenderer(file, entryRenderer.Fingerprint)
		sourceHashes[file.Name] = sourceHash
		contents, reused := reuseCachedExportAsset(
			cachedAssets[file.Name],
			file,
			sourceHash,
		)
		if reused {
			result.Reused++
			renderedFiles[fileIndex] = renderedExportFile{
				Name:     file.Name,
				Width:    file.Width,
				Height:   file.Height,
				Contents: contents,
			}
			continue
		}
		renderJobs = append(renderJobs, exportRenderJob{
			order:     len(renderJobs),
			fileIndex: fileIndex,
			file:      file,
		})
	}

	var rendered []exportRenderOutcome
	if useAtlases {
		var atlases int
		var fallback int
		rendered, atlases, fallback, err = rasterizeExportAtlases(
			renderJobs,
			exported.Manifest,
			rasterizer,
			workerLimit,
		)
		result.Atlases = atlases
		result.Fallback = fallback
	} else {
		rendered, err = rasterizeExportJobs(renderJobs, rasterizer, workerLimit)
	}
	if err != nil {
		return result, err
	}
	result.Rendered = len(rendered)
	for _, outcome := range rendered {
		file := outcome.job.file
		renderedFiles[outcome.job.fileIndex] = renderedExportFile{
			Name:     file.Name,
			Width:    file.Width,
			Height:   file.Height,
			Contents: outcome.contents,
		}
	}
	for fileIndex, path := range stagingPaths {
		if err := writeAtomic(path, renderedFiles[fileIndex].Contents); err != nil {
			return result, err
		}
	}
	if err := validateRenderedScaleCoverage(exported.Manifest, renderedFiles); err != nil {
		return result, err
	}

	generation := exportGeneration(renderedFiles)
	assetNames := make(map[string]string, len(exported.Files))
	for _, file := range exported.Files {
		assetNames[file.Name] = filepath.ToSlash(
			filepath.Join("assets", generation, file.Name),
		)
	}
	assetRoot := filepath.Join(server.bundleRoot, "assets", generation)
	createdGeneration := false
	if _, err := os.Stat(assetRoot); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(assetRoot), 0o755); err != nil {
			return result, fmt.Errorf("creating export asset directory: %w", err)
		}
		if err := os.Rename(stagingRoot, assetRoot); err != nil {
			return result, fmt.Errorf("committing export assets: %w", err)
		}
		createdGeneration = true
	} else if err != nil {
		return result, fmt.Errorf("checking export asset generation: %w", err)
	} else if err := validateExistingExportGeneration(assetRoot, renderedFiles); err != nil {
		return result, fmt.Errorf("validating existing export generation %q: %w", generation, err)
	}
	generationCommitted := false
	defer func() {
		if createdGeneration && !generationCommitted {
			if err := os.RemoveAll(assetRoot); err != nil {
				log.Printf("warning: rolling back uncommitted UI generation: %v", err)
			}
		}
	}()
	for surfaceIndex := range exported.Manifest.Surfaces {
		surface := &exported.Manifest.Surfaces[surfaceIndex]
		for variantIndex := range surface.Variants {
			variant := &surface.Variants[variantIndex]
			variant.SourceHash = sourceHashes[variant.File]
			variant.File = assetNames[variant.File]
		}
	}
	commitVersion, err := server.currentVersion()
	if err != nil {
		return result, fmt.Errorf("checking workbench source before manifest switch: %w", err)
	}
	if commitVersion != entryVersion {
		return result, fmt.Errorf(
			"workbench source changed during export (%q to %q); reload and retry",
			entryVersion.StaticVersion,
			commitVersion.StaticVersion,
		)
	}
	commitRenderer, err := server.currentEdgeRendererIdentity()
	if err != nil {
		return result, fmt.Errorf("checking Edge renderer before manifest switch: %w", err)
	}
	if commitRenderer != entryRenderer {
		return result, fmt.Errorf(
			"Edge renderer changed during export (%q to %q); reload and retry",
			entryRenderer.Version,
			commitRenderer.Version,
		)
	}

	if err := commitValidatedWorkbenchManifest(
		server.bundleRoot,
		exported.Manifest,
		previousManifest,
		hadPreviousManifest,
	); err != nil {
		return result, err
	}
	generationCommitted = true
	if err := cleanupCommittedExportArtifactsKeeping(
		server.bundleRoot,
		previousGenerations,
	); err != nil {
		log.Printf("warning: cleaning stale UI export artifacts: %v", err)
	}
	result.Surfaces = len(exported.Manifest.Surfaces)
	result.Files = len(exported.Files)
	return result, nil
}

func rasterizeExportJobs(
	jobs []exportRenderJob,
	rasterizer func(exportFile) ([]byte, error),
	workerLimit int,
) ([]exportRenderOutcome, error) {
	if len(jobs) == 0 {
		return []exportRenderOutcome{}, nil
	}

	workerCount := min(max(workerLimit, 1), len(jobs))
	jobChannel := make(chan exportRenderJob)
	outcomeChannel := make(chan exportRenderOutcome, workerCount)
	stop := make(chan struct{})
	var stopOnce sync.Once
	var workers sync.WaitGroup

	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for job := range jobChannel {
				contents, err := rasterizeExportFile(job.file, rasterizer)
				outcome := exportRenderOutcome{
					job:      job,
					contents: contents,
					err:      err,
				}
				if err != nil {
					outcomeChannel <- outcome
					stopOnce.Do(func() { close(stop) })
					return
				}
				select {
				case outcomeChannel <- outcome:
				case <-stop:
					return
				}
			}
		}()
	}

	go func() {
		defer close(jobChannel)
		for _, job := range jobs {
			select {
			case jobChannel <- job:
			case <-stop:
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(outcomeChannel)
	}()

	outcomes := make([]exportRenderOutcome, len(jobs))
	completed := make([]bool, len(jobs))
	for outcome := range outcomeChannel {
		outcomes[outcome.job.order] = outcome
		completed[outcome.job.order] = true
	}
	for order, outcome := range outcomes {
		if !completed[order] || outcome.err == nil {
			continue
		}
		return []exportRenderOutcome{}, fmt.Errorf(
			"rendering export asset %q: %w",
			outcome.job.file.Name,
			outcome.err,
		)
	}
	for order, done := range completed {
		if !done {
			return []exportRenderOutcome{}, fmt.Errorf(
				"rendering export asset %q was canceled without an error",
				jobs[order].file.Name,
			)
		}
	}
	return outcomes, nil
}

func rasterizeExportAtlases(
	jobs []exportRenderJob,
	manifest bundleManifest,
	rasterizer func(exportFile) ([]byte, error),
	workerLimit int,
) ([]exportRenderOutcome, int, int, error) {
	if len(jobs) == 0 {
		return []exportRenderOutcome{}, 0, 0, nil
	}

	scaleByFile := make(map[string]int, len(jobs))
	for _, surface := range manifest.Surfaces {
		for _, variant := range surface.Variants {
			scaleIndex, ok := workbenchScaleIndex(variant.Scale)
			if ok {
				scaleByFile[variant.File] = scaleIndex
			}
		}
	}
	jobsByScale := make([][]exportRenderJob, len(workbenchVariantScales))
	for _, job := range jobs {
		scaleIndex, ok := scaleByFile[job.file.Name]
		if !ok {
			return nil, 0, 0, fmt.Errorf(
				"finding atlas scale for export asset %q",
				job.file.Name,
			)
		}
		jobsByScale[scaleIndex] = append(jobsByScale[scaleIndex], job)
	}

	atlases := make([]exportAtlas, 0, len(workbenchVariantScales))
	fallbackGroups := make([][]exportRenderJob, 0, len(workbenchVariantScales))
	atlasFailures := make([]error, 0, len(workbenchVariantScales))
	for scaleIndex, scaleJobs := range jobsByScale {
		if len(scaleJobs) == 0 {
			continue
		}
		atlas, err := buildExportAtlas(scaleIndex, scaleJobs)
		if err != nil {
			fallbackGroups = append(fallbackGroups, scaleJobs)
			atlasFailures = append(atlasFailures, err)
			continue
		}
		atlases = append(atlases, atlas)
	}

	atlasOutcomes := rasterizeAtlasJobs(atlases, rasterizer, workerLimit)
	completed := make([]exportRenderOutcome, 0, len(jobs))
	successfulAtlases := 0
	for _, outcome := range atlasOutcomes {
		if outcome.err != nil {
			scaleJobs := make([]exportRenderJob, 0, len(outcome.atlas.placements))
			for _, placement := range outcome.atlas.placements {
				scaleJobs = append(scaleJobs, placement.job)
			}
			fallbackGroups = append(fallbackGroups, scaleJobs)
			atlasFailures = append(atlasFailures, fmt.Errorf(
				"scale %.4g atlas: %w",
				workbenchVariantScales[outcome.atlas.scaleIndex],
				outcome.err,
			))
			continue
		}
		successfulAtlases++
		completed = append(completed, outcome.contents...)
	}

	fallbackFiles := 0
	for index, fallbackJobs := range fallbackGroups {
		fallbackFiles += len(fallbackJobs)
		log.Printf(
			"warning: Edge atlas export fell back to %d individual assets: %v",
			len(fallbackJobs),
			atlasFailures[index],
		)
		fallbackOutcomes, err := rasterizeExportJobSubset(
			fallbackJobs,
			rasterizer,
			workerLimit,
		)
		if err != nil {
			return nil, successfulAtlases, fallbackFiles, fmt.Errorf(
				"atlas export failed (%v) and individual fallback failed: %w",
				atlasFailures[index],
				err,
			)
		}
		completed = append(completed, fallbackOutcomes...)
	}

	slices.SortFunc(completed, func(left, right exportRenderOutcome) int {
		return left.job.order - right.job.order
	})
	if len(completed) != len(jobs) {
		return nil, successfulAtlases, fallbackFiles, fmt.Errorf(
			"atlas export completed %d assets, want %d",
			len(completed),
			len(jobs),
		)
	}
	return completed, successfulAtlases, fallbackFiles, nil
}

func rasterizeExportJobSubset(
	jobs []exportRenderJob,
	rasterizer func(exportFile) ([]byte, error),
	workerLimit int,
) ([]exportRenderOutcome, error) {
	localJobs := make([]exportRenderJob, len(jobs))
	for index, job := range jobs {
		localJobs[index] = job
		localJobs[index].order = index
	}
	outcomes, err := rasterizeExportJobs(localJobs, rasterizer, workerLimit)
	if err != nil {
		return nil, err
	}
	for index := range outcomes {
		outcomes[index].job = jobs[index]
	}
	return outcomes, nil
}

func buildExportAtlas(scaleIndex int, jobs []exportRenderJob) (exportAtlas, error) {
	ordered := append([]exportRenderJob(nil), jobs...)
	slices.SortFunc(ordered, func(left, right exportRenderJob) int {
		return strings.Compare(left.file.Name, right.file.Name)
	})
	totalArea := 0
	maximumWidth := 0
	for _, job := range ordered {
		totalArea += job.file.Width * job.file.Height
		maximumWidth = max(maximumWidth, job.file.Width)
	}
	targetWidth := max(maximumWidth, int(math.Ceil(math.Sqrt(float64(totalArea)))))
	targetWidth = min(targetWidth+exportAtlasGutter*2, maxExportAtlasDimension)

	x := exportAtlasGutter
	y := exportAtlasGutter
	rowHeight := 0
	usedWidth := 0
	placements := make([]exportAtlasPlacement, 0, len(ordered))
	for _, job := range ordered {
		if x > exportAtlasGutter && x+job.file.Width+exportAtlasGutter > targetWidth {
			x = exportAtlasGutter
			y += rowHeight + exportAtlasGutter
			rowHeight = 0
		}
		rect := image.Rect(x, y, x+job.file.Width, y+job.file.Height)
		placements = append(placements, exportAtlasPlacement{job: job, rect: rect})
		x = rect.Max.X + exportAtlasGutter
		rowHeight = max(rowHeight, job.file.Height)
		usedWidth = max(usedWidth, rect.Max.X)
	}
	atlasWidth := usedWidth + exportAtlasGutter
	atlasHeight := y + rowHeight + exportAtlasGutter
	if atlasWidth <= 0 || atlasHeight <= 0 ||
		atlasWidth > maxExportAtlasDimension || atlasHeight > maxExportAtlasDimension ||
		atlasWidth*atlasHeight > maxExportPixels {
		return exportAtlas{}, fmt.Errorf(
			"scale %.4g atlas dimensions %dx%d exceed the exporter limit",
			workbenchVariantScales[scaleIndex],
			atlasWidth,
			atlasHeight,
		)
	}

	var snapshot strings.Builder
	_, _ = fmt.Fprintf(
		&snapshot,
		"<!doctype html><html><head><meta charset=\"utf-8\"><style>"+
			"html,body{width:%dpx;height:%dpx;min-width:0;min-height:0;margin:0;"+
			"padding:0;overflow:hidden;background:transparent}"+
			"iframe{position:absolute;display:block;border:0;margin:0;padding:0;"+
			"overflow:hidden;background:transparent}</style></head><body>",
		atlasWidth,
		atlasHeight,
	)
	for _, placement := range placements {
		_, _ = fmt.Fprintf(
			&snapshot,
			"<iframe title=\"%s\" sandbox=\"\" scrolling=\"no\" "+
				"style=\"left:%dpx;top:%dpx;width:%dpx;height:%dpx\" srcdoc=\"%s\"></iframe>",
			stdhtml.EscapeString(placement.job.file.Name),
			placement.rect.Min.X,
			placement.rect.Min.Y,
			placement.rect.Dx(),
			placement.rect.Dy(),
			stdhtml.EscapeString(placement.job.file.HTML),
		)
		if snapshot.Len() > maxExportAtlasHTML {
			return exportAtlas{}, fmt.Errorf(
				"scale %.4g atlas HTML exceeds %d bytes",
				workbenchVariantScales[scaleIndex],
				maxExportAtlasHTML,
			)
		}
	}
	_, _ = snapshot.WriteString("</body></html>")
	return exportAtlas{
		scaleIndex: scaleIndex,
		file: exportFile{
			Name:   fmt.Sprintf("atlas@%d.png", int(math.Round(workbenchVariantScales[scaleIndex]*100))),
			HTML:   snapshot.String(),
			Width:  atlasWidth,
			Height: atlasHeight,
		},
		placements: placements,
	}, nil
}

func rasterizeAtlasJobs(
	atlases []exportAtlas,
	rasterizer func(exportFile) ([]byte, error),
	workerLimit int,
) []exportAtlasOutcome {
	if len(atlases) == 0 {
		return []exportAtlasOutcome{}
	}
	workerCount := min(max(workerLimit, 1), len(atlases))
	jobs := make(chan exportAtlas)
	outcomes := make(chan exportAtlasOutcome, len(atlases))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for atlas := range jobs {
				contents, err := rasterizeAtlasFile(atlas.file, rasterizer)
				var cropped []exportRenderOutcome
				if err == nil {
					cropped, err = cropExportAtlas(atlas, contents)
				}
				outcomes <- exportAtlasOutcome{
					atlas:    atlas,
					contents: cropped,
					err:      err,
				}
			}
		}()
	}
	go func() {
		for _, atlas := range atlases {
			jobs <- atlas
		}
		close(jobs)
		workers.Wait()
		close(outcomes)
	}()

	ordered := make([]exportAtlasOutcome, 0, len(atlases))
	for outcome := range outcomes {
		ordered = append(ordered, outcome)
	}
	slices.SortFunc(ordered, func(left, right exportAtlasOutcome) int {
		return left.atlas.scaleIndex - right.atlas.scaleIndex
	})
	return ordered
}

func rasterizeAtlasFile(
	file exportFile,
	rasterizer func(exportFile) ([]byte, error),
) ([]byte, error) {
	failures := make([]error, 0, rasterizeAttempts)
	for attempt := 1; attempt <= rasterizeAttempts; attempt++ {
		contents, err := rasterizer(file)
		if err == nil {
			contents, err = normalizeAtlasPNG(contents, file.Width, file.Height)
		}
		if err == nil {
			return contents, nil
		}
		failures = append(failures, fmt.Errorf("attempt %d: %w", attempt, err))
	}
	return nil, fmt.Errorf(
		"atlas rasterizer failed after %d attempts: %w",
		rasterizeAttempts,
		errors.Join(failures...),
	)
}

func normalizeAtlasPNG(contents []byte, width int, height int) ([]byte, error) {
	rendered, format, err := image.Decode(bytes.NewReader(contents))
	if err != nil {
		return nil, fmt.Errorf("decoding Edge atlas screenshot: %w", err)
	}
	if format != "png" {
		return nil, fmt.Errorf("Edge atlas screenshot format is %q, want PNG", format)
	}
	bounds := rendered.Bounds()
	if bounds.Dx() != width || bounds.Dy() < height {
		return nil, fmt.Errorf(
			"Edge produced atlas %dx%d, expected width %d and height at least %d",
			bounds.Dx(),
			bounds.Dy(),
			width,
			height,
		)
	}
	normalized := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.Draw(normalized, normalized.Bounds(), rendered, bounds.Min, draw.Src)
	if metrics := measureRenderedAlpha(normalized); metrics.maxAlpha < 0x8000 {
		return nil, errors.New("Edge atlas screenshot contains no strongly visible pixel")
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, normalized); err != nil {
		return nil, fmt.Errorf("encoding validated Edge atlas screenshot: %w", err)
	}
	return encoded.Bytes(), nil
}

func cropExportAtlas(
	atlas exportAtlas,
	contents []byte,
) ([]exportRenderOutcome, error) {
	rendered, err := png.Decode(bytes.NewReader(contents))
	if err != nil {
		return nil, fmt.Errorf("decoding validated Edge atlas: %w", err)
	}
	outcomes := make([]exportRenderOutcome, 0, len(atlas.placements))
	for _, placement := range atlas.placements {
		if !placement.rect.In(rendered.Bounds()) {
			return nil, fmt.Errorf(
				"atlas crop for %q is outside %v",
				placement.job.file.Name,
				rendered.Bounds(),
			)
		}
		cropped := image.NewNRGBA(image.Rect(
			0,
			0,
			placement.rect.Dx(),
			placement.rect.Dy(),
		))
		draw.Draw(cropped, cropped.Bounds(), rendered, placement.rect.Min, draw.Src)
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, cropped); err != nil {
			return nil, fmt.Errorf("encoding atlas crop %q: %w", placement.job.file.Name, err)
		}
		normalized, err := normalizeRenderedPNG(
			encoded.Bytes(),
			placement.job.file.Width,
			placement.job.file.Height,
		)
		if err != nil {
			return nil, fmt.Errorf("validating atlas crop %q: %w", placement.job.file.Name, err)
		}
		outcomes = append(outcomes, exportRenderOutcome{
			job:      placement.job,
			contents: normalized,
		})
	}
	return outcomes, nil
}

func exportGeneration(files []renderedExportFile) string {
	ordered := append([]renderedExportFile(nil), files...)
	slices.SortFunc(ordered, func(left, right renderedExportFile) int {
		return strings.Compare(left.Name, right.Name)
	})
	hash := sha256.New()
	for _, file := range ordered {
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\x00", file.Name, file.Width, file.Height)
		_, _ = hash.Write(file.Contents)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

func exportSourceHash(file exportFile) string {
	return exportSourceHashForRenderer(file, "")
}

func exportSourceHashForRenderer(file exportFile, rendererFingerprint string) string {
	hash := sha256.New()
	if rendererFingerprint != "" {
		_, _ = fmt.Fprintf(hash, "renderer:%s\x00", rendererFingerprint)
	}
	_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\x00", file.Name, file.Width, file.Height)
	_, _ = hash.Write([]byte(file.HTML))
	digest := hex.EncodeToString(hash.Sum(nil))
	if rendererFingerprint == "" {
		return digest
	}
	return rendererFingerprint + digest[exportRendererStampLength:]
}

func manifestUsesRenderer(manifest bundleManifest, rendererFingerprint string) bool {
	if !isLowerHex(rendererFingerprint, exportRendererStampLength) {
		return false
	}
	for _, surface := range manifest.Surfaces {
		for _, variant := range surface.Variants {
			if !strings.HasPrefix(variant.SourceHash, rendererFingerprint) {
				return false
			}
		}
	}
	return len(manifest.Surfaces) > 0
}

func (server *workbenchServer) loadCachedExportAssets() map[string]cachedExportAsset {
	assets := map[string]cachedExportAsset{}
	manifest, err := readManifest(server.bundleRoot)
	if err != nil || manifest.Schema != bundleSchema {
		return assets
	}
	for _, surface := range manifest.Surfaces {
		for _, variant := range surface.Variants {
			if !isLowerHex(variant.SourceHash, 64) {
				continue
			}
			path, err := safeBundlePath(server.bundleRoot, variant.File)
			if err != nil {
				continue
			}
			name := filepath.Base(filepath.FromSlash(variant.File))
			assets[name] = cachedExportAsset{
				Path:       path,
				SourceHash: variant.SourceHash,
				Width:      variant.Width,
				Height:     variant.Height,
			}
		}
	}
	return assets
}

func reuseCachedExportAsset(
	asset cachedExportAsset,
	file exportFile,
	sourceHash string,
) ([]byte, bool) {
	isMatch := asset.SourceHash == sourceHash &&
		asset.Width == file.Width && asset.Height == file.Height
	if !isMatch {
		return nil, false
	}
	info, err := os.Stat(asset.Path)
	if err != nil || info.Size() <= 0 || info.Size() > maxBundleAssetBytes {
		return nil, false
	}
	contents, err := os.ReadFile(asset.Path)
	if err != nil {
		return nil, false
	}
	normalized, err := normalizeRenderedPNG(contents, file.Width, file.Height)
	if err != nil {
		return nil, false
	}
	return normalized, true
}

func readOptionalFile(path string) ([]byte, bool, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading existing %q: %w", path, err)
	}
	return contents, true, nil
}

func referencedGenerationsFromContents(contents []byte) map[string]struct{} {
	if len(contents) == 0 {
		return map[string]struct{}{}
	}
	var manifest bundleManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return map[string]struct{}{}
	}
	generations, err := referencedExportGenerations(manifest)
	if err != nil {
		return map[string]struct{}{}
	}
	return generations
}

func validateExistingExportGeneration(
	assetRoot string,
	files []renderedExportFile,
) error {
	expected := make(map[string]renderedExportFile, len(files))
	for _, file := range files {
		expected[file.Name] = file
	}
	entries, err := os.ReadDir(assetRoot)
	if err != nil {
		return fmt.Errorf("reading generation directory: %w", err)
	}
	if len(entries) != len(expected) {
		return fmt.Errorf(
			"generation contains %d entries, want %d",
			len(entries),
			len(expected),
		)
	}
	for _, entry := range entries {
		file, ok := expected[entry.Name()]
		if !ok || !entry.Type().IsRegular() {
			return fmt.Errorf("generation contains unexpected entry %q", entry.Name())
		}
		path, err := cleanupDirectChildPath(assetRoot, entry.Name())
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading generation asset %q: %w", entry.Name(), err)
		}
		normalized, err := normalizeRenderedPNG(contents, file.Width, file.Height)
		if err != nil {
			return fmt.Errorf("validating generation asset %q: %w", entry.Name(), err)
		}
		if !bytes.Equal(normalized, file.Contents) {
			return fmt.Errorf("generation asset %q does not match its content hash", entry.Name())
		}
	}
	return nil
}

func commitValidatedWorkbenchManifest(
	bundleRoot string,
	manifest bundleManifest,
	previous []byte,
	hadPrevious bool,
) error {
	return commitWorkbenchManifestWithOperations(
		bundleRoot,
		manifest,
		previous,
		hadPrevious,
		writeAtomic,
		func() error {
			committed, err := readManifest(bundleRoot)
			if err != nil {
				return err
			}
			return validateCommittedWorkbenchBundle(bundleRoot, committed)
		},
	)
}

func commitWorkbenchManifestWithOperations(
	bundleRoot string,
	manifest bundleManifest,
	previous []byte,
	hadPrevious bool,
	write func(string, []byte) error,
	validateAfterWrite func() error,
) error {
	if err := validateCommittedWorkbenchBundle(bundleRoot, manifest); err != nil {
		return fmt.Errorf("validating bundle before manifest switch: %w", err)
	}
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding bundle manifest: %w", err)
	}
	contents = append(contents, '\n')
	manifestPath := filepath.Join(bundleRoot, "manifest.json")
	if err := write(manifestPath, contents); err != nil {
		return err
	}
	err = validateAfterWrite()
	if err == nil {
		return nil
	}
	rollbackErr := rollbackWorkbenchManifest(manifestPath, previous, hadPrevious)
	return errors.Join(
		fmt.Errorf("validating bundle after manifest switch: %w", err),
		rollbackErr,
	)
}

func rollbackWorkbenchManifest(path string, previous []byte, hadPrevious bool) error {
	if hadPrevious {
		if err := writeAtomic(path, previous); err != nil {
			return fmt.Errorf("restoring previous bundle manifest: %w", err)
		}
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing failed bundle manifest: %w", err)
	}
	return nil
}

func validateCommittedWorkbenchBundle(bundleRoot string, manifest bundleManifest) error {
	if len(manifest.Surfaces) != len(workbenchSurfaceIDs) {
		return fmt.Errorf("committed bundle has %d surfaces", len(manifest.Surfaces))
	}
	if !isMainWorkbenchSurfaceID(manifest.DefaultSurface) {
		return fmt.Errorf("committed bundle has invalid default surface %q", manifest.DefaultSurface)
	}

	surfaces := make(map[string]bundleSurface, len(manifest.Surfaces))
	for _, surface := range manifest.Surfaces {
		if !isWorkbenchSurfaceID(surface.ID) {
			return fmt.Errorf("committed bundle contains unexpected surface %q", surface.ID)
		}
		if _, duplicate := surfaces[surface.ID]; duplicate {
			return fmt.Errorf("committed bundle repeats surface %q", surface.ID)
		}
		if err := validateWorkbenchDynamicContract(surface); err != nil {
			return err
		}
		if err := validateSurfaceActions(surface); err != nil {
			return err
		}
		if err := validateCommittedWorkbenchVariants(bundleRoot, manifest, surface); err != nil {
			return err
		}
		surfaces[surface.ID] = surface
	}
	for _, surfaceID := range workbenchSurfaceIDs {
		if _, found := surfaces[surfaceID]; !found {
			return fmt.Errorf("committed bundle is missing surface %q", surfaceID)
		}
	}
	if err := validateWorkbenchThemePairs(surfaces); err != nil {
		return err
	}
	generations, err := referencedExportGenerations(manifest)
	if err != nil {
		return err
	}
	if len(generations) != 1 {
		return fmt.Errorf("committed bundle references %d asset generations", len(generations))
	}
	return nil
}

func validateCommittedWorkbenchVariants(
	bundleRoot string,
	manifest bundleManifest,
	surface bundleSurface,
) error {
	if len(surface.Variants) != len(workbenchVariantScales) {
		return fmt.Errorf("committed surface %q has %d variants", surface.ID, len(surface.Variants))
	}
	seenScales := [len(workbenchVariantScales)]bool{}
	for _, variant := range surface.Variants {
		scaleIndex, ok := workbenchScaleIndex(variant.Scale)
		if !ok || seenScales[scaleIndex] {
			return fmt.Errorf("committed surface %q has invalid scale %.4g", surface.ID, variant.Scale)
		}
		seenScales[scaleIndex] = true

		rendered, err := loadRenderedSurfaceFromManifestAtScale(
			bundleRoot,
			manifest,
			surface.ID,
			variant.Scale,
		)
		if err != nil {
			return err
		}
		metrics := measureRenderedAlpha(rendered.Image)
		if metrics.maxAlpha < 0x8000 || metrics.strongCoverage() < minimumStrongAlphaCoverage {
			return fmt.Errorf("committed surface %q has invalid alpha coverage", surface.ID)
		}
	}
	return nil
}

func cleanupCommittedExportArtifacts(bundleRoot string) error {
	return cleanupCommittedExportArtifactsKeeping(bundleRoot, nil)
}

func cleanupCommittedExportArtifactsKeeping(
	bundleRoot string,
	additionalGenerations map[string]struct{},
) error {
	manifest, err := readManifest(bundleRoot)
	if err != nil {
		return fmt.Errorf("reading committed UI manifest before cleanup: %w", err)
	}
	protectedGenerations, err := referencedExportGenerations(manifest)
	if err != nil {
		return err
	}
	if len(additionalGenerations) > 0 {
		previous, found, findErr := newestExportGeneration(
			bundleRoot,
			additionalGenerations,
			protectedGenerations,
		)
		if findErr != nil {
			return findErr
		}
		if !found {
			previous, found, findErr = newestExportGeneration(
				bundleRoot,
				nil,
				protectedGenerations,
			)
			if findErr != nil {
				return findErr
			}
		}
		if found {
			protectedGenerations[previous] = struct{}{}
		}
	}

	failures := []error{}
	if err := cleanupStaleAssetGenerations(bundleRoot, protectedGenerations); err != nil {
		failures = append(failures, err)
	}
	if err := cleanupTopLevelExportArtifacts(bundleRoot); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func newestExportGeneration(
	bundleRoot string,
	candidates map[string]struct{},
	excluded map[string]struct{},
) (string, bool, error) {
	assetsRoot, err := cleanupDirectChildPath(bundleRoot, "assets")
	if err != nil {
		return "", false, err
	}
	entries, err := os.ReadDir(assetsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading UI generations for retention: %w", err)
	}
	var selected string
	var selectedTime time.Time
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type() != os.ModeDir || !isExportGenerationName(name) {
			continue
		}
		if _, skip := excluded[name]; skip {
			continue
		}
		if candidates != nil {
			if _, allowed := candidates[name]; !allowed {
				continue
			}
		}
		info, err := entry.Info()
		if err != nil {
			return "", false, fmt.Errorf("reading UI generation %q metadata: %w", name, err)
		}
		modified := info.ModTime()
		if selected == "" || modified.After(selectedTime) ||
			(modified.Equal(selectedTime) && name > selected) {
			selected = name
			selectedTime = modified
		}
	}
	return selected, selected != "", nil
}

func referencedExportGenerations(manifest bundleManifest) (map[string]struct{}, error) {
	generations := map[string]struct{}{}
	for _, surface := range manifest.Surfaces {
		for _, variant := range surface.Variants {
			cleanPath := filepath.ToSlash(
				filepath.Clean(filepath.FromSlash(variant.File)),
			)
			parts := strings.Split(cleanPath, "/")
			hasGenerationPath := len(parts) >= 3 && parts[0] == "assets"
			isCanonicalPath := cleanPath == variant.File
			isGeneratedPath := hasGenerationPath && isExportGenerationName(parts[1])
			if !isCanonicalPath || !isGeneratedPath {
				return nil, fmt.Errorf(
					"committed surface %q has unsafe asset path %q",
					surface.ID,
					variant.File,
				)
			}
			generations[parts[1]] = struct{}{}
		}
	}
	if len(generations) == 0 {
		return nil, errors.New("committed UI manifest references no asset generation")
	}
	return generations, nil
}

func cleanupStaleAssetGenerations(
	bundleRoot string,
	protectedGenerations map[string]struct{},
) error {
	assetsRoot, err := cleanupDirectChildPath(bundleRoot, "assets")
	if err != nil {
		return err
	}
	info, err := os.Lstat(assetsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking UI assets directory before cleanup: %w", err)
	}
	if info.Mode().Type() != os.ModeDir {
		return fmt.Errorf("UI assets path %q is not a real directory", assetsRoot)
	}

	entries, err := os.ReadDir(assetsRoot)
	if err != nil {
		return fmt.Errorf("reading UI asset generations for cleanup: %w", err)
	}
	failures := []error{}
	for _, entry := range entries {
		name := entry.Name()
		_, protected := protectedGenerations[name]
		isGenerationDirectory := entry.Type() == os.ModeDir &&
			isExportGenerationName(name)
		if protected || !isGenerationDirectory {
			continue
		}
		path, pathErr := cleanupDirectChildPath(assetsRoot, name)
		if pathErr != nil {
			failures = append(failures, pathErr)
			continue
		}
		if removeErr := os.RemoveAll(path); removeErr != nil {
			failures = append(
				failures,
				fmt.Errorf("removing stale UI generation %q: %w", name, removeErr),
			)
		}
	}
	return errors.Join(failures...)
}

func cleanupTopLevelExportArtifacts(bundleRoot string) error {
	entries, err := os.ReadDir(bundleRoot)
	if err != nil {
		return fmt.Errorf("reading UI bundle root for cleanup: %w", err)
	}
	failures := []error{}
	for _, entry := range entries {
		name := entry.Name()
		isLegacyFile := entry.Type().IsRegular() && isLegacyExportPNGName(name)
		isStagingDirectory := entry.Type() == os.ModeDir &&
			isExportStagingDirectoryName(name)
		if !isLegacyFile && !isStagingDirectory {
			continue
		}
		path, pathErr := cleanupDirectChildPath(bundleRoot, name)
		if pathErr != nil {
			failures = append(failures, pathErr)
			continue
		}
		if isLegacyFile {
			if removeErr := os.Remove(path); removeErr != nil {
				failures = append(
					failures,
					fmt.Errorf("removing legacy UI asset %q: %w", name, removeErr),
				)
			}
			continue
		}
		if removeErr := os.RemoveAll(path); removeErr != nil {
			failures = append(
				failures,
				fmt.Errorf("removing stale export staging directory %q: %w", name, removeErr),
			)
		}
	}
	return errors.Join(failures...)
}

func cleanupDirectChildPath(root string, name string) (string, error) {
	hasSeparator := strings.ContainsAny(name, `/\`)
	isDirectName := name != "" && name != "." && name != ".." &&
		!filepath.IsAbs(name) &&
		!hasSeparator && filepath.Base(name) == name
	if !isDirectName {
		return "", fmt.Errorf("invalid cleanup child %q", name)
	}
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving cleanup root %q: %w", root, err)
	}
	targetPath, err := filepath.Abs(filepath.Join(rootPath, name))
	if err != nil {
		return "", fmt.Errorf("resolving cleanup target %q: %w", name, err)
	}
	relative, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return "", fmt.Errorf("clamping cleanup target %q: %w", name, err)
	}
	if relative != name || filepath.IsAbs(relative) {
		return "", fmt.Errorf("cleanup target %q escapes %q", name, rootPath)
	}
	return targetPath, nil
}

func isExportGenerationName(name string) bool {
	if len(name) != 16 {
		return false
	}
	for _, character := range name {
		isDigit := character >= '0' && character <= '9'
		isLowerHex := character >= 'a' && character <= 'f'
		if !isDigit && !isLowerHex {
			return false
		}
	}
	return true
}

func isLegacyExportPNGName(name string) bool {
	for _, surfaceID := range workbenchSurfaceIDs {
		for _, scale := range workbenchVariantScales {
			percentage := int(math.Round(scale * 100))
			if name == fmt.Sprintf("%s@%d.png", surfaceID, percentage) {
				return true
			}
		}
	}
	return false
}

func isExportStagingDirectoryName(name string) bool {
	const prefix = ".export-staging-"
	return strings.HasPrefix(name, prefix) && allASCIIDigits(name[len(prefix):])
}

func allASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validateExport(exported exportRequest) error {
	if exported.Manifest.Schema != bundleSchema {
		return fmt.Errorf("unsupported export schema %d", exported.Manifest.Schema)
	}
	if exported.Manifest.Project != projectID {
		return fmt.Errorf("unexpected export project %q", exported.Manifest.Project)
	}
	if len(exported.Files) == 0 {
		return errors.New("export contains no surfaces or files")
	}
	if len(exported.Manifest.Surfaces) != len(workbenchSurfaceIDs) {
		return fmt.Errorf(
			"export must contain exactly %d workbench surfaces",
			len(workbenchSurfaceIDs),
		)
	}
	expectedFiles := len(workbenchSurfaceIDs) * len(workbenchVariantScales)
	if len(exported.Files) != expectedFiles {
		return fmt.Errorf("export must contain exactly %d files", expectedFiles)
	}
	if len(exported.Files) > maxExportFiles {
		return fmt.Errorf("export exceeds the file limit: %d", len(exported.Files))
	}

	files := make(map[string]exportFile, len(exported.Files))
	totalPixels := 0
	for _, file := range exported.Files {
		cleanName := filepath.ToSlash(filepath.Clean(filepath.FromSlash(file.Name)))
		if cleanName != file.Name || strings.Contains(cleanName, "..") {
			return fmt.Errorf("invalid export asset path %q", file.Name)
		}
		if !strings.HasSuffix(strings.ToLower(file.Name), ".png") {
			return fmt.Errorf("export asset %q is not a PNG", file.Name)
		}
		if _, duplicate := files[file.Name]; duplicate {
			return fmt.Errorf("duplicate export asset %q", file.Name)
		}
		if file.Width <= 0 || file.Height <= 0 ||
			file.Width > maxSurfaceDimension || file.Height > maxSurfaceDimension {
			return fmt.Errorf("invalid output size %dx%d", file.Width, file.Height)
		}
		totalPixels += file.Width * file.Height
		if totalPixels > maxExportPixels {
			return fmt.Errorf("export exceeds %d pixels", maxExportPixels)
		}
		if err := validateSnapshotHTML(file.HTML); err != nil {
			return fmt.Errorf("invalid HTML snapshot %q: %w", file.Name, err)
		}
		files[file.Name] = file
	}

	foundDefault := false
	surfaceIDs := make(map[string]struct{}, len(exported.Manifest.Surfaces))
	surfaces := make(map[string]bundleSurface, len(exported.Manifest.Surfaces))
	referencedFiles := make(map[string]string, len(exported.Files))
	for _, surface := range exported.Manifest.Surfaces {
		if !isWorkbenchSurfaceID(surface.ID) {
			return fmt.Errorf("export contains unexpected surface %q", surface.ID)
		}
		if _, duplicate := surfaceIDs[surface.ID]; duplicate {
			return fmt.Errorf("export contains duplicate surface %q", surface.ID)
		}
		surfaceIDs[surface.ID] = struct{}{}
		surfaces[surface.ID] = surface
		if surface.ID == exported.Manifest.DefaultSurface {
			foundDefault = true
		}
		if surface.LogicalWidth <= 0 || surface.LogicalHeight <= 0 ||
			surface.LogicalWidth > maxSurfaceDimension ||
			surface.LogicalHeight > maxSurfaceDimension {
			return fmt.Errorf("surface %q has invalid logical size", surface.ID)
		}
		if len(surface.HitRegions) > 128 {
			return fmt.Errorf("surface %q has too many hit regions", surface.ID)
		}
		if err := validateDynamicSlots(surface); err != nil {
			return err
		}
		if err := validateWorkbenchDynamicContract(surface); err != nil {
			return err
		}
		if err := validateSurfaceActions(surface); err != nil {
			return err
		}
		if err := validateWorkbenchVariants(surface, files, referencedFiles); err != nil {
			return err
		}
	}
	if !foundDefault {
		return fmt.Errorf("default surface %q was not exported", exported.Manifest.DefaultSurface)
	}
	if !isMainWorkbenchSurfaceID(exported.Manifest.DefaultSurface) {
		return fmt.Errorf(
			"default surface %q is not a main workbench surface",
			exported.Manifest.DefaultSurface,
		)
	}
	if len(referencedFiles) != len(files) {
		return errors.New("export contains an unreferenced asset")
	}
	if err := validateWorkbenchThemePairs(surfaces); err != nil {
		return err
	}
	return nil
}

func validateWorkbenchVariants(
	surface bundleSurface,
	files map[string]exportFile,
	referencedFiles map[string]string,
) error {
	if len(surface.Variants) != len(workbenchVariantScales) {
		return fmt.Errorf(
			"surface %q must contain exactly %d scale variants",
			surface.ID,
			len(workbenchVariantScales),
		)
	}

	seenScales := [len(workbenchVariantScales)]bool{}
	for _, variant := range surface.Variants {
		scaleIndex, ok := workbenchScaleIndex(variant.Scale)
		if !ok {
			return fmt.Errorf("surface %q has unexpected scale %.4g", surface.ID, variant.Scale)
		}
		if seenScales[scaleIndex] {
			return fmt.Errorf("surface %q repeats scale %.4g", surface.ID, variant.Scale)
		}
		seenScales[scaleIndex] = true

		expectedWidth := int(math.Round(float64(surface.LogicalWidth) * variant.Scale))
		expectedHeight := int(math.Round(float64(surface.LogicalHeight) * variant.Scale))
		if variant.Width != expectedWidth || variant.Height != expectedHeight {
			return fmt.Errorf(
				"surface %q scale %.4g must be %dx%d",
				surface.ID,
				variant.Scale,
				expectedWidth,
				expectedHeight,
			)
		}

		file, ok := files[variant.File]
		if !ok {
			return fmt.Errorf("surface %q references missing asset %q", surface.ID, variant.File)
		}
		if variant.Width != file.Width || variant.Height != file.Height {
			return fmt.Errorf("surface %q asset %q dimensions do not match", surface.ID, variant.File)
		}
		if owner, duplicate := referencedFiles[variant.File]; duplicate {
			return fmt.Errorf(
				"surface %q reuses asset %q already referenced by %q",
				surface.ID,
				variant.File,
				owner,
			)
		}
		referencedFiles[variant.File] = surface.ID
	}
	return nil
}

func workbenchScaleIndex(scale float64) (int, bool) {
	if !finite(scale) {
		return 0, false
	}
	for index, expected := range workbenchVariantScales {
		if math.Abs(scale-expected) < 0.000001 {
			return index, true
		}
	}
	return 0, false
}

func validateWorkbenchDynamicContract(surface bundleSurface) error {
	baseID := strings.TrimSuffix(surface.ID, "-light")
	textBindings := make([]string, 0, len(surface.Dynamic.Text))
	for _, slot := range surface.Dynamic.Text {
		textBindings = append(textBindings, slot.Bind)
	}
	progressBindings := make([]string, 0, len(surface.Dynamic.Progress))
	for _, slot := range surface.Dynamic.Progress {
		progressBindings = append(progressBindings, slot.Bind)
	}

	switch {
	case strings.HasPrefix(baseID, "main-"):
		if err := validateExactBindings(
			surface.ID,
			"text",
			textBindings,
			mainWorkbenchTextBindings[:],
		); err != nil {
			return err
		}
		if err := validateExactBindings(
			surface.ID,
			"progress",
			progressBindings,
			[]string{"quota.progress"},
		); err != nil {
			return err
		}
		if len(surface.Dynamic.Cells) != 0 {
			return fmt.Errorf("surface %q must not contain cell slots", surface.ID)
		}
	case baseID == "statistics":
		if err := validateExactBindings(
			surface.ID,
			"text",
			textBindings,
			statisticsWorkbenchTextBindings[:],
		); err != nil {
			return err
		}
		if len(surface.Dynamic.Progress) != 0 {
			return fmt.Errorf("surface %q must not contain progress slots", surface.ID)
		}
		if len(surface.Dynamic.Cells) != 1 ||
			surface.Dynamic.Cells[0].Bind != "statistics.monthCells" ||
			len(surface.Dynamic.Cells[0].Rects) != 42 {
			return fmt.Errorf(
				"surface %q must contain one 42-cell statistics.monthCells slot",
				surface.ID,
			)
		}
	case strings.HasPrefix(baseID, "usage-toast"):
		if err := validateExactBindings(
			surface.ID,
			"text",
			textBindings,
			toastWorkbenchTextBindings[:],
		); err != nil {
			return err
		}
		if len(surface.Dynamic.Progress) != 0 || len(surface.Dynamic.Cells) != 0 {
			return fmt.Errorf("surface %q contains unexpected non-text slots", surface.ID)
		}
	default:
		return fmt.Errorf("surface %q has no dynamic contract", surface.ID)
	}
	return nil
}

func validateExactBindings(
	surfaceID string,
	kind string,
	actual []string,
	expected []string,
) error {
	if len(actual) != len(expected) {
		return fmt.Errorf(
			"surface %q must contain exactly %d %s bindings",
			surfaceID,
			len(expected),
			kind,
		)
	}
	found := make(map[string]struct{}, len(actual))
	for _, binding := range actual {
		found[binding] = struct{}{}
	}
	for _, binding := range expected {
		if _, ok := found[binding]; !ok {
			return fmt.Errorf("surface %q is missing %s binding %q", surfaceID, kind, binding)
		}
	}
	return nil
}

func validateWorkbenchThemePairs(surfaces map[string]bundleSurface) error {
	for _, darkID := range workbenchSurfaceIDs {
		if strings.HasSuffix(darkID, "-light") {
			continue
		}
		dark := surfaces[darkID]
		lightID := darkID + "-light"
		light := surfaces[lightID]
		if err := validateThemePair(dark, light); err != nil {
			return fmt.Errorf("theme pair %q/%q: %w", darkID, lightID, err)
		}
	}
	return nil
}

func validateThemePair(dark bundleSurface, light bundleSurface) error {
	if dark.LogicalWidth != light.LogicalWidth || dark.LogicalHeight != light.LogicalHeight {
		return errors.New("logical dimensions differ")
	}

	lightText := make(map[string]textSlot, len(light.Dynamic.Text))
	for _, slot := range light.Dynamic.Text {
		lightText[slot.Bind] = slot
	}
	for _, darkSlot := range dark.Dynamic.Text {
		lightSlot, ok := lightText[darkSlot.Bind]
		if !ok || !sameTextSlotShape(darkSlot, lightSlot) {
			return fmt.Errorf("text slot %q differs", darkSlot.Bind)
		}
	}

	lightProgress := make(map[string]progressSlot, len(light.Dynamic.Progress))
	for _, slot := range light.Dynamic.Progress {
		lightProgress[slot.Bind] = slot
	}
	for _, darkSlot := range dark.Dynamic.Progress {
		lightSlot, ok := lightProgress[darkSlot.Bind]
		if !ok || !sameSlotRect(darkSlot.Rect, lightSlot.Rect) ||
			!sameToneColorShape(darkSlot.ToneColors, lightSlot.ToneColors) {
			return fmt.Errorf("progress slot %q differs", darkSlot.Bind)
		}
	}

	for index, darkSlot := range dark.Dynamic.Cells {
		if index >= len(light.Dynamic.Cells) {
			return fmt.Errorf("cell slot %q differs", darkSlot.Bind)
		}
		lightSlot := light.Dynamic.Cells[index]
		if darkSlot.Bind != lightSlot.Bind || len(darkSlot.Rects) != len(lightSlot.Rects) {
			return fmt.Errorf("cell slot %q differs", darkSlot.Bind)
		}
		for rectIndex, darkRect := range darkSlot.Rects {
			if !sameSlotRect(darkRect, lightSlot.Rects[rectIndex]) {
				return fmt.Errorf("cell slot %q geometry differs", darkSlot.Bind)
			}
		}
	}
	return nil
}

func sameTextSlotShape(left textSlot, right textSlot) bool {
	return sameSlotRect(left.Rect, right.Rect) &&
		left.FontFamily == right.FontFamily &&
		slices.Equal(left.FontFamilies, right.FontFamilies) &&
		math.Abs(left.FontSize-right.FontSize) < 0.000001 &&
		left.FontWeight == right.FontWeight &&
		left.Align == right.Align &&
		sameToneColorShape(left.ToneColors, right.ToneColors)
}

func sameSlotRect(left slotRect, right slotRect) bool {
	return math.Abs(left.X-right.X) < 0.01 &&
		math.Abs(left.Y-right.Y) < 0.01 &&
		math.Abs(left.Width-right.Width) < 0.01 &&
		math.Abs(left.Height-right.Height) < 0.01
}

func sameToneColorShape(left toneColors, right toneColors) bool {
	return (left.Good != "") == (right.Good != "") &&
		(left.Warn != "") == (right.Warn != "") &&
		(left.Danger != "") == (right.Danger != "") &&
		(left.Offline != "") == (right.Offline != "")
}

func isWorkbenchSurfaceID(surfaceID string) bool {
	for _, expected := range workbenchSurfaceIDs {
		if surfaceID == expected {
			return true
		}
	}
	return false
}

func isMainWorkbenchSurfaceID(surfaceID string) bool {
	return surfaceID == "main-horizontal" ||
		surfaceID == "main-vertical" ||
		surfaceID == "main-horizontal-light" ||
		surfaceID == "main-vertical-light"
}

func validateSurfaceActions(surface bundleSurface) error {
	actions := make(map[string]struct{}, len(surface.HitRegions))
	for _, region := range surface.HitRegions {
		if region.Action == "" || region.Width <= 0 || region.Height <= 0 {
			return fmt.Errorf("surface %q contains an invalid hit region", surface.ID)
		}
		isOutsideSurface := region.X < 0 || region.Y < 0 ||
			region.X+region.Width > float64(surface.LogicalWidth) ||
			region.Y+region.Height > float64(surface.LogicalHeight)
		if isOutsideSurface {
			return fmt.Errorf("surface %q contains an out-of-bounds hit region", surface.ID)
		}
		actions[region.Action] = struct{}{}
	}

	requiredActions := []string{"hide"}
	baseID := strings.TrimSuffix(surface.ID, "-light")
	switch {
	case isMainWorkbenchSurfaceID(surface.ID):
		requiredActions = []string{
			"toggle-theme",
			"toggle-statistics",
			"toggle-toast",
			"toggle-layout",
			"hide",
		}
	case baseID == "statistics":
		requiredActions = []string{
			"statistics-view-month",
			"statistics-view-week",
			"statistics-view-cumulative",
			"statistics-previous-month",
			"statistics-next-month",
		}
	}
	for _, required := range requiredActions {
		if _, found := actions[required]; !found {
			return fmt.Errorf("surface %q is missing action %q", surface.ID, required)
		}
	}
	return nil
}

func (server *workbenchServer) currentVersion() (pageVersion, error) {
	fingerprint, err := fingerprintTree(server.workbenchRoot)
	if err != nil {
		return pageVersion{}, err
	}
	return newPageVersion(server.startedAt, fingerprint), nil
}

func validateWorkbenchPageVersion(client pageVersion, current pageVersion) error {
	if client.StaticVersion != current.StaticVersion {
		return fmt.Errorf(
			"workbench page fingerprint %q does not match current source %q; reload and retry",
			client.StaticVersion,
			current.StaticVersion,
		)
	}
	if client.Update != current.Update || client.Build != current.Build {
		return fmt.Errorf(
			"workbench page build %q/%q does not match server build %q/%q; reload and retry",
			client.Update,
			client.Build,
			current.Update,
			current.Build,
		)
	}
	return nil
}

func rasterizeHTML(file exportFile) ([]byte, error) {
	return rasterizeExportFile(file, rasterizeHTMLAttempt)
}

func rasterizeExportFile(
	file exportFile,
	rasterizer func(exportFile) ([]byte, error),
) ([]byte, error) {
	failures := make([]error, 0, rasterizeAttempts)
	for attempt := 1; attempt <= rasterizeAttempts; attempt++ {
		contents, err := rasterizer(file)
		if err == nil {
			contents, err = normalizeRenderedPNG(contents, file.Width, file.Height)
		}
		if err == nil {
			return contents, nil
		}
		failures = append(failures, fmt.Errorf("attempt %d: %w", attempt, err))
	}
	return nil, fmt.Errorf(
		"rasterizer failed after %d attempts: %w",
		rasterizeAttempts,
		errors.Join(failures...),
	)
}

func normalizeRenderedPNG(contents []byte, width int, height int) ([]byte, error) {
	rendered, format, err := image.Decode(bytes.NewReader(contents))
	if err != nil {
		return nil, fmt.Errorf("decoding Edge screenshot: %w", err)
	}
	if format != "png" {
		return nil, fmt.Errorf("Edge screenshot format is %q, want PNG", format)
	}
	bounds := rendered.Bounds()
	if bounds.Dx() != width || bounds.Dy() < height {
		return nil, fmt.Errorf(
			"Edge produced %dx%d, expected width %d and height at least %d",
			bounds.Dx(),
			bounds.Dy(),
			width,
			height,
		)
	}

	normalized := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.Draw(normalized, normalized.Bounds(), rendered, bounds.Min, draw.Src)
	metrics := measureRenderedAlpha(normalized)
	if metrics.maxAlpha < 0x8000 {
		return nil, fmt.Errorf(
			"Edge screenshot maximum alpha is %d, expected at least 128",
			metrics.maxAlpha>>8,
		)
	}
	coverage := metrics.strongCoverage()
	if coverage < minimumStrongAlphaCoverage {
		return nil, fmt.Errorf(
			"Edge screenshot strong-alpha coverage is %.2f%%, expected at least %.2f%%",
			coverage*100,
			minimumStrongAlphaCoverage*100,
		)
	}

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, normalized); err != nil {
		return nil, fmt.Errorf("encoding validated Edge screenshot: %w", err)
	}
	return encoded.Bytes(), nil
}

func measureRenderedAlpha(rendered image.Image) renderedAlphaMetrics {
	metrics := renderedAlphaMetrics{
		totalPixels: rendered.Bounds().Dx() * rendered.Bounds().Dy(),
	}
	for y := rendered.Bounds().Min.Y; y < rendered.Bounds().Max.Y; y++ {
		for x := rendered.Bounds().Min.X; x < rendered.Bounds().Max.X; x++ {
			_, _, _, alpha := rendered.At(x, y).RGBA()
			if alpha >= 0x8000 {
				metrics.strongPixels++
			}
			if alpha > metrics.maxAlpha {
				metrics.maxAlpha = alpha
			}
		}
	}
	return metrics
}

func (metrics renderedAlphaMetrics) strongCoverage() float64 {
	if metrics.totalPixels == 0 {
		return 0
	}
	return float64(metrics.strongPixels) / float64(metrics.totalPixels)
}

func validateRenderedScaleCoverage(
	manifest bundleManifest,
	files []renderedExportFile,
) error {
	coverageByFile := make(map[string]float64, len(files))
	for _, file := range files {
		rendered, format, err := image.Decode(bytes.NewReader(file.Contents))
		if err != nil {
			return fmt.Errorf("checking rendered coverage for %q: %w", file.Name, err)
		}
		if format != "png" {
			return fmt.Errorf("rendered asset %q has format %q, want PNG", file.Name, format)
		}
		coverageByFile[file.Name] = measureRenderedAlpha(rendered).strongCoverage()
	}

	for _, surface := range manifest.Surfaces {
		minimumCoverage := 1.0
		maximumCoverage := 0.0
		for _, variant := range surface.Variants {
			coverage, ok := coverageByFile[variant.File]
			if !ok {
				return fmt.Errorf(
					"surface %q coverage references missing asset %q",
					surface.ID,
					variant.File,
				)
			}
			minimumCoverage = min(minimumCoverage, coverage)
			maximumCoverage = max(maximumCoverage, coverage)
		}
		if maximumCoverage-minimumCoverage > maximumScaleCoverageDelta {
			return fmt.Errorf(
				"surface %q strong-alpha coverage varies across scales: %.2f%% to %.2f%%",
				surface.ID,
				minimumCoverage*100,
				maximumCoverage*100,
			)
		}
	}
	return nil
}

func cleanupTemporaryExportRoot(path string) {
	ownedPath, err := ownedTemporaryExportPath(path)
	if err != nil {
		log.Printf("warning: refusing to clean temporary Edge export: %v", err)
		return
	}
	err = removeTreeWithRetry(
		ownedPath,
		temporaryCleanupAttempts,
		temporaryCleanupRetryDelay,
		os.RemoveAll,
	)
	if err != nil {
		log.Printf("warning: cleaning temporary Edge export: %v", err)
	}
}

func ownedTemporaryExportPath(path string) (string, error) {
	const prefix = "codexfloatingbar-export-"
	name := filepath.Base(path)
	isGeneratedName := strings.HasPrefix(name, prefix) &&
		allASCIIDigits(name[len(prefix):])
	if !isGeneratedName {
		return "", fmt.Errorf("temporary export path %q has an unexpected name", path)
	}
	ownedPath, err := cleanupDirectChildPath(os.TempDir(), name)
	if err != nil {
		return "", err
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving temporary export path %q: %w", path, err)
	}
	samePath := filepath.Clean(absolutePath) == ownedPath
	if runtime.GOOS == "windows" {
		samePath = strings.EqualFold(filepath.Clean(absolutePath), ownedPath)
	}
	if !samePath {
		return "", fmt.Errorf("temporary export path %q is outside %q", path, os.TempDir())
	}
	return ownedPath, nil
}

func removeTreeWithRetry(
	path string,
	attempts int,
	delay time.Duration,
	remove func(string) error,
) error {
	if attempts <= 0 {
		return errors.New("temporary cleanup requires at least one attempt")
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		err := remove(path)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt < attempts && delay > 0 {
			time.Sleep(delay * time.Duration(attempt))
		}
	}
	return fmt.Errorf(
		"removing %q after %d attempts: %w",
		path,
		attempts,
		lastErr,
	)
}

func rasterizeHTMLAttempt(file exportFile) ([]byte, error) {
	edgePath, err := findEdge()
	if err != nil {
		return nil, err
	}
	temporaryRoot, err := os.MkdirTemp("", "codexfloatingbar-export-")
	if err != nil {
		return nil, fmt.Errorf("creating exporter directory: %w", err)
	}
	defer cleanupTemporaryExportRoot(temporaryRoot)

	htmlPath := filepath.Join(temporaryRoot, "surface.html")
	pngPath := filepath.Join(temporaryRoot, "surface.png")
	if err := os.WriteFile(htmlPath, []byte(file.HTML), 0o600); err != nil {
		return nil, fmt.Errorf("writing HTML snapshot: %w", err)
	}

	fileURL := (&url.URL{
		Scheme: "file",
		Path:   "/" + filepath.ToSlash(htmlPath),
	}).String()
	arguments := edgeRasterizerArguments(
		file,
		fileURL,
		pngPath,
		filepath.Join(temporaryRoot, "profile"),
	)
	command := exec.Command(
		edgePath,
		arguments...,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	processJob, err := startEdgeCommandInJob(command)
	if err != nil {
		return nil, fmt.Errorf("starting Edge rasterizer: %w", err)
	}
	defer func() {
		if err := processJob.Close(); err != nil {
			log.Printf("warning: closing Edge export process job: %v", err)
		}
	}()
	commandErr := waitForOwnedCommand(
		command.Wait,
		func() error {
			return stopOwnedCommand(processJob, func() error {
				return killDirectProcess(command.Process)
			})
		},
		30*time.Second,
	)
	if errors.Is(commandErr, errEdgeCommandTimedOut) {
		return nil, commandErr
	}
	if commandErr != nil {
		return nil, fmt.Errorf(
			"Edge rasterizer failed: %w: %s",
			commandErr,
			strings.TrimSpace(output.String()),
		)
	}
	pngContents, err := readDecodedPNGWithRetry(pngPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf(
			"Edge produced no decodable screenshot: %w: %s",
			err,
			strings.TrimSpace(output.String()),
		)
	}
	return pngContents, nil
}

func edgeRasterizerArguments(
	file exportFile,
	fileURL string,
	pngPath string,
	profilePath string,
) []string {
	windowHeight := max(file.Height, headlessMinimumHeight)
	return []string{
		"--headless=new",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-javascript",
		"--host-resolver-rules=MAP * ~NOTFOUND",
		"--hide-scrollbars",
		"--no-first-run",
		"--run-all-compositor-stages-before-draw",
		"--force-device-scale-factor=1",
		"--default-background-color=00000000",
		fmt.Sprintf("--window-size=%d,%d", file.Width, windowHeight),
		"--user-data-dir=" + profilePath,
		"--screenshot=" + pngPath,
		fileURL,
	}
}

func (server *workbenchServer) authorizeExport(request *http.Request) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("export requires application/json")
	}
	if request.Header.Get("Origin") != server.origin {
		return errors.New("export origin is not the local workbench")
	}
	provided := []byte(request.Header.Get("X-Codex-Workbench-Token"))
	expected := []byte(server.exportToken)
	if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
		return errors.New("export token is invalid")
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || !isLoopbackHost(host) {
		return errors.New("export client is not local")
	}
	return nil
}

func requireLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid workbench address %q: %w", address, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("workbench address %q is not loopback", address)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func newWorkbenchToken() (string, error) {
	contents := make([]byte, 32)
	if _, err := rand.Read(contents); err != nil {
		return "", fmt.Errorf("generating workbench token: %w", err)
	}
	return hex.EncodeToString(contents), nil
}

func validateSnapshotHTML(snapshot string) error {
	if len(snapshot) == 0 || len(snapshot) > 4<<20 {
		return errors.New("HTML snapshot is empty or too large")
	}
	lower := strings.ToLower(snapshot)
	trimmed := strings.TrimSpace(lower)
	if !strings.HasPrefix(trimmed, "<!doctype html>") ||
		!strings.Contains(trimmed, "<html") ||
		!strings.Contains(trimmed, "<body") {
		return errors.New("snapshot is not a complete HTML document")
	}
	for _, forbidden := range []string{
		"<script",
		"<iframe",
		"<object",
		"<embed",
		"<img",
		"<link",
		"<base",
		"http-equiv",
		"@import",
		"url(",
		"onload=",
		"onerror=",
		"src=",
		"href=",
	} {
		if strings.Contains(lower, forbidden) {
			return fmt.Errorf("snapshot contains unsupported external or active content %q", forbidden)
		}
	}
	return nil
}

func readDecodedPNGWithRetry(path string, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	delay := 10 * time.Millisecond
	var lastErr error
	for {
		contents, err := os.ReadFile(path)
		if err == nil && len(contents) > 0 {
			if _, decodeErr := png.Decode(bytes.NewReader(contents)); decodeErr == nil {
				return contents, nil
			} else {
				lastErr = decodeErr
			}
		} else if err != nil {
			lastErr = err
		} else {
			lastErr = errors.New("screenshot is empty")
		}
		if time.Now().Add(delay).After(deadline) {
			break
		}
		time.Sleep(delay)
		delay = min(delay*2, 160*time.Millisecond)
	}
	return nil, fmt.Errorf("reading and decoding %q before timeout: %w", path, lastErr)
}

func findEdge() (string, error) {
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("LocalAppData"), "Microsoft", "Edge", "Application", "msedge.exe"),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if runtime.GOOS != "windows" {
		return "", errors.New("the native exporter requires Microsoft Edge on Windows")
	}
	return "", errors.New("Microsoft Edge was not found")
}

func (server *workbenchServer) currentEdgeRendererIdentity() (edgeRendererIdentity, error) {
	resolve := server.resolveEdge
	if resolve == nil {
		resolve = installedEdgeRendererIdentity
	}
	identity, err := resolve()
	if err != nil {
		return edgeRendererIdentity{}, fmt.Errorf("identifying Microsoft Edge renderer: %w", err)
	}
	if strings.TrimSpace(identity.Version) == "" ||
		!isLowerHex(identity.Fingerprint, exportRendererStampLength) {
		return edgeRendererIdentity{}, errors.New("Microsoft Edge renderer identity is invalid")
	}
	return identity, nil
}

func installedEdgeRendererIdentity() (edgeRendererIdentity, error) {
	edgePath, err := findEdge()
	if err != nil {
		return edgeRendererIdentity{}, err
	}
	version, err := windowsFileVersion(edgePath)
	if err != nil {
		return edgeRendererIdentity{}, err
	}
	info, err := os.Stat(edgePath)
	if err != nil {
		return edgeRendererIdentity{}, fmt.Errorf("stat Edge executable: %w", err)
	}
	absolutePath, err := filepath.Abs(edgePath)
	if err != nil {
		return edgeRendererIdentity{}, fmt.Errorf("resolve Edge executable path: %w", err)
	}
	hash := sha256.New()
	_, _ = fmt.Fprintf(
		hash,
		"%s\x00%s\x00%d\x00%d",
		strings.ToLower(filepath.Clean(absolutePath)),
		version,
		info.Size(),
		info.ModTime().UTC().UnixNano(),
	)
	return edgeRendererIdentity{
		Version:     version,
		Fingerprint: hex.EncodeToString(hash.Sum(nil))[:exportRendererStampLength],
	}, nil
}

func windowsFileVersion(path string) (string, error) {
	var zero windows.Handle
	size, err := windows.GetFileVersionInfoSize(path, &zero)
	if err != nil {
		return "", fmt.Errorf("read Edge version size: %w", err)
	}
	if size == 0 {
		return "", errors.New("Edge executable has no version information")
	}
	contents := make([]byte, size)
	if err := windows.GetFileVersionInfo(
		path,
		0,
		size,
		unsafe.Pointer(&contents[0]),
	); err != nil {
		return "", fmt.Errorf("read Edge version information: %w", err)
	}
	var fixed *windows.VS_FIXEDFILEINFO
	fixedSize := uint32(unsafe.Sizeof(windows.VS_FIXEDFILEINFO{}))
	if err := windows.VerQueryValue(
		unsafe.Pointer(&contents[0]),
		`\`,
		unsafe.Pointer(&fixed),
		&fixedSize,
	); err != nil {
		return "", fmt.Errorf("query Edge version information: %w", err)
	}
	if fixed == nil || fixedSize < uint32(unsafe.Sizeof(*fixed)) {
		return "", errors.New("Edge version information is incomplete")
	}
	return fmt.Sprintf(
		"%d.%d.%d.%d",
		fixed.FileVersionMS>>16,
		fixed.FileVersionMS&0xffff,
		fixed.FileVersionLS>>16,
		fixed.FileVersionLS&0xffff,
	), nil
}

func writeAtomic(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating parent directory for %q: %w", path, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary file for %q: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("setting temporary file permissions for %q: %w", path, err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("writing temporary file for %q: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("syncing temporary file for %q: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary file for %q: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replacing %q: %w", path, err)
	}
	return nil
}

func writeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		log.Printf("writing JSON response: %v", err)
	}
}

func noCacheFiles(root http.FileSystem) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		http.FileServer(root).ServeHTTP(response, request)
	})
}
