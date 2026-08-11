//go:build windows && !workbench

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type options struct {
	uiRoot            string
	surfaceID         string
	logFile           string
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

	bundleRoot := filepath.Join(options.uiRoot, "dist")
	app := newNativeApp(bundleRoot, options.surfaceID, startedAt)
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
	flag.Parse()
	return options
}
