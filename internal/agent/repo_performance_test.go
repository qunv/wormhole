package agent

import (
	"context"
	"path/filepath"
	"testing"

	"wormhole/internal/config"
)

func phase5RepositoryRuntime(b *testing.B) *Runtime {
	b.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	cfg := config.Default()
	cfg.Workspace = root
	cfg.NoTunnel = true
	cfg.Policy = "full"
	cfg.Audit = false
	cfg.Memory.Enabled = false
	runtime, err := NewWorkspaceContextWithReporter(
		context.Background(), "repository-benchmark", b.TempDir(), cfg,
		"benchmark", "pro", "benchmark", nil,
	)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(runtime.Close)
	return runtime
}

func BenchmarkTaskContextWarm(b *testing.B) {
	runtime := phase5RepositoryRuntime(b)
	ctx := context.Background()
	args := map[string]any{
		"query": "memory recorder performance", "detail_level": "compact",
		"token_budget": 4_000, "include_codegraph": false,
		"include_git": false, "include_memory": false,
	}
	value, err := runtime.taskContext(ctx, args)
	if err != nil {
		b.Fatal(err)
	}
	responseBytes := estimatedJSONChars(value)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := runtime.taskContext(ctx, args); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(responseBytes), "response-B")
}

func BenchmarkRepositorySnapshotWarm(b *testing.B) {
	runtime := phase5RepositoryRuntime(b)
	ctx := context.Background()
	if _, err := runtime.workspaceSnapshot(ctx, map[string]any{"include_symbols": false}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := runtime.workspaceSnapshot(ctx, map[string]any{"include_symbols": false}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRepositorySnapshotRefresh(b *testing.B) {
	runtime := phase5RepositoryRuntime(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := runtime.workspaceSnapshot(ctx, map[string]any{
			"include_symbols": false, "refresh": true,
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRepositorySecurityScanGitGrep(b *testing.B) {
	runtime := phase5RepositoryRuntime(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := runtime.patternScan(ctx, ".", 200, "security"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGitStatusWarmCache(b *testing.B) {
	runtime := phase5RepositoryRuntime(b)
	ctx := context.Background()
	if _, err := runtime.gitStatus(ctx, "."); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := runtime.gitStatus(ctx, "."); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRepositorySymbolsWarm(b *testing.B) {
	runtime := phase5RepositoryRuntime(b)
	ctx := context.Background()
	if _, err := runtime.scanSymbols(ctx, runtime.Workspace.Primary, 500, 2_000, "", false); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := runtime.scanSymbols(ctx, runtime.Workspace.Primary, 500, 2_000, "", false); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTaskContextCompactWithoutCodegraph(b *testing.B) {
	runtime := phase5RepositoryRuntime(b)
	ctx := context.Background()
	args := map[string]any{
		"query":        "repository cache performance",
		"detail_level": "compact", "token_budget": 4_000,
		"include_codegraph": false, "include_git": false,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := runtime.taskContext(ctx, args); err != nil {
			b.Fatal(err)
		}
	}
}
