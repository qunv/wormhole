// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package upstreammcp

import "os/exec"

func newUpstreamCommand(resolved string, args []string) (*exec.Cmd, error) {
	return exec.Command(resolved, args...), nil
}
