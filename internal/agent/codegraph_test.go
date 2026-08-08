package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"wormhole/internal/config"
	"wormhole/internal/processx"
)

func TestCodegraphExploreSkipsUnindexedWorkspace(t *testing.T) {
	runtime := newCodegraphTestRuntime(t)
	value, err := runtime.Handle(context.Background(), "codegraph_explore", map[string]any{"query": "How does Runtime.Handle work?"})
	if err != nil {
		t.Fatal(err)
	}
	if text := value.(string); !strings.Contains(text, "no .codegraph/ directory") {
		t.Fatalf("unexpected fallback: %s", text)
	}
}

func TestCodegraphExploreUsesDirectArguments(t *testing.T) {
	runtime := newCodegraphTestRuntime(t)
	if err := os.Mkdir(filepath.Join(runtime.Workspace.Primary, ".codegraph"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldLookPath, oldCapture := codeGraphLookPath, codeGraphCapture
	t.Cleanup(func() {
		codeGraphLookPath, codeGraphCapture = oldLookPath, oldCapture
	})
	codeGraphLookPath = func(name string) (string, error) {
		if name != "codegraph" {
			t.Fatalf("looked up %q, want codegraph", name)
		}
		return "/fake/codegraph", nil
	}
	var gotName, gotCWD string
	var gotArgs []string
	var gotTimeout time.Duration
	codeGraphCapture = func(_ context.Context, name string, args []string, cwd string, timeout time.Duration) processx.Result {
		gotName, gotArgs, gotCWD, gotTimeout = name, append([]string(nil), args...), cwd, timeout
		return processx.Result{Stdout: "# CodeGraph result\nsource and call paths"}
	}

	query := "Runtime.Handle; rm -rf /"
	value, err := runtime.Handle(context.Background(), "codegraph_explore", map[string]any{
		"query": query, "timeout_ms": 5_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "/fake/codegraph" || gotCWD != runtime.Workspace.Primary || gotTimeout != 5*time.Second {
		t.Fatalf("unexpected invocation: name=%q cwd=%q timeout=%s", gotName, gotCWD, gotTimeout)
	}
	if want := []string{"explore", query}; !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %#v, want %#v", gotArgs, want)
	}
	if text := value.(string); !strings.Contains(text, "source and call paths") {
		t.Fatalf("unexpected output: %s", text)
	}
}

func newCodegraphTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	workspace := t.TempDir()
	t.Setenv("WORMHOLE_DATA_DIR", t.TempDir())
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = workspace, true, "full"
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return runtime
}
