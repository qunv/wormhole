// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package upstreammcp

import (
	"fmt"
	"strings"
)

// composeWindowsBatchCommand builds the command string consumed by cmd.exe /c.
// Every token is quoted, delayed expansion is disabled by the caller, and the
// two expansion characters that remain unsafe inside quotes are rejected.
func composeWindowsBatchCommand(command string, args []string) (string, error) {
	values := make([]string, 0, len(args)+1)
	values = append(values, command)
	values = append(values, args...)
	quoted := make([]string, 0, len(values))
	for index, value := range values {
		if strings.ContainsAny(value, "\r\n\x00\"%!") {
			return "", fmt.Errorf("Windows batch command token %d contains an unsupported control, quote, or expansion character", index)
		}
		quoted = append(quoted, `"`+value+`"`)
	}
	// cmd.exe /s /c expects the command itself to be wrapped in an additional
	// pair of quotes when the executable path is quoted.
	return `"` + strings.Join(quoted, " ") + `"`, nil
}
