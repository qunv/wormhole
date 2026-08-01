//go:build !windows

package admin

import (
	"os/exec"
	"syscall"
)

func prepareAdminRestart(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
