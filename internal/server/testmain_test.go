// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "codebridge-server-test-*")
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
