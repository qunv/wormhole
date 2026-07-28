package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitStatusRefreshBypassesConfiguredCache(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	runGitTestCommand(t, root, "init", "-q")
	runtime := newIsolatedRuntime(t, "git-refresh", root, t.TempDir(), "full")
	runtime.Config.GitStatusCacheMS = 5_000
	ctx := context.Background()

	if _, err := runtime.gitStatus(ctx, "."); err != nil {
		t.Fatal(err)
	}
	cached, err := runtime.gitStatus(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	if cached.(map[string]any)["cached"] != true {
		t.Fatalf("second status did not hit configured cache: %#v", cached)
	}
	fresh, err := runtime.gitStatusWithRefresh(ctx, ".", true)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.(map[string]any)["cached"] != false {
		t.Fatalf("refresh did not bypass Git status cache: %#v", fresh)
	}
	stats := runtime.repositoryCacheStats()
	if stats["git_status_ttl_ms"] != int64(5_000) {
		t.Fatalf("Git status TTL stats = %#v, want 5000", stats["git_status_ttl_ms"])
	}
}

func TestSymbolScanUsesLoadedInventoryInsteadOfWalkingAgain(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "first.go"), []byte("package sample\nfunc FirstSymbol() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := newIsolatedRuntime(t, "symbol-inventory", root, t.TempDir(), "full")
	ctx := context.Background()
	inventory, _, err := runtime.loadRepoInventory(ctx, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "second.go"), []byte("package sample\nfunc SecondSymbol() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	symbols, err := runtime.scanSymbolsFromInventory(ctx, inventory, 100, 100, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if hasSymbol(symbols, "SecondSymbol") {
		t.Fatalf("symbol scan walked beyond the supplied inventory: %#v", symbols)
	}
	if !hasSymbol(symbols, "FirstSymbol") {
		t.Fatalf("symbol scan omitted inventory source: %#v", symbols)
	}

	refreshed, err := runtime.scanSymbols(ctx, root, 100, 100, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSymbol(refreshed, "SecondSymbol") {
		t.Fatalf("refreshed inventory omitted new symbol: %#v", refreshed)
	}
}

func TestChangedGoPackageRejectsShellSensitivePaths(t *testing.T) {
	for file, want := range map[string]string{
		"main.go":             ".",
		"internal/api/api.go": "./internal/api",
		"pkg-name/x_test.go":  "./pkg-name",
	} {
		got, ok := changedGoPackage(file)
		if !ok || got != want {
			t.Errorf("changedGoPackage(%q) = %q, %t; want %q, true", file, got, ok, want)
		}
	}
	for _, file := range []string{
		"dir name/file.go", "dir;touch-pwned/file.go", "dir$(touch-pwned)/file.go",
		`"quoted path/file.go"`, "../outside/file.go",
	} {
		if got, ok := changedGoPackage(file); ok {
			t.Errorf("changedGoPackage(%q) = %q, true; want rejection", file, got)
		}
	}
}

func hasSymbol(symbols []map[string]any, name string) bool {
	for _, symbol := range symbols {
		if symbol["name"] == name {
			return true
		}
	}
	return false
}
