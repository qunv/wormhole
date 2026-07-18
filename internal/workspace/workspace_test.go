package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
