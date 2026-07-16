//go:build windows

package processx

import (
	"os/exec"
	"strconv"
	"syscall"
)

func prepareBackground(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func killBackground(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	killer := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	if err := killer.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
