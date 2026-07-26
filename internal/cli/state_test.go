// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codebridge/internal/config"
)

func TestStateDataDirsIncludesUnregisteredInstances(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEBRIDGE_HOME", home)
	orphanInstance := filepath.Join(config.AppDataDir(), "instances", "removed-workspace")
	if err := os.MkdirAll(orphanInstance, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := stateDataDirs()
	found := false
	for _, path := range paths {
		if filepath.Clean(path) == filepath.Clean(orphanInstance) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("unregistered instance data dir missing from %#v", paths)
	}
}

func TestStateGCDryRunAndApply(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEBRIDGE_HOME", home)
	workspaceDir := filepath.Join(config.AppDataDir(), "workspaces", "empty")
	if err := os.MkdirAll(filepath.Join(workspaceDir, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Port = reserveAvailablePort(t)

	var output bytes.Buffer
	app := App{Stdout: &output, Stderr: &output, Stdin: strings.NewReader("")}
	if err := app.stateCommand(cfg, options{Rest: []string{"gc"}, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "would remove") {
		t.Fatalf("dry-run summary missing: %s", output.String())
	}
	if _, err := os.Stat(workspaceDir); err != nil {
		t.Fatalf("dry-run removed state: %v", err)
	}

	output.Reset()
	if err := app.stateCommand(cfg, options{Rest: []string{"gc"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "State GC removed") {
		t.Fatalf("apply summary missing: %s", output.String())
	}
	if _, err := os.Stat(workspaceDir); !os.IsNotExist(err) {
		t.Fatalf("state was not removed: %v", err)
	}
}
