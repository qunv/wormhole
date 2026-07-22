package agent

import (
	"os"
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

	shallow, cached, err := runtime.buildIndex(root, 1, 100, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Fatal("first index build unexpectedly hit cache")
	}

	deep, cached, err := runtime.buildIndex(root, 3, 100, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Fatal("depth change incorrectly reused cache")
	}
	if len(deep.Tree) <= len(shallow.Tree) {
		t.Fatalf("deep tree entries=%d, shallow=%d", len(deep.Tree), len(shallow.Tree))
	}

	_, cached, err = runtime.buildIndex(root, 3, 100, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cached {
		t.Fatal("identical index request did not hit cache")
	}

	_, cached, err = runtime.buildIndex(root, 3, 2, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Fatal("limit change incorrectly reused cache")
	}

	withSymbols, cached, err := runtime.buildIndex(root, 3, 100, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if cached || !withSymbols.SymbolsIncluded {
		t.Fatalf("symbols request cached=%v included=%v", cached, withSymbols.SymbolsIncluded)
	}

	withoutSymbols, cached, err := runtime.buildIndex(root, 3, 100, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cached || !withoutSymbols.SymbolsIncluded {
		t.Fatalf("symbol-rich cache was not reused for a subset request: cached=%v included=%v", cached, withoutSymbols.SymbolsIncluded)
	}
}
