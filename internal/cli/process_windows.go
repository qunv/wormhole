//go:build windows

package cli

import (
	"os/exec"
	"strconv"
	"syscall"
)

func prepareDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000}
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH")
	raw, err := cmd.Output()
	return err == nil && len(raw) > 0
}

func stopPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}
