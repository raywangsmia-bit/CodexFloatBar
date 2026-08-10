//go:build windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const edgeJobHelperMode = "CODEXFLOATINGBAR_EDGE_JOB_HELPER"

func TestNewKillOnCloseJobSetsLimit(t *testing.T) {
	job, err := newKillOnCloseJob()
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()

	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	err = windows.QueryInformationJobObject(
		job.handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if information.BasicLimitInformation.LimitFlags&
		windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		t.Fatal("job does not have JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE")
	}
}

func TestStartEdgeCommandInJobResumesSuspendedProcess(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestEdgeJobProcessHelper$")
	command.Env = append(os.Environ(), edgeJobHelperMode+"=output")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output

	job, err := startEdgeCommandInJob(command)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		_ = job.Close()
		t.Fatalf("waiting for helper: %v: %s", err, output.String())
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "helper-stdout") ||
		!strings.Contains(output.String(), "helper-stderr") {
		t.Fatalf("combined helper output = %q", output.String())
	}
	if command.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		t.Fatal("helper was not created suspended")
	}
}

func TestClosingEdgeJobStopsInheritedChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEdgeJobProcessHelper$")
	command.Env = append(os.Environ(), edgeJobHelperMode+"=parent")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr

	job, err := startEdgeCommandInJob(command)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("reading child PID: %v: %s", err, stderr.String())
	}
	childProcessID, err := parseEdgeJobHelperProcessID(line)
	if err != nil {
		t.Fatal(err)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, childProcessID)
	if err != nil {
		t.Fatalf("opening helper child for observation: %v", err)
	}
	defer windows.CloseHandle(child)

	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	waitStarted := time.Now()
	_ = command.Wait()
	if elapsed := time.Since(waitStarted); elapsed > 5*time.Second {
		t.Fatalf("job-owned helper took %v to exit", elapsed)
	}
	waitResult, err := windows.WaitForSingleObject(child, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if waitResult != windows.WAIT_OBJECT_0 {
		t.Fatalf("inherited helper child remained alive: wait result %#x", waitResult)
	}
}

func TestEdgeJobProcessHelper(t *testing.T) {
	switch os.Getenv(edgeJobHelperMode) {
	case "output":
		fmt.Fprint(os.Stdout, "helper-stdout")
		fmt.Fprint(os.Stderr, "helper-stderr")
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestEdgeJobProcessHelper$")
		child.Env = append(os.Environ(), edgeJobHelperMode+"=child")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(os.Stdout, "CHILD=%d\n", child.Process.Pid)
		if err := child.Wait(); err != nil {
			t.Fatal(err)
		}
	case "child":
		time.Sleep(30 * time.Second)
	}
}

func parseEdgeJobHelperProcessID(line string) (uint32, error) {
	value := strings.TrimSpace(strings.TrimPrefix(line, "CHILD="))
	processID, err := strconv.ParseUint(value, 10, 32)
	if err != nil || !strings.HasPrefix(strings.TrimSpace(line), "CHILD=") {
		return 0, fmt.Errorf("invalid helper child line %q", line)
	}
	return uint32(processID), nil
}
