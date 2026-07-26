package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskContextCombinesSearchAndTargetedReadsWithinBudget(t *testing.T) {
	root := t.TempDir()
	content := `package sample

func OptimizeBridgeLatency() string {
	return "fast path"
}
`
	if err := os.WriteFile(filepath.Join(root, "bridge.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := newIsolatedRuntime(t, "task-context", root, t.TempDir(), "full")

	value, err := runtime.taskContext(context.Background(), map[string]any{
		"query":             "OptimizeBridgeLatency implementation",
		"detail_level":      "compact",
		"token_budget":      2_000,
		"include_codegraph": false,
		"include_git":       false,
		"max_read_files":    4,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if result["kind"] != "task_context" || result["query"] != "OptimizeBridgeLatency implementation" {
		t.Fatalf("unexpected task context identity: %#v", result)
	}
	search := result["search"].(map[string]any)
	if search["count"].(int) < 1 {
		t.Fatalf("task context did not find the requested symbol: %#v", search)
	}
	reads := result["reads"].(map[string]any)
	files := reads["files"].([]map[string]any)
	if len(files) == 0 || files[0]["path"] != "bridge.go" {
		t.Fatalf("task context did not read the matching file: %#v", reads)
	}
	if text, _ := files[0]["content"].(string); !strings.Contains(text, "fast path") {
		t.Fatalf("targeted read omitted relevant source: %q", text)
	}
	if estimated, _ := result["chars_estimated"].(int); estimated <= 0 || estimated > 12_000 {
		t.Fatalf("unexpected estimated output size: %d", estimated)
	}
}

func TestWorkspaceSnapshotDefaultsToCompactCachedMemoryMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module sample\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := newIsolatedRuntime(t, "compact-snapshot", root, t.TempDir(), "full")
	value, err := runtime.workspaceSnapshot(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if result["detail_level"] != "compact" {
		t.Fatalf("snapshot detail level = %#v, want compact", result["detail_level"])
	}
	profile := result["profile"].(map[string]any)
	if _, exists := profile["rootDir"]; exists {
		t.Fatalf("compact snapshot leaked rootDir: %#v", profile)
	}
	if _, exists := result["memory"]; exists {
		t.Fatalf("compact snapshot included memory details without include_memory=true: %#v", result["memory"])
	}
}
