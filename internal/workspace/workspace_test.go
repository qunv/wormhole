package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestResolveBlocksTraversalAndSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	manager, err := New(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve(filepath.Join("..", filepath.Base(outside))); err == nil {
		t.Fatal("expected traversal outside root to fail")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges")
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	_, err = manager.Resolve(filepath.Join("escape", "secret.txt"))
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected symlink escape error, got %v", err)
	}
}

func TestIsRootRecognizesCanonicalSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges")
	}
	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	manager, err := New(linkRoot, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := manager.Resolve(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !manager.IsRoot(resolved) {
		t.Fatalf("canonical root %q was not recognized", resolved)
	}
}

func TestNewRejectsMissingOrNonDirectoryRootsWithoutCreatingThem(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := New(missing, nil, nil); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing primary root was accepted: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing primary root was created: %v", err)
	}

	primary := t.TempDir()
	missingExtra := filepath.Join(t.TempDir(), "missing-extra")
	if _, err := New(primary, []string{missingExtra}, nil); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing extra root was accepted: %v", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(file, nil, nil); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file primary root was accepted: %v", err)
	}
}

func TestTreeContextPropagatesCancellation(t *testing.T) {
	manager, err := New(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := manager.TreeContext(ctx, ".", 3, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("TreeContext cancellation = %v, want context.Canceled", err)
	}
}

func TestOwningRootUsesMostSpecificConfiguredRoot(t *testing.T) {
	primary := t.TempDir()
	nested := filepath.Join(primary, "packages", "api")
	if err := os.MkdirAll(filepath.Join(nested, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager, err := New(primary, []string{nested}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := manager.OwningRoot(filepath.Join(nested, "internal", "missing.go"))
	if err != nil {
		t.Fatal(err)
	}
	if got != nested {
		t.Fatalf("owning root = %q, want %q", got, nested)
	}
}

func TestRipgrepPathsAndMatchesStopAtGlobalLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell helper")
	}
	root := t.TempDir()
	manager, err := New(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "fake-rg")
	content := `#!/bin/sh
case " $* " in
  *" --files "*)
    i=1
    while [ "$i" -le 100000 ]; do echo "file-$i.go"; i=$((i+1)); done
    ;;
  *)
    i=1
    while [ "$i" -le 100000 ]; do echo "file.go:$i:match-$i"; i=$((i+1)); done
    ;;
esac
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	manager.RGBin = script

	started := time.Now()
	files, engine, err := manager.FindFiles(".", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if engine != "ripgrep" || len(files) != 5 {
		t.Fatalf("engine=%q files=%d, want ripgrep/5", engine, len(files))
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("bounded file search took %s", elapsed)
	}

	started = time.Now()
	matches, engine, err := manager.Search(".", "match", false, "", 0, 7)
	if err != nil {
		t.Fatal(err)
	}
	if engine != "ripgrep" || len(matches) != 7 {
		t.Fatalf("engine=%q matches=%d, want ripgrep/7", engine, len(matches))
	}
	for index, match := range matches {
		if match.Line != index+1 || match.Text != "match-"+strconv.Itoa(index+1) {
			t.Fatalf("match[%d] = %#v", index, match)
		}
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("bounded text search took %s", elapsed)
	}
}
