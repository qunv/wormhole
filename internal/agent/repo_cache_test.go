package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRepoIndexCacheIncludesDepthLimitAndSymbols(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := newIsolatedRuntime(t, "cache", root, t.TempDir(), "full")

	shallow, cached, err := runtime.buildIndex(context.Background(), root, 1, 100, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Fatal("first index build unexpectedly hit cache")
	}

	deep, cached, err := runtime.buildIndex(context.Background(), root, 3, 100, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Fatal("depth change incorrectly reused cache")
	}
	if len(deep.Tree) <= len(shallow.Tree) {
		t.Fatalf("deep tree entries=%d, shallow=%d", len(deep.Tree), len(shallow.Tree))
	}

	_, cached, err = runtime.buildIndex(context.Background(), root, 3, 100, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cached {
		t.Fatal("identical index request did not hit cache")
	}

	_, cached, err = runtime.buildIndex(context.Background(), root, 3, 2, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Fatal("limit change incorrectly reused cache")
	}

	withSymbols, cached, err := runtime.buildIndex(context.Background(), root, 3, 100, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if cached || !withSymbols.SymbolsIncluded {
		t.Fatalf("symbols request cached=%v included=%v", cached, withSymbols.SymbolsIncluded)
	}

	withoutSymbols, cached, err := runtime.buildIndex(context.Background(), root, 3, 100, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cached || !withoutSymbols.SymbolsIncluded {
		t.Fatalf("symbol-rich cache was not reused for a subset request: cached=%v included=%v", cached, withoutSymbols.SymbolsIncluded)
	}
}

func TestRepositoryRefreshAdvancesGenerationAndDropsDerivedViews(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "before.go"), []byte("package sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := newIsolatedRuntime(t, "refresh-generation", root, t.TempDir(), "full")
	ctx := context.Background()
	first, _, err := runtime.buildIndex(ctx, root, 1, 100, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.buildIndex(ctx, root, 3, 100, true, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "after.go"), []byte("package sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refreshed, cached, err := runtime.buildIndex(ctx, root, 1, 100, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if cached || refreshed.Generation <= first.Generation {
		t.Fatalf("refresh generation = %d, first = %d, cached=%t", refreshed.Generation, first.Generation, cached)
	}
	runtime.repoCacheMu.Lock()
	views := len(runtime.repoViews)
	runtime.repoCacheMu.Unlock()
	if views > 1 {
		t.Fatalf("refresh retained stale derived views: %d", views)
	}
	found := false
	for _, path := range refreshed.Tree {
		if path == "after.go" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("refreshed tree omitted externally added file: %#v", refreshed.Tree)
	}
}

func TestRepositoryCacheInvalidatesAfterFilesystemMutation(t *testing.T) {
	root := t.TempDir()
	runtime := newIsolatedRuntime(t, "invalidate", root, t.TempDir(), "full")
	ctx := context.Background()
	before, _, err := runtime.buildIndex(ctx, root, 3, 100, false, false)
	if err != nil {
		t.Fatal(err)
	}
	generation := before.Generation
	if _, err := runtime.Handle(ctx, "write_file", map[string]any{"path": "new.go", "content": "package sample\n"}); err != nil {
		t.Fatal(err)
	}
	after, cached, err := runtime.buildIndex(ctx, root, 3, 100, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if cached || after.Generation <= generation {
		t.Fatalf("mutation did not invalidate repository cache: before=%d after=%d cached=%t", generation, after.Generation, cached)
	}
	found := false
	for _, path := range after.Tree {
		if path == "new.go" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("rebuilt tree omitted new.go: %#v", after.Tree)
	}
}

func TestGitStatusUsesShortCacheAndMutationInvalidatesIt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	runGitTestCommand(t, root, "init", "-q")
	runtime := newIsolatedRuntime(t, "git-cache", root, t.TempDir(), "full")
	ctx := context.Background()
	first, err := runtime.gitStatus(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["cached"] != false {
		t.Fatalf("first status unexpectedly cached: %#v", first)
	}
	second, err := runtime.gitStatus(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	if second.(map[string]any)["cached"] != true {
		t.Fatalf("second status did not hit cache: %#v", second)
	}
	if _, err := runtime.Handle(ctx, "write_file", map[string]any{"path": "changed.txt", "content": "changed\n"}); err != nil {
		t.Fatal(err)
	}
	third, err := runtime.gitStatus(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	if third.(map[string]any)["cached"] != false || third.(map[string]any)["count"] != 1 {
		t.Fatalf("mutation did not invalidate Git cache: %#v", third)
	}
}

func TestPatternScanUsesTrackedFilesAndHonorsCancellation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	runGitTestCommand(t, root, "init", "-q")
	marker := "TO" + "DO"
	if err := os.WriteFile(filepath.Join(root, "tracked.go"), []byte("package sample\n// "+marker+" tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.go"), []byte("package sample\n// "+marker+" untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, root, "add", "tracked.go")
	runtime := newIsolatedRuntime(t, "scan", root, t.TempDir(), "full")
	value, err := runtime.patternScan(context.Background(), ".", 20, markerScanKind)
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if result["engine"] != "git-grep" || result["count"] != 1 {
		t.Fatalf("unexpected tracked scan result: %#v", result)
	}
	findings := result["findings"].([]map[string]any)
	if findings[0]["path"] != "tracked.go" {
		t.Fatalf("untracked file leaked into scan: %#v", findings)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.scanSymbols(ctx, root, 100, 100, "", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled symbol scan error = %v, want context.Canceled", err)
	}
}

func TestRepositoryCachesRemainCardinalityBounded(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 20; index++ {
		directory := filepath.Join(root, "dir", fmt.Sprintf("%02d", index))
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "file.go"), []byte("package sample\nfunc Value() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runtime := newIsolatedRuntime(t, "bounded-cache", root, t.TempDir(), "full")
	ctx := context.Background()
	for index := 0; index < 20; index++ {
		directory := filepath.Join(root, "dir", fmt.Sprintf("%02d", index))
		if _, _, err := runtime.loadRepoInventory(ctx, directory, false); err != nil {
			t.Fatal(err)
		}
	}
	runtime.repoCacheMu.Lock()
	inventoryCount := len(runtime.repoInventories)
	runtime.repoCacheMu.Unlock()
	if inventoryCount > repoInventoryCacheLimit {
		t.Fatalf("inventory cache size = %d, limit %d", inventoryCount, repoInventoryCacheLimit)
	}
	for index := 1; index <= 24; index++ {
		if _, err := runtime.scanSymbols(ctx, root, index, 100, "", false); err != nil {
			t.Fatal(err)
		}
	}
	runtime.repoCacheMu.Lock()
	symbolCount := len(runtime.repoSymbols)
	runtime.repoCacheMu.Unlock()
	if symbolCount > repoSymbolCacheLimit {
		t.Fatalf("symbol cache size = %d, limit %d", symbolCount, repoSymbolCacheLimit)
	}
}

func runGitTestCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
