// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	base, err := os.MkdirTemp("", "codebridge-cli-test-*")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create isolated CLI test home: %v\n", err)
		os.Exit(1)
	}
	_ = os.Unsetenv("CODEBRIDGE_DATA_DIR")
	_ = os.Unsetenv("CODEBRIDGE_CONFIG_PATH")
	_ = os.Unsetenv("CODEBRIDGE_WORKSPACE_REGISTRY_PATH")
	if err := os.Setenv("CODEBRIDGE_HOME", base); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "configure isolated CLI test home: %v\n", err)
		_ = os.RemoveAll(base)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(base)
	os.Exit(code)
}
