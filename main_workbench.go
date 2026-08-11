//go:build windows && workbench

package main

import (
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type workbenchOptions struct {
	uiRoot        string
	logFile       string
	openBrowser   bool
	listenAddress string
}

func main() {
	startedAt := time.Now()
	options := parseWorkbenchOptions()
	closeLog, err := configureLog(options.logFile)
	if err != nil {
		log.Fatal(err)
	}
	defer closeLog()

	url, err := startWorkbenchServer(
		options.listenAddress,
		startedAt,
		filepath.Join(options.uiRoot, "workbench"),
		filepath.Join(options.uiRoot, "dist"),
	)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("UI workbench: %s", url)
	if options.openBrowser {
		openWorkbenchURL(url)
	}

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	<-interrupt
}

func parseWorkbenchOptions() workbenchOptions {
	var options workbenchOptions
	flag.StringVar(&options.uiRoot, "ui-root", defaultUIRoot(), "UI source and bundle root")
	flag.StringVar(&options.logFile, "log-file", "", "optional workbench log file")
	flag.BoolVar(&options.openBrowser, "open-browser", true, "open the workbench in the default browser")
	flag.StringVar(
		&options.listenAddress,
		"listen-address",
		"127.0.0.1:9315",
		"development workbench listen address",
	)
	flag.Parse()
	return options
}

func openWorkbenchURL(url string) {
	command := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	if err := command.Start(); err != nil {
		log.Printf("opening workbench: %v", err)
	}
}
