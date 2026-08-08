// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package agentmemory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wormhole/internal/memory"
)

func TestProviderRESTMapping(t *testing.T) {
	requests := make(chan map[string]any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/agentmemory/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "healthy", "service": "agentmemory", "version": "test"})
			return
		}
		if r.URL.Path == "/agentmemory/config/flags" {
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "test", "flags": []any{}})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		body["_path"] = r.URL.Path
		requests <- body
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	provider, err := New(Config{Endpoint: server.URL, Secret: "secret", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if health := provider.Health(ctx); !health.Available || health.Provider != "agentmemory" {
		t.Fatalf("health = %#v", health)
	}
	if _, err := provider.Search(ctx, memory.SearchRequest{
		Query: "why streamable HTTP", Project: "git:github.com/acme/wormhole",
		CWD: "/repo", AgentID: "chatgpt", Limit: 7, Format: "compact", TokenBudget: 900,
	}); err != nil {
		t.Fatal(err)
	}
	search := <-requests
	if search["_path"] != "/agentmemory/search" || search["project"] != "git:github.com/acme/wormhole" || search["agentId"] != "chatgpt" {
		t.Fatalf("search request = %#v", search)
	}
	if _, err := provider.Remember(ctx, memory.RememberRequest{
		Content: "Use provider-neutral memory tools", Kind: "decision", Project: "project",
		AgentID: "chatgpt", Concepts: []string{"architecture"}, Files: []string{"internal/memory/provider.go"},
	}); err != nil {
		t.Fatal(err)
	}
	remember := <-requests
	if remember["_path"] != "/agentmemory/remember" || remember["type"] != "decision" {
		t.Fatalf("remember request = %#v", remember)
	}
	if err := provider.Observe(ctx, memory.ObservationRequest{
		HookType: "PostToolUse", SessionID: "session-1", Project: "project", CWD: "/repo",
		Timestamp: "2026-07-18T00:00:00Z", Data: map[string]any{"tool_name": "review_diff"},
	}); err != nil {
		t.Fatal(err)
	}
	observe := <-requests
	if observe["_path"] != "/agentmemory/observe" || observe["sessionId"] != "session-1" {
		t.Fatalf("observe request = %#v", observe)
	}
	if _, err := provider.Forget(ctx, memory.ForgetRequest{MemoryID: "mem-1"}); err != nil {
		t.Fatal(err)
	}
	forget := <-requests
	if forget["_path"] != "/agentmemory/forget" || forget["memoryId"] != "mem-1" {
		t.Fatalf("forget request = %#v", forget)
	}
}

func TestProviderRejectsNonHTTPURL(t *testing.T) {
	if _, err := New(Config{Endpoint: "file:///tmp/memory"}); err == nil {
		t.Fatal("expected endpoint validation error")
	}
}

func TestProviderWorksWithoutSecretAndNormalizesResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("unexpected authorization header: %q", got)
		}
		switch r.URL.Path {
		case "/agentmemory/search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"context": "Use the provider boundary.",
				"results": []any{map[string]any{
					"id": "mem-1", "type": "decision", "content": "Keep MCP tools neutral", "score": 0.9,
				}},
			})
		case "/agentmemory/context":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode context request: %v", err)
			}
			if body["sessionId"] != "session-7" || body["project"] != "project" || body["budget"] != float64(800) {
				t.Errorf("unexpected context request: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"text": "Historical context", "memories": []any{map[string]any{"id": "mem-2", "summary": "Prior decision"}},
			})
		case "/agentmemory/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "healthy"})
		case "/agentmemory/config/flags":
			_ = json.NewEncoder(w).Encode(map[string]any{"flags": []any{
				map[string]any{"key": "GRAPH_EXTRACTION_ENABLED", "enabled": false},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider, err := New(Config{Endpoint: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	search, err := provider.Search(context.Background(), memory.SearchRequest{Query: "architecture", Project: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if search.Context != "Use the provider boundary." || search.Count != 1 || search.Memories[0].ID != "mem-1" {
		t.Fatalf("unexpected normalized search: %#v", search)
	}
	contextResult, err := provider.Context(context.Background(), memory.ContextRequest{Query: "why", Project: "project", SessionID: "session-7", TokenBudget: 800})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(contextResult.Text, "Historical context") || contextResult.Count != 2 {
		t.Fatalf("unexpected normalized context: %#v", contextResult)
	}
	if health := provider.Health(context.Background()); health.Capabilities.KnowledgeGraph {
		t.Fatalf("dynamic capability was not applied: %#v", health)
	}
}

func TestProviderCanonicalExportAndImport(t *testing.T) {
	remembered := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agentmemory/export":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "0.9.27",
				"memories": []any{
					map[string]any{"id": "one", "content": "first", "project": "project", "type": "fact"},
					map[string]any{"id": "two", "content": "second", "project": "other", "type": "decision"},
				},
			})
		case "/agentmemory/remember":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode remember request: %v", err)
			}
			remembered <- body
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "imported"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider, err := New(Config{Endpoint: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	exported, err := provider.Export(context.Background(), memory.ExportRequest{Project: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if exported.SchemaVersion != 1 || exported.Count != 1 || exported.Memories[0].ID != "one" {
		t.Fatalf("unexpected canonical export: %#v", exported)
	}
	imported, err := provider.Import(context.Background(), memory.ImportRequest{
		Project: "project", AgentID: "chatgpt",
		Memories: []memory.Item{{Content: "import me", Kind: "decision"}, {ID: "empty"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 1 || imported.Skipped != 1 {
		t.Fatalf("unexpected import result: %#v", imported)
	}
	request := <-remembered
	if request["project"] != "project" || request["agentId"] != "chatgpt" || request["type"] != "decision" {
		t.Fatalf("unexpected imported remember request: %#v", request)
	}
}

func TestProviderCircuitIgnoresNonTransientHTTPFailures(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.NotFound(w, nil)
	}))
	defer server.Close()

	provider, err := New(Config{
		Endpoint: server.URL, Timeout: time.Second,
		Options: map[string]any{"circuitFailureThreshold": 1, "circuitCooldownMs": 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := provider.Search(context.Background(), memory.SearchRequest{Query: "missing"}); err == nil {
			t.Fatal("expected HTTP 404")
		}
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("non-transient failures opened circuit after %d requests", got)
	}
	if status := provider.circuitStatus(); status["state"] != "closed" || status["trips"] != uint64(0) {
		t.Fatalf("non-transient failure changed circuit state: %#v", status)
	}
}

func TestProviderCircuitBreakerFastFailsAndRecovers(t *testing.T) {
	var requests atomic.Int32
	var failing atomic.Bool
	failing.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if failing.Load() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	provider, err := New(Config{
		Endpoint: server.URL, Timeout: time.Second,
		Options: map[string]any{"circuitFailureThreshold": 2, "circuitCooldownMs": 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := memory.ObservationRequest{HookType: "PostToolUse", SessionID: "session"}
	for attempt := 0; attempt < 2; attempt++ {
		if err := provider.Observe(context.Background(), request); err == nil {
			t.Fatal("expected provider failure")
		}
	}
	if err := provider.Observe(context.Background(), request); err == nil || !strings.Contains(err.Error(), "circuit breaker") {
		t.Fatalf("circuit did not fast-fail: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("network requests while circuit open = %d, want 2", got)
	}

	failing.Store(false)
	time.Sleep(30 * time.Millisecond)
	if err := provider.Observe(context.Background(), request); err != nil {
		t.Fatalf("half-open probe did not recover: %v", err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("network requests after recovery = %d, want 3", got)
	}
	status := provider.circuitStatus()
	if status["state"] != "closed" || status["trips"] != uint64(1) || status["rejected"] != uint64(1) {
		t.Fatalf("unexpected circuit status after recovery: %#v", status)
	}
}
