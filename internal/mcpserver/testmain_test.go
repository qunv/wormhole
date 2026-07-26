// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "codebridge-mcpserver-test-*")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("CODEBRIDGE_HOME", home); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
