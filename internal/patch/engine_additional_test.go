package patch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestApplyDiffAcceptsGitMetadataBetweenFiles(t *testing.T) {
	engine, root := newTestEngine(t)
	writeTestFile(t, filepath.Join(root, "one.txt"), "one\n", 0o644)
	writeTestFile(t, filepath.Join(root, "two.txt"), "two\n", 0o644)
	diff := strings.Join([]string{
		"diff --git a/one.txt b/one.txt",
		"index 1111111..2222222 100644",
		"--- a/one.txt",
		"+++ b/one.txt",
		"@@ -1 +1 @@",
		"-one",
		"+ONE",
		"diff --git a/two.txt b/two.txt",
		"index 3333333..4444444 100644",
		"--- a/two.txt",
		"+++ b/two.txt",
		"@@ -1 +1 @@",
		"-two",
		"+TWO",
		"",
	}, "\n")
	result, err := engine.ApplyDiff(diff, false)
	if err != nil {
		t.Fatal(err)
	}
	if result["ok"] != true {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got := readTestFile(t, filepath.Join(root, "one.txt")); got != "ONE\n" {
		t.Fatalf("one.txt = %q", got)
	}
	if got := readTestFile(t, filepath.Join(root, "two.txt")); got != "TWO\n" {
		t.Fatalf("two.txt = %q", got)
	}
}

func TestApplyDiffRenamePreservesSourceMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix executable permission bits")
	}
	engine, root := newTestEngine(t)
	source := filepath.Join(root, "tool.sh")
	destination := filepath.Join(root, "renamed.sh")
	writeTestFile(t, source, "old\n", 0o755)
	diff := strings.Join([]string{
		"--- a/tool.sh",
		"+++ b/renamed.sh",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"",
	}, "\n")
	result, err := engine.ApplyDiff(diff, false)
	if err != nil {
		t.Fatal(err)
	}
	if result["ok"] != true {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source should be removed: %v", err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("destination mode = %o, want 755", got)
	}
}

func TestApplyOperationsContextCancellationDoesNotMutate(t *testing.T) {
	engine, root := newTestEngine(t)
	path := filepath.Join(root, "cancel.txt")
	writeTestFile(t, path, "before\n", 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := engine.ApplyOperationsContext(ctx, []Operation{{
		Op: "update", Path: "cancel.txt", Edits: []Edit{{OldText: "before", NewText: "after"}},
	}}, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled patch error = %v, want context.Canceled", err)
	}
	if got := readTestFile(t, path); got != "before\n" {
		t.Fatalf("canceled patch mutated file: %q", got)
	}
}
