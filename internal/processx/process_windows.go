//go:build windows

package processx

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func prepareBackground(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func killBackground(cmd *exec.Cmd) error {
	return forceKillBackground(cmd)
}

func forceKillBackground(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	killer := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	if err := killer.Run(); err != nil {
		if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return killErr
		}
	}
	return nil
}
