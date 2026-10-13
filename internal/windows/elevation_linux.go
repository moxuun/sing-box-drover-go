//go:build linux

package windows

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func IsProcessElevated() bool { return os.Geteuid() == 0 }

// Keep the GUI in the user session; only the separately supplied core needs
// network administration privileges.
func CheckTunPrivileges(corePath string) error {
	if IsProcessElevated() {
		return nil
	}
	var capability [24]byte
	n, err := unix.Getxattr(corePath, "security.capability", capability[:])
	if err == nil && n >= 12 {
		flags := binary.LittleEndian.Uint32(capability[:4])
		permitted := binary.LittleEndian.Uint32(capability[4:8])
		if flags&1 != 0 && permitted&(1<<unix.CAP_NET_ADMIN) != 0 {
			return nil
		}
	}
	return fmt.Errorf("Linux TUN requires CAP_NET_ADMIN on %s; configure the sing-box executable before enabling TUN", corePath)
}

func LaunchSelf(params string, elevate bool) error {
	if elevate && !IsProcessElevated() {
		return fmt.Errorf("Linux controller elevation is unsupported; grant network capabilities to sing-box instead")
	}
	file, err := os.Executable()
	if err != nil {
		return err
	}
	// Linux handoffs contain only the controller's fixed feature flags.
	args := strings.Fields(params)
	for _, arg := range args {
		switch arg {
		case "-restart", "-tun", "-no-tun", "-proxy", "-no-proxy":
		default:
			return fmt.Errorf("unsupported Linux handoff argument %q", arg)
		}
	}
	cmd := exec.Command(file, args...)
	cmd.Dir = filepath.Dir(file)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch controller: %w", err)
	}
	return cmd.Process.Release()
}
