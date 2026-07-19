// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package upstreammcp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func newUpstreamCommand(resolved string, args []string) (*exec.Cmd, error) {
	extension := strings.ToLower(filepath.Ext(resolved))
	if extension != ".cmd" && extension != ".bat" {
		return exec.Command(resolved, args...), nil
	}
	commandLine, err := composeWindowsBatchCommand(resolved, args)
	if err != nil {
		return nil, err
	}
	comspec := strings.TrimSpace(os.Getenv("ComSpec"))
	if comspec == "" {
		comspec, err = exec.LookPath("cmd.exe")
		if err != nil {
			return nil, fmt.Errorf("resolve cmd.exe for Windows batch MCP command: %w", err)
		}
	}
	if strings.ContainsAny(comspec, "\r\n\x00\"") {
		return nil, fmt.Errorf("ComSpec contains an unsupported control or quote character")
	}
	cmd := exec.Command(comspec)
	cmd.Args = []string{comspec}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine: `"` + comspec + `" /d /s /v:off /c ` + commandLine,
	}
	return cmd, nil
}
