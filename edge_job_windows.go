//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type killOnCloseJob struct {
	handle windows.Handle
}

var getProcessIDOfThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIdOfThread")

func startEdgeCommandInJob(command *exec.Cmd) (edgeCommandJob, error) {
	job, err := newKillOnCloseJob()
	if err != nil {
		return nil, err
	}

	configureSuspendedStart(command)
	if err := command.Start(); err != nil {
		return nil, errors.Join(
			fmt.Errorf("starting suspended Edge process: %w", err),
			job.Close(),
		)
	}

	assigned := false
	var ownershipErr error
	handleErr := command.Process.WithHandle(func(processHandle uintptr) {
		ownershipErr = windows.AssignProcessToJobObject(
			job.handle,
			windows.Handle(processHandle),
		)
		if ownershipErr != nil {
			return
		}
		assigned = true
		ownershipErr = resumeSuspendedInitialThread(
			windows.Handle(processHandle),
			uint32(command.Process.Pid),
		)
	})
	if handleErr != nil {
		ownershipErr = errors.Join(ownershipErr, handleErr)
	}
	if ownershipErr != nil {
		cleanupErr := abortStartedEdgeCommand(command, job, assigned)
		return nil, errors.Join(
			fmt.Errorf("taking ownership of suspended Edge process: %w", ownershipErr),
			cleanupErr,
		)
	}
	return job, nil
}

func newKillOnCloseJob() (*killOnCloseJob, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("creating Edge process job: %w", err)
	}
	job := &killOnCloseJob{handle: handle}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("configuring Edge process job: %w", err),
			job.Close(),
		)
	}
	return job, nil
}

func configureSuspendedStart(command *exec.Cmd) {
	attributes := syscall.SysProcAttr{}
	if command.SysProcAttr != nil {
		attributes = *command.SysProcAttr
	}
	attributes.CreationFlags |= windows.CREATE_SUSPENDED
	command.SysProcAttr = &attributes
}

func resumeSuspendedInitialThread(
	process windows.Handle,
	processID uint32,
) error {
	threads, err := snapshotThreadOwners()
	if err != nil {
		return err
	}
	threadID, err := uniqueOwnedThreadID(processID, threads)
	if err != nil {
		return err
	}
	thread, err := windows.OpenThread(
		windows.THREAD_SUSPEND_RESUME|windows.THREAD_QUERY_LIMITED_INFORMATION,
		false,
		threadID,
	)
	if err != nil {
		return fmt.Errorf("opening suspended Edge thread %d: %w", threadID, err)
	}
	defer windows.CloseHandle(thread)
	threadProcessID, err := processIDOfThread(thread)
	if err != nil {
		return err
	}
	if threadProcessID != processID {
		return fmt.Errorf(
			"suspended Edge thread %d belongs to process %d, want %d",
			threadID,
			threadProcessID,
			processID,
		)
	}
	waitResult, err := windows.WaitForSingleObject(process, 0)
	if err != nil {
		return fmt.Errorf("checking suspended Edge process state: %w", err)
	}
	if waitResult != uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf(
			"suspended Edge process %d is no longer active: wait result %#x",
			processID,
			waitResult,
		)
	}

	previousSuspendCount, err := windows.ResumeThread(thread)
	if err != nil {
		return fmt.Errorf("resuming suspended Edge thread %d: %w", threadID, err)
	}
	if previousSuspendCount != 1 {
		return fmt.Errorf(
			"suspended Edge thread %d had suspend count %d, want 1",
			threadID,
			previousSuspendCount,
		)
	}
	return nil
}

func processIDOfThread(thread windows.Handle) (uint32, error) {
	processID, _, callErr := getProcessIDOfThread.Call(uintptr(thread))
	if processID == 0 {
		return 0, fmt.Errorf("getting suspended Edge thread owner: %w", callErr)
	}
	return uint32(processID), nil
}

func snapshotThreadOwners() ([]threadOwner, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return nil, fmt.Errorf("snapshotting threads for suspended Edge process: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading first Windows thread: %w", err)
	}

	threads := make([]threadOwner, 0, 512)
	for {
		threads = append(threads, threadOwner{
			threadID:       entry.ThreadID,
			ownerProcessID: entry.OwnerProcessID,
		})
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return threads, nil
			}
			return nil, fmt.Errorf("reading Windows thread: %w", err)
		}
	}
}

func abortStartedEdgeCommand(
	command *exec.Cmd,
	job *killOnCloseJob,
	assigned bool,
) error {
	cleanupErrors := make([]error, 0, 3)
	if assigned {
		if err := job.Close(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			if err := killDirectProcess(command.Process); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	} else {
		if err := killDirectProcess(command.Process); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
		if err := job.Close(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if err := command.Wait(); err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) && !errors.Is(err, os.ErrProcessDone) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("waiting for aborted Edge process: %w", err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func (job *killOnCloseJob) Close() error {
	if job == nil || job.handle == 0 {
		return nil
	}
	handle := job.handle
	job.handle = 0
	if err := windows.CloseHandle(handle); err != nil {
		return fmt.Errorf("closing Edge process job: %w", err)
	}
	return nil
}
