//go:build windows

package admin

import (
	"os/exec"
	"syscall"
)

func prepareAdminRestart(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000}
}
