package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"wormhole/internal/config"
)

func TestTaskContextHonorsCharacterBudget(t *testing.T) {
	runtime := newContextBudgetRuntime(t)
	for index := 0; index < 40; index++ {
		path := filepath.Join(runtime.Workspace.Primary, "pkg", fmt.Sprintf("feature%02d", index), "handler.go")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("package feature%02d\n\nfunc PerformanceHandler%02d() string {\n\treturn \"performance context payload %s\"\n}\n", index, index, longTestText(600))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	value, err := runtime.taskContext(context.Background(), map[string]any{
		"query":        "performance handler context payload",
		"detail_level": "compact", "token_budget": 1_000,
		"max_entries": 500, "search_limit": 40, "max_read_files": 12,
		"include_codegraph": false, "include_git": false, "include_memory": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if got := estimatedJSONChars(result); got > 4_000 {
		t.Fatalf("task context chars = %d, want <= 4000", got)
	}
	if result["truncated"] != true {
		t.Fatalf("expected bounded task context to report truncation: %#v", result)
	}
}

func TestWorkspaceSnapshotHonorsCharacterBudgetAndMemoryFlag(t *testing.T) {
	runtime := newContextBudgetRuntime(t)
	for index := 0; index < 120; index++ {
		path := filepath.Join(runtime.Workspace.Primary, "internal", fmt.Sprintf("package%03d", index), "service.go")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(fmt.Sprintf("package package%03d\nfunc Service%03d() {}\n", index, index)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	value, err := runtime.workspaceSnapshot(context.Background(), map[string]any{
		"detail_level": "compact", "token_budget": 1_000,
		"max_entries": 500, "include_symbols": true, "include_memory": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if got := estimatedJSONChars(result); got > 4_000 {
		t.Fatalf("workspace snapshot chars = %d, want <= 4000", got)
	}
	if _, exists := result["memory"]; exists {
		t.Fatalf("workspace snapshot ignored include_memory=false: %#v", result["memory"])
	}
	if result["truncated"] != true {
		t.Fatalf("expected bounded snapshot to report truncation")
	}
}

func newContextBudgetRuntime(t *testing.T) *Runtime {
	t.Helper()
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.NoTunnel = true
	cfg.Mode = "full"
	cfg.Policy = "full"
	cfg.Audit = false
	cfg.Memory.Enabled = false
	runtime, err := NewWorkspaceContextWithReporter(
		context.Background(), "context-budget", t.TempDir(), cfg,
		"test", "pro", "context-budget", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return runtime
}

func longTestText(length int) string {
	out := make([]byte, length)
	for index := range out {
		out[index] = byte('a' + index%26)
	}
	return string(out)
}
