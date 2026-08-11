//go:build workbench

package main

import (
	"errors"
	"testing"
	"time"
)

type recordingEdgeJob struct {
	closeCalls int
	closeErr   error
}

func TestUniqueOwnedThreadID(t *testing.T) {
	t.Parallel()

	threads := []threadOwner{
		{threadID: 12, ownerProcessID: 90},
		{threadID: 13, ownerProcessID: 91},
		{threadID: 14, ownerProcessID: 92},
	}
	threadID, err := uniqueOwnedThreadID(91, threads)
	if err != nil {
		t.Fatal(err)
	}
	if threadID != 13 {
		t.Fatalf("thread ID = %d, want 13", threadID)
	}
}

func TestUniqueOwnedThreadIDRejectsMissingOrMultiple(t *testing.T) {
	t.Parallel()

	threads := []threadOwner{
		{threadID: 12, ownerProcessID: 90},
		{threadID: 13, ownerProcessID: 90},
	}
	if _, err := uniqueOwnedThreadID(90, threads); err == nil {
		t.Fatal("multiple owned threads were accepted")
	}
	if _, err := uniqueOwnedThreadID(91, threads); err == nil {
		t.Fatal("missing owned thread was accepted")
	}
}

func TestWaitForOwnedCommandReturnsCommandFailure(t *testing.T) {
	t.Parallel()

	commandErr := errors.New("command failed")
	job := &recordingEdgeJob{}
	err := waitForOwnedCommand(
		func() error { return commandErr },
		func() error {
			t.Fatal("stop called before command failure returned")
			return nil
		},
		time.Second,
	)
	if !errors.Is(err, commandErr) {
		t.Fatalf("wait error = %v, want command failure", err)
	}
	if job.closeCalls != 0 {
		t.Fatalf("job close calls = %d, want 0", job.closeCalls)
	}
}

func TestWaitForOwnedCommandTimeoutClosesJobAndWaits(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("close failed")
	job := &recordingEdgeJob{closeErr: closeErr}
	stopped := make(chan struct{})
	killCalls := 0
	waitReturned := make(chan struct{})
	err := waitForOwnedCommand(
		func() error {
			<-stopped
			close(waitReturned)
			return errors.New("job closed")
		},
		func() error {
			return stopOwnedCommand(job, func() error {
				killCalls++
				close(stopped)
				return nil
			})
		},
		10*time.Millisecond,
	)
	if !errors.Is(err, errEdgeCommandTimedOut) {
		t.Fatalf("wait error = %v, want timeout", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("wait error = %v, want job close failure", err)
	}
	if job.closeCalls != 1 {
		t.Fatalf("job close calls = %d, want 1", job.closeCalls)
	}
	if killCalls != 1 {
		t.Fatalf("direct kill calls = %d, want 1", killCalls)
	}
	select {
	case <-waitReturned:
	default:
		t.Fatal("timeout returned before command Wait completed")
	}
}

func (job *recordingEdgeJob) Close() error {
	job.closeCalls++
	return job.closeErr
}
