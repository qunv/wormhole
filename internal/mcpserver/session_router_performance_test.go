package mcpserver

import (
	"fmt"
	"testing"
	"time"

	"wormhole/internal/agent"
)

func BenchmarkSessionRouterResolve10000Bindings(b *testing.B) {
	now := time.Unix(1_700_000_000, 0).UTC()
	runtime := &agent.Runtime{WorkspaceID: "benchmark"}
	router := &SessionRouter{
		primaryID: "benchmark", ttl: 24 * time.Hour,
		cleanupInterval: time.Minute, maxBindings: 10_001,
		now:      func() time.Time { return now },
		runtimes: map[string]*agent.Runtime{"benchmark": runtime},
		sessions: map[string]string{}, bindingSessions: map[string]map[string]struct{}{},
		bindings: make(map[string]workspaceBinding, 10_000), nextCleanup: now.Add(time.Hour),
	}
	selected := ""
	for index := 0; index < 10_000; index++ {
		token := fmt.Sprintf("token-%08d", index)
		router.bindings[token] = workspaceBinding{
			Token: token, WorkspaceID: "benchmark", CreatedAt: now, LastUsedAt: now,
		}
		if index == 5_000 {
			selected = token
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, _, err := router.resolve("", selected); err != nil {
			b.Fatal(err)
		}
	}
}
