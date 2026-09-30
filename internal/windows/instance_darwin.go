//go:build darwin

package windows

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const instanceLockEnvironment = "SING_BOX_DROVER_INSTANCE_LOCK"

type SingleInstance struct {
	file *os.File
}

func defaultInstanceLockPath() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	return filepath.Join(cacheDir, "sing-box-drover", "instance.lock"), nil
}

func instanceLockPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv(instanceLockEnvironment)); path != "" {
		return path, nil
	}
	return defaultInstanceLockPath()
}

func AcquireSingleInstance(name string) (*SingleInstance, bool, error) {
	path, err := instanceLockPath()
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, fmt.Errorf("create instance directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open instance lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock instance: %w", err)
	}
	if err := file.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
		_ = file.Sync()
	}
	return &SingleInstance{file: file}, true, nil
}

func (i *SingleInstance) Close() {
	if i == nil || i.file == nil {
		return
	}
	_ = unix.Flock(int(i.file.Fd()), unix.LOCK_UN)
	_ = i.file.Close()
	i.file = nil
}
