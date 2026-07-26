// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/memory"
)

func TestMemoryForgetRequiresApproval(t *testing.T) {
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = t.TempDir(), true, "balanced"
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	_, err = runtime.Handle(context.Background(), "memory_forget", map[string]any{"memory_id": "mem-1"})
	if err == nil || !strings.Contains(err.Error(), "approval required") {
		t.Fatalf("memory_forget error = %v", err)
	}
}

func TestSelectedMemoryCaptureRedactsWriteContent(t *testing.T) {
	observed := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agentmemory/observe" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode observation: %v", err)
		}
		observed <- body
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = t.TempDir(), true, "full"
	cfg.Memory.Enabled = true
	cfg.Memory.Provider = "agentmemory"
	cfg.Memory.Endpoint = server.URL
	cfg.Memory.CaptureMode = "selected"
	cfg.Memory.TimeoutMS = 1_000
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	if _, err := runtime.HandleSession(context.Background(), "mcp-session-42", "write_file", map[string]any{
		"path": "demo.txt", "content": "super secret payload",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-observed:
		data, _ := body["data"].(map[string]any)
		input, _ := data["tool_input"].(map[string]any)
		content, _ := input["content"].(string)
		if !strings.HasPrefix(content, "[redacted ") {
			t.Fatalf("captured content was not redacted: %#v", body)
		}
		if got := body["sessionId"]; got != "mcp-session-42" {
			t.Fatalf("captured session ID = %#v, want mcp-session-42", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("memory observation was not delivered")
	}
}

func TestPathHashMemoryScopeUsesConfiguredOwningRoot(t *testing.T) {
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Memory.ProjectStrategy = root, true, "path-hash"
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	project, _, err := runtime.memoryScope(map[string]any{"path": "internal/memory/missing.go"})
	if err != nil {
		t.Fatal(err)
	}
	if project != runtime.MemoryProject {
		t.Fatalf("nested path project = %q, want workspace project %q", project, runtime.MemoryProject)
	}
}

func TestDecodeMemoryImportJSONL(t *testing.T) {
	items, err := decodeMemoryImport(map[string]any{
		"jsonl": "{\"id\":\"one\",\"content\":\"first\"}\n{\"id\":\"two\",\"summary\":\"second\"}\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "one" || items[1].Summary != "second" {
		t.Fatalf("unexpected JSONL import: %#v", items)
	}
	if _, err := decodeMemoryImport(map[string]any{"jsonl": "not-json\n"}); err == nil {
		t.Fatal("expected invalid JSONL to fail")
	}
}

func TestMemoryCommitBuildsSessionHandoffFromLocalState(t *testing.T) {
	remembered := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agentmemory/remember" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode remember: %v", err)
		}
		remembered <- body
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "session-memory"})
	}))
	defer server.Close()

	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = t.TempDir(), true, "full"
	cfg.Memory.Enabled = true
	cfg.Memory.Provider = "agentmemory"
	cfg.Memory.Endpoint = server.URL
	cfg.Memory.CaptureMode = "off"
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := runtime.Handle(context.Background(), "task_plan", map[string]any{
		"goal": "stabilize memory", "steps": []any{"scope", "session"},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.HandleSession(context.Background(), "chat-session-9", "memory_commit", map[string]any{
		"include_git": false, "include_review": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored, ok := result.(memory.RememberResult); !ok || !stored.Stored {
		t.Fatalf("unexpected memory commit result: %#v", result)
	}
	select {
	case body := <-remembered:
		content, _ := body["content"].(string)
		if !strings.Contains(content, "stabilize memory") {
			t.Fatalf("commit did not include task state: %#v", body)
		}
		if body["sessionId"] != "chat-session-9" || body["type"] != "session" {
			t.Fatalf("commit session mapping is wrong: %#v", body)
		}
	case <-time.After(time.Second):
		t.Fatal("memory commit was not delivered")
	}
}

type blockingHealthProvider struct {
	sharedTestMemoryProvider
	started chan struct{}
	release chan struct{}
}

func (p *blockingHealthProvider) Health(ctx context.Context) memory.HealthResult {
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
		return memory.HealthResult{Provider: p.Name(), Enabled: true, Available: true}
	case <-ctx.Done():
		return memory.HealthResult{Provider: p.Name(), Enabled: true, Error: ctx.Err().Error()}
	}
}

func TestCachedMemoryHealthDoesNotBlockSnapshotPaths(t *testing.T) {
	provider := &blockingHealthProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	cfg := config.Default()
	cfg.Memory.Enabled = true
	cfg.Memory.HealthCacheMS = 1000
	cfg.Memory.TimeoutMS = 500
	runtime := &Runtime{Config: cfg, Memory: provider, MemoryProject: "project"}

	startedAt := time.Now()
	status := runtime.memoryStatusCached()
	if elapsed := time.Since(startedAt); elapsed > 50*time.Millisecond {
		t.Fatalf("cached memory status blocked for %s", elapsed)
	}
	health, _ := status["health"].(memory.HealthResult)
	if health.Provider != provider.Name() || health.Details["refreshing"] != true {
		t.Fatalf("unexpected initial cached health: %#v", health)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("background health refresh did not start")
	}
	close(provider.release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if refreshed := runtime.memoryHealthCached(); refreshed.Available {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background health refresh did not populate the cache")
}

func TestMemoryProviderAvailabilityHonorsRequiredFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	server.Close()

	newConfig := func(required bool) config.Config {
		cfg := config.Default()
		cfg.Workspace, cfg.NoTunnel = t.TempDir(), true
		cfg.Memory.Enabled = true
		cfg.Memory.Provider = "agentmemory"
		cfg.Memory.Endpoint = server.URL
		cfg.Memory.TimeoutMS = 50
		cfg.Memory.Required = required
		cfg.Memory.CaptureMode = "off"
		return cfg
	}

	t.Run("optional", func(t *testing.T) {
		t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
		runtime, err := New(newConfig(false), "test", "pro", "optional")
		if err != nil {
			t.Fatalf("optional unavailable memory blocked startup: %v", err)
		}
		runtime.Close()
	})

	t.Run("required", func(t *testing.T) {
		t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
		runtime, err := New(newConfig(true), "test", "pro", "required")
		if runtime != nil {
			runtime.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "required memory provider") {
			t.Fatalf("required unavailable memory error = %v", err)
		}
	})
}
