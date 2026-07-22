package patch

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"codebridge/internal/state"
	"codebridge/internal/workspace"
)

func TestApplyOperationsRollsBackExactBatch(t *testing.T) {
	engine, root := newTestEngine(t)
	path := filepath.Join(root, "config.txt")
	writeTestFile(t, path, "original\n", 0o644)

	result, err := engine.ApplyOperations([]Operation{
		{Op: "update", Path: "config.txt", Edits: []Edit{{OldText: "original", NewText: "changed"}}},
		{Op: "update", Path: "config.txt", Edits: []Edit{{OldText: "missing", NewText: "never"}}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result["rolled_back"] != true || result["ok"] != false {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got := readTestFile(t, path); got != "original\n" {
		t.Fatalf("file after rollback = %q", got)
	}
	if _, err := engine.Undo(); err == nil || !strings.Contains(err.Error(), "no patch history") {
		t.Fatalf("rolled-back batch remained undoable: %v", err)
	}
}

func TestTransactionSerializesMutations(t *testing.T) {
	engine, root := newTestEngine(t)
	first := filepath.Join(root, "first.txt")
	second := filepath.Join(root, "second.txt")
	writeTestFile(t, first, "one", 0o644)
	writeTestFile(t, second, "two", 0o644)

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := engine.Transaction("first", []string{first}, func() error {
			close(firstEntered)
			<-releaseFirst
			return WriteFileAtomic(first, []byte("changed-one"), 0o644)
		}); err != nil {
			t.Errorf("first transaction: %v", err)
		}
	}()
	<-firstEntered
	go func() {
		defer wg.Done()
		if err := engine.Transaction("second", []string{second}, func() error {
			close(secondEntered)
			return WriteFileAtomic(second, []byte("changed-two"), 0o644)
		}); err != nil {
			t.Errorf("second transaction: %v", err)
		}
	}()

	select {
	case <-secondEntered:
		t.Fatal("second transaction entered while first transaction held the engine lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	wg.Wait()
	if got := readTestFile(t, first); got != "changed-one" {
		t.Fatalf("first file = %q", got)
	}
	if got := readTestFile(t, second); got != "changed-two" {
		t.Fatalf("second file = %q", got)
	}
}

func TestTransactionRestoresMutationFailure(t *testing.T) {
	engine, root := newTestEngine(t)
	path := filepath.Join(root, "data.txt")
	writeTestFile(t, path, "before", 0o644)

	err := engine.Transaction("failing", []string{path}, func() error {
		if err := WriteFileAtomic(path, []byte("after"), 0o644); err != nil {
			return err
		}
		return errors.New("injected failure")
	})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("unexpected transaction error: %v", err)
	}
	if got := readTestFile(t, path); got != "before" {
		t.Fatalf("file after rollback = %q", got)
	}
}

func TestWriteFileAtomicPreservesExecutableMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix executable permission bits")
	}
	path := filepath.Join(t.TempDir(), "script.sh")
	writeTestFile(t, path, "#!/bin/sh\nexit 0\n", 0o755)
	if err := WriteFileAtomic(path, []byte("#!/bin/sh\necho ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("mode = %o, want 755", got)
	}
}

func TestApplyDiffUsesHunkCoordinates(t *testing.T) {
	engine, root := newTestEngine(t)
	path := filepath.Join(root, "duplicate.txt")
	writeTestFile(t, path, "same\nx\nsame\ny\n", 0o644)

	diff := strings.Join([]string{
		"--- a/duplicate.txt",
		"+++ b/duplicate.txt",
		"@@ -3,2 +3,2 @@",
		"-same",
		"+changed",
		" y",
		"",
	}, "\n")
	result, err := engine.ApplyDiff(diff, false)
	if err != nil {
		t.Fatal(err)
	}
	if result["ok"] != true {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got := readTestFile(t, path); got != "same\nx\nchanged\ny\n" {
		t.Fatalf("patched content = %q", got)
	}
}

func TestApplyDiffRollbackRestoresRenameDestination(t *testing.T) {
	engine, root := newTestEngine(t)
	writeTestFile(t, filepath.Join(root, "source.txt"), "old\n", 0o755)
	writeTestFile(t, filepath.Join(root, "destination.txt"), "destination-before\n", 0o644)
	writeTestFile(t, filepath.Join(root, "other.txt"), "actual\n", 0o644)

	diff := strings.Join([]string{
		"--- a/source.txt",
		"+++ b/destination.txt",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"--- a/other.txt",
		"+++ b/other.txt",
		"@@ -1 +1 @@",
		"-expected",
		"+changed",
		"",
	}, "\n")
	result, err := engine.ApplyDiff(diff, false)
	if err != nil {
		t.Fatal(err)
	}
	if result["rolled_back"] != true {
		t.Fatalf("patch was not rolled back: %#v", result)
	}
	if got := readTestFile(t, filepath.Join(root, "source.txt")); got != "old\n" {
		t.Fatalf("source after rollback = %q", got)
	}
	if got := readTestFile(t, filepath.Join(root, "destination.txt")); got != "destination-before\n" {
		t.Fatalf("destination after rollback = %q", got)
	}
	if got := readTestFile(t, filepath.Join(root, "other.txt")); got != "actual\n" {
		t.Fatalf("other after rollback = %q", got)
	}
}

func TestBackupFailsClosedForOutsidePath(t *testing.T) {
	engine, _ := newTestEngine(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeTestFile(t, outside, "secret", 0o600)
	if err := engine.Backup("invalid", []string{outside}); err == nil {
		t.Fatal("expected outside path backup to fail")
	}
	if _, err := os.Stat(engine.Store.PatchHistory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history should not be created after failed backup: %v", err)
	}
}

func newTestEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	root := t.TempDir()
	manager, err := workspace.New(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.NewAt(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Workspace: manager, Store: store}, root
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
