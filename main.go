//go:build windows

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

type options struct {
	uiRoot            string
	surfaceID         string
	logFile           string
	workbench         bool
	openBrowser       bool
	workbenchAddress  string
	selfTestOutput    string
	selfTestWakeProbe bool
	quietUninstall    bool
}

func main() {
	runtime.LockOSThread()
	startedAt := time.Now()
	options := parseOptions()
	if options.quietUninstall {
		exitCode, err := runQuietUninstall()
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(exitCode)
	}
	closeLog, err := configureLog(options.logFile)
	if err != nil {
		log.Fatal(err)
	}
	defer closeLog()

	workbenchRoot := filepath.Join(options.uiRoot, "workbench")
	bundleRoot := filepath.Join(options.uiRoot, "dist")
	if options.workbench {
		url, err := startWorkbenchServer(
			options.workbenchAddress,
			startedAt,
			workbenchRoot,
			bundleRoot,
		)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("UI workbench: %s", url)
		if options.openBrowser {
			openURL(url)
		}
		if err := waitForManifest(bundleRoot, 10*time.Minute); err != nil {
			log.Fatal(err)
		}
	}

	app := newNativeApp(bundleRoot, options.surfaceID, startedAt)
	if options.workbench {
		app.useWorkbenchIdentity()
	}
	if options.selfTestOutput != "" || options.selfTestWakeProbe {
		app.useSelfTestIdentity()
	}
	if options.selfTestOutput != "" {
		app.selfTest = newNativeSelfTest(options.selfTestOutput)
	}
	if err := app.run(); err != nil {
		if app.selfTest != nil {
			if reportErr := app.selfTest.ensureFailureReport(err); reportErr != nil {
				log.Printf("writing native self-test failure report: %v", reportErr)
			}
		}
		log.Fatal(err)
	}
}

func parseOptions() options {
	var options options
	flag.StringVar(&options.uiRoot, "ui-root", defaultUIRoot(), "UI source and bundle root")
	flag.StringVar(&options.surfaceID, "surface", "", "surface ID from the bundle manifest")
	flag.StringVar(&options.logFile, "log-file", "", "optional runtime log file")
	flag.BoolVar(&options.workbench, "workbench", false, "start the development-only HTML workbench")
	flag.BoolVar(&options.openBrowser, "open-browser", true, "open the workbench in the default browser")
	flag.StringVar(
		&options.selfTestOutput,
		"self-test-output",
		"",
		"run the native window self-test and write its JSON report",
	)
	flag.BoolVar(
		&options.selfTestWakeProbe,
		"self-test-wake-probe",
		false,
		"internal second-instance wake probe",
	)
	flag.BoolVar(
		&options.quietUninstall,
		"quiet-uninstall",
		false,
		"run the registered silent uninstall command",
	)
	flag.StringVar(
		&options.workbenchAddress,
		"workbench-address",
		"127.0.0.1:9315",
		"development workbench listen address",
	)
	flag.Parse()
	return options
}

func defaultUIRoot() string {
	executablePath, executableErr := os.Executable()
	workingDirectory, workingDirectoryErr := os.Getwd()
	if workingDirectoryErr != nil {
		workingDirectory = "."
	}
	if executableErr != nil {
		return filepath.Join(workingDirectory, "ui")
	}
	return chooseUIRoot(executablePath, workingDirectory)
}

func chooseUIRoot(executablePath string, workingDirectory string) string {
	executableDirectory := filepath.Dir(executablePath)
	candidates := []string{
		filepath.Join(executableDirectory, "ui"),
		filepath.Join(filepath.Dir(executableDirectory), "ui"),
		filepath.Join(workingDirectory, "ui"),
	}
	for _, candidate := range candidates {
		manifest := filepath.Join(candidate, "dist", "manifest.json")
		if info, err := os.Stat(manifest); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return candidates[0]
}

func configureLog(path string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving log path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	log.SetOutput(file)
	return func() {
		_ = file.Close()
	}, nil
}

func openURL(url string) {
	command := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	if err := command.Start(); err != nil {
		log.Printf("opening workbench: %v", err)
	}
}

func waitForManifest(bundleRoot string, timeout time.Duration) error {
	manifestPath := filepath.Join(bundleRoot, "manifest.json")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(manifestPath); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %q", manifestPath)
}
