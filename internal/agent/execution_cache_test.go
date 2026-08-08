package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"wormhole/internal/config"
)

func TestCommandMayMutateWorkspace(t *testing.T) {
	for _, command := range []string{
		"pwd",
		"ls -la",
		"rg TODO .",
		"git status --short",
		"go test ./...",
		"find . -name '*.go'",
	} {
		if commandMayMutateWorkspace(command) {
			t.Errorf("read-only command classified as mutating: %q", command)
		}
	}
	for _, command := range []string{
		"touch changed.txt",
		"echo changed > changed.txt",
		"go test ./... && touch changed.txt",
		"go test -coverprofile=coverage.out ./...",
		"find . -delete",
		"git checkout main",
	} {
		if !commandMayMutateWorkspace(command) {
			t.Errorf("mutating command classified as read-only: %q", command)
		}
	}
}

func TestQualityCommandAlwaysInvalidatesRepositoryCache(t *testing.T) {
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.Mode = "full"
	cfg.Policy = "full"
	cfg.NoTunnel = true
	cfg.Audit = false
	runtime, err := New(cfg, "test", "pro", "quality-cache")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	before := runtime.currentRepositoryGeneration()
	if _, err := runtime.runQualityCommand(context.Background(), "run_tests", map[string]any{"command": "pwd"}, true); err != nil {
		t.Fatal(err)
	}
	if got := runtime.currentRepositoryGeneration(); got != before+1 {
		t.Fatalf("quality command generation = %d, want %d", got, before+1)
	}
}

func TestRunCommandsInvalidatesRepositoryCacheOncePerMutatingBatch(t *testing.T) {
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.Mode = "full"
	cfg.Policy = "full"
	cfg.NoTunnel = true
	cfg.Audit = false
	cfg.Memory.Enabled = false
	runtime, err := New(cfg, "test", "pro", "execution-cache")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	before := runtime.currentRepositoryGeneration()
	if _, err := runtime.runCommands(context.Background(), map[string]any{
		"parallel": true,
		"commands": []any{
			map[string]any{"command": "pwd"},
			map[string]any{"command": "ls"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got := runtime.currentRepositoryGeneration(); got != before {
		t.Fatalf("read-only batch generation = %d, want %d", got, before)
	}

	if _, err := runtime.runCommands(context.Background(), map[string]any{
		"parallel": true,
		"commands": []any{
			map[string]any{"command": "echo one > one.txt"},
			map[string]any{"command": "echo two > two.txt"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got := runtime.currentRepositoryGeneration(); got != before+1 {
		t.Fatalf("mutating batch generation = %d, want %d", got, before+1)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(cfg.Workspace, name)); err != nil {
			t.Fatalf("expected %s to be created: %v", name, err)
		}
	}
}
