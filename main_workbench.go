//go:build windows && workbench

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	exportOnceTimeout     = 80 * time.Second
	exportOnceStopTimeout = 5 * time.Second
)

var errExportOnceTimedOut = errors.New("automatic workbench export timed out")

type workbenchOptions struct {
	uiRoot        string
	logFile       string
	openBrowser   bool
	listenAddress string
	exportOnce    bool
}

type exportOnceWaitKind uint8

const (
	exportOnceReported exportOnceWaitKind = iota + 1
	exportOnceEdgeExited
	exportOnceExpired
)

type exportOnceTerminal struct {
	kind       exportOnceWaitKind
	report     autoExportReport
	processErr error
}

func main() {
	if err := runWorkbench(os.Args[1:]); err != nil {
		log.Printf("UI workbench failed: %v", err)
		os.Exit(1)
	}
}

func runWorkbench(arguments []string) error {
	startedAt := time.Now()
	options, err := parseWorkbenchOptions(arguments)
	if err != nil {
		return err
	}
	closeLog, err := configureLog(options.logFile)
	if err != nil {
		return err
	}
	defer closeLog()

	workbenchRoot := filepath.Join(options.uiRoot, "workbench")
	bundleRoot := filepath.Join(options.uiRoot, "dist")
	if options.exportOnce {
		return runWorkbenchExportOnce(
			options.listenAddress,
			startedAt,
			workbenchRoot,
			bundleRoot,
		)
	}

	workbenchURL, err := startWorkbenchServer(
		options.listenAddress,
		startedAt,
		workbenchRoot,
		bundleRoot,
	)
	if err != nil {
		return err
	}
	log.Printf("UI workbench: %s", workbenchURL)
	if options.openBrowser {
		openWorkbenchURL(workbenchURL)
	}

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	<-interrupt
	return nil
}

func parseWorkbenchOptions(arguments []string) (workbenchOptions, error) {
	options := workbenchOptions{}
	flags := flag.NewFlagSet("codexfloatingbar-workbench", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.uiRoot, "ui-root", defaultUIRoot(), "UI source and bundle root")
	flags.StringVar(&options.logFile, "log-file", "", "optional workbench log file")
	flags.BoolVar(&options.openBrowser, "open-browser", true, "open the workbench in the default browser")
	flags.StringVar(
		&options.listenAddress,
		"listen-address",
		"127.0.0.1:9315",
		"development workbench listen address",
	)
	flags.BoolVar(
		&options.exportOnce,
		"export-once",
		false,
		"export the complete UI bundle in owned headless Microsoft Edge, then exit",
	)
	if err := flags.Parse(arguments); err != nil {
		return workbenchOptions{}, fmt.Errorf("parsing workbench options: %w", err)
	}
	if flags.NArg() != 0 {
		return workbenchOptions{}, fmt.Errorf("unexpected workbench arguments: %q", flags.Args())
	}
	if options.exportOnce {
		options.openBrowser = false
	}
	return options, nil
}

func runWorkbenchExportOnce(
	address string,
	startedAt time.Time,
	workbenchRoot string,
	bundleRoot string,
) error {
	running, err := startWorkbenchServerWithOptions(
		address,
		startedAt,
		workbenchRoot,
		bundleRoot,
		workbenchServerOptions{AutoExport: true},
	)
	if err != nil {
		return err
	}
	log.Printf("UI workbench automatic export: %s", running.URL)
	report, exportErr := exportWorkbenchOnce(
		running.URL,
		running.AutoReports,
		exportOnceTimeout,
	)
	closeErr := running.Close()
	if exportErr != nil {
		return errors.Join(exportErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	log.Printf(
		"UI workbench export complete: files=%d rendered=%d reused=%d atlases=%d fallback=%d upToDate=%t",
		report.Files,
		report.Rendered,
		report.Reused,
		report.Atlases,
		report.Fallback,
		report.UpToDate,
	)
	return nil
}

func exportWorkbenchOnce(
	workbenchURL string,
	reports <-chan autoExportReport,
	timeout time.Duration,
) (autoExportReport, error) {
	edgePath, err := findEdge()
	if err != nil {
		return autoExportReport{}, err
	}
	temporaryRoot, err := os.MkdirTemp("", "codexfloatingbar-export-")
	if err != nil {
		return autoExportReport{}, fmt.Errorf("creating automatic export profile root: %w", err)
	}
	defer cleanupTemporaryExportRoot(temporaryRoot)

	command := exec.Command(
		edgePath,
		exportOnceEdgeArguments(
			workbenchURL,
			filepath.Join(temporaryRoot, "profile"),
		)...,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	processJob, err := startEdgeCommandInJob(command)
	if err != nil {
		return autoExportReport{}, fmt.Errorf("starting automatic export Edge: %w", err)
	}
	processDone := make(chan error, 1)
	go func() {
		processDone <- command.Wait()
	}()

	terminal := waitForExportOnceTerminal(reports, processDone, timeout)
	stopErr := stopOwnedCommand(processJob, func() error {
		return killDirectProcess(command.Process)
	})
	if terminal.kind != exportOnceEdgeExited {
		if err := waitForStoppedExportOnceProcess(processDone, command.Process); err != nil {
			stopErr = errors.Join(stopErr, err)
		}
	}

	switch terminal.kind {
	case exportOnceReported:
		if !terminal.report.OK {
			return autoExportReport{}, errors.Join(
				fmt.Errorf("automatic workbench export failed: %s", terminal.report.Error),
				stopErr,
			)
		}
		if stopErr != nil {
			return autoExportReport{}, stopErr
		}
		return terminal.report, nil
	case exportOnceEdgeExited:
		summary := strings.TrimSpace(output.String())
		if terminal.processErr == nil {
			terminal.processErr = errors.New("Edge exited successfully before reporting a result")
		}
		return autoExportReport{}, errors.Join(
			fmt.Errorf(
				"automatic export Edge exited before reporting a result: %w: %s",
				terminal.processErr,
				summary,
			),
			stopErr,
		)
	case exportOnceExpired:
		return autoExportReport{}, errors.Join(errExportOnceTimedOut, stopErr)
	default:
		return autoExportReport{}, errors.Join(
			errors.New("automatic export reached an invalid terminal state"),
			stopErr,
		)
	}
}

func exportOnceEdgeArguments(workbenchURL string, profilePath string) []string {
	return []string{
		"--headless=new",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-component-update",
		"--hide-scrollbars",
		"--no-first-run",
		"--run-all-compositor-stages-before-draw",
		"--force-device-scale-factor=1",
		"--window-size=1600,1200",
		"--user-data-dir=" + profilePath,
		workbenchURL,
	}
}

func waitForExportOnceTerminal(
	reports <-chan autoExportReport,
	processDone <-chan error,
	timeout time.Duration,
) exportOnceTerminal {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case report := <-reports:
		return exportOnceTerminal{kind: exportOnceReported, report: report}
	case processErr := <-processDone:
		return exportOnceTerminal{kind: exportOnceEdgeExited, processErr: processErr}
	case <-timer.C:
		return exportOnceTerminal{kind: exportOnceExpired}
	}
}

func waitForStoppedExportOnceProcess(done <-chan error, process *os.Process) error {
	timer := time.NewTimer(exportOnceStopTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	}
	if err := killDirectProcess(process); err != nil {
		return err
	}
	timer.Reset(exportOnceStopTimeout)
	select {
	case <-done:
		return nil
	case <-timer.C:
		return errors.New("automatic export Edge did not stop after direct termination")
	}
}

func openWorkbenchURL(workbenchURL string) {
	command := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", workbenchURL)
	if err := command.Start(); err != nil {
		log.Printf("opening workbench: %v", err)
	}
}
