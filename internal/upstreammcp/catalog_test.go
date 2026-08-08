package upstreammcp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func catalogTestTools() []*mcp.Tool {
	return []*mcp.Tool{{
		Name: "read.data", Title: "Read data", Description: "Read cached data.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Meta:        mcp.Meta{"private_marker": "must-not-persist"},
	}}
}

func TestToolCatalogRoundTrip(t *testing.T) {
	t.Setenv("WORMHOLE_DATA_DIR", t.TempDir())
	key := strings.Repeat("a", 32)
	if err := SaveToolCatalog(key, catalogTestTools()); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadToolCatalog(key)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Version != toolCatalogVersion || len(catalog.Tools) != 1 || catalog.Tools[0].Name != "read.data" || catalog.Tools[0].Meta != nil {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
	path, err := ToolCatalogPath(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-not-persist") {
		t.Fatal("catalog persisted arbitrary upstream metadata")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("catalog permissions = %o, want owner-only", info.Mode().Perm())
	}
}

func TestToolCatalogRejectsInvalidAndCorruptContent(t *testing.T) {
	t.Setenv("WORMHOLE_DATA_DIR", t.TempDir())
	if err := SaveToolCatalog("invalid", catalogTestTools()); err == nil {
		t.Fatal("invalid catalog key was accepted")
	}
	key := strings.Repeat("b", 32)
	path, err := ToolCatalogPath(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":99,"tools":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToolCatalog(key); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("unsupported catalog version error = %v", err)
	}
}
