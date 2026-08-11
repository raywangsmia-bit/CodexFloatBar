//go:build workbench

package main

import (
	"errors"
	"fmt"
	"os"
	"time"
)

var errEdgeCommandTimedOut = errors.New("Edge rasterizer timed out")

type edgeCommandJob interface {
	Close() error
}

type threadOwner struct {
	threadID       uint32
	ownerProcessID uint32
}

func waitForOwnedCommand(
	wait func() error,
	stop func() error,
	timeout time.Duration,
) error {
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- wait()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-waitDone:
		return err
	case <-timer.C:
		stopErr := stop()
		<-waitDone
		return errors.Join(errEdgeCommandTimedOut, stopErr)
	}
}

func stopOwnedCommand(
	job edgeCommandJob,
	killDirectProcess func() error,
) error {
	closeErr := job.Close()
	if closeErr == nil {
		return nil
	}
	return errors.Join(closeErr, killDirectProcess())
}

func killDirectProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stopping directly held Edge process: %w", err)
	}
	return nil
}

func uniqueOwnedThreadID(
	processID uint32,
	threads []threadOwner,
) (uint32, error) {
	var threadID uint32
	count := 0
	for _, thread := range threads {
		if thread.ownerProcessID != processID {
			continue
		}
		threadID = thread.threadID
		count++
	}
	if count != 1 {
		return 0, fmt.Errorf(
			"suspended process %d has %d initial threads, want exactly one",
			processID,
			count,
		)
	}
	return threadID, nil
}
