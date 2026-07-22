package agent

import (
	"context"
	"path/filepath"
	"testing"

	"codebridge/internal/config"
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
