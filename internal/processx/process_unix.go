//go:build !windows

package processx

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func prepareBackground(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killBackground(cmd *exec.Cmd) error {
	return signalBackground(cmd, syscall.SIGTERM)
}

func forceKillBackground(cmd *exec.Cmd) error {
	return signalBackground(cmd, syscall.SIGKILL)
}

func signalBackground(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, signal); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return killErr
		}
	}
	return nil
}
