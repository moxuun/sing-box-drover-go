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

func requestGracefulStop(process *os.Process) (func(), error) {
	if process == nil {
		return nil, errors.New("process is nil")
	}
	return func() {}, process.Signal(os.Interrupt)
}
