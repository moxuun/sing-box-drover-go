//go:build !windows

package core

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

type processJob interface{}

func executableDir(path string) string { return filepath.Dir(path) }

func configureCommand(*exec.Cmd) {}

func attachProcessJob(*os.Process) (processJob, error) { return nil, nil }

func closeProcessJob(processJob) {}

func requestGracefulStop(process *os.Process) error {
	if process == nil {
		return errors.New("process is nil")
	}
	return process.Signal(os.Interrupt)
}
