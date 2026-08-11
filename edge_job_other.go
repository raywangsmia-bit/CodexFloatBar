//go:build !windows && workbench

package main

import (
	"errors"
	"os/exec"
)

func startEdgeCommandInJob(command *exec.Cmd) (edgeCommandJob, error) {
	return nil, errors.New("Edge process ownership requires Windows Job Objects")
}
