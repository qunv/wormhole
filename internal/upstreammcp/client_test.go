package upstreammcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wormhole/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStartupWaitTimeoutIncludesCommandCleanup(t *testing.T) {
	stdio := config.MCPServerConfig{Command: "example", StartupTimeoutMS: 3_000}
	if got, want := StartupWaitTimeout(stdio), 18*time.Second; got != want {
		t.Fatalf("StartupWaitTimeout(stdio) = %s, want %s", got, want)
	}
	http := config.MCPServerConfig{URL: "http://127.0.0.1:9000/mcp", StartupTimeoutMS: 3_000}
	if got, want := StartupWaitTimeout(http), 3*time.Second; got != want {
		t.Fatalf("StartupWaitTimeout(http) = %s, want %s", got, want)
	}
}

func testMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "upstream-test", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{
		Name: "echo.read", Title: "Echo read", Description: "Echo arguments and selected environment metadata.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"message": map[string]any{"type": "string"}},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if len(request.Params.Arguments) > 0 {
			if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
				return nil, err
			}
		}
		value := map[string]any{
			"message":               args["message"],
			"explicit_env":          os.Getenv("EXPLICIT_VALUE"),
			"parent_secret_visible": os.Getenv("WORMHOLE_SHOULD_NOT_LEAK") != "",
		}
		raw, _ := json.Marshal(value)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}, StructuredContent: value,
		}, nil
	})
	server.AddTool(&mcp.Tool{
		Name: "mutate", Title: "Mutate", Description: "A synthetic mutation tool.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "mutated"}}}, nil
	})
	return server
}

func TestStdioMCPHelper(t *testing.T) {
	if os.Getenv("WORMHOLE_MCP_HELPER") != "1" {
		return
	}
	session, err := testMCPServer().Connect(context.Background(), &mcp.StdioTransport{}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := session.Wait(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(0)
}

func TestCommandTransportDiscoversCallsAndIsolatesEnvironment(t *testing.T) {
	t.Setenv("WORMHOLE_SHOULD_NOT_LEAK", "parent-secret")
	cfg := config.MCPServerConfig{
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestStdioMCPHelper"},
		Env: map[string]string{
			"WORMHOLE_MCP_HELPER": "1",
			"EXPLICIT_VALUE":      "visible",
		},
		StartupTimeoutMS: 5_000,
		CallTimeoutMS:    5_000,
		HealthTimeoutMS:  2_000,
		MaxTools:         10,
	}
	client, err := New(context.Background(), "stdio-test", cfg, "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if got := len(client.Tools()); got != 2 {
		t.Fatalf("discovered %d tools, want 2", got)
	}
	result, err := client.Call(context.Background(), "echo.read", map[string]any{"message": "hello"}, true)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content type = %T", result.StructuredContent)
	}
	if value["message"] != "hello" || value["explicit_env"] != "visible" {
		t.Fatalf("unexpected upstream result: %#v", value)
	}
	if value["parent_secret_visible"] != false {
		t.Fatalf("parent environment leaked to upstream: %#v", value)
	}
	health := client.Health(context.Background())
	if health["available"] != true || health["process_id"] == nil {
		t.Fatalf("unexpected health: %#v", health)
	}
}

func TestStreamableHTTPTransportForwardsConfiguredHeaders(t *testing.T) {
	var seenHeader string
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return testMCPServer() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.Header.Get("X-Wormhole-Test")
		mcpHandler.ServeHTTP(w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := config.MCPServerConfig{
		Transport:        "streamable-http",
		URL:              server.URL,
		Headers:          map[string]string{"X-Wormhole-Test": "tenant-a"},
		StartupTimeoutMS: 5_000,
		CallTimeoutMS:    5_000,
		HealthTimeoutMS:  2_000,
		MaxTools:         10,
	}
	client, err := New(context.Background(), "http-test", cfg, "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.httpTransport == nil || client.httpTransport.MaxIdleConnsPerHost < client.cfg.MaxConcurrency {
		t.Fatalf("HTTP connection pool was not sized for maxConcurrency=%d: %#v", client.cfg.MaxConcurrency, client.httpTransport)
	}
	result, err := client.Call(context.Background(), "echo.read", map[string]any{"message": "http"}, true)
	if err != nil || result.IsError {
		t.Fatalf("HTTP call failed: err=%v result=%#v", err, result)
	}
	if seenHeader != "tenant-a" {
		t.Fatalf("forwarded header = %q, want tenant-a", seenHeader)
	}
}

func TestStreamableHTTPUsesOperationSpecificTimeouts(t *testing.T) {
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return testMCPServer() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		time.Sleep(40 * time.Millisecond)
		mcpHandler.ServeHTTP(writer, request)
	}))
	defer server.Close()

	cfg := config.MCPServerConfig{
		Transport: "streamable-http", URL: server.URL,
		StartupTimeoutMS: 500, CallTimeoutMS: 20, HealthTimeoutMS: 200,
		HealthCacheMS: 100, FailureCooldownMS: 100, MaxConcurrency: 2, MaxTools: 10,
	}
	client, err := New(context.Background(), "timeout-http", cfg, "test", t.TempDir())
	if err != nil {
		t.Fatalf("startup should use startupTimeoutMs instead of callTimeoutMs: %v", err)
	}
	defer client.Close()

	health := client.Health(context.Background())
	if health["available"] != true {
		t.Fatalf("health should use healthTimeoutMs instead of callTimeoutMs: %#v", health)
	}
	_, err = client.Call(context.Background(), "echo.read", map[string]any{"message": "slow"}, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("tool call error = %v, want callTimeoutMs deadline", err)
	}
}

func TestConcurrentReconnectFailureIsCoalesced(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		http.Error(writer, "upstream unavailable", http.StatusBadRequest)
	}))
	defer server.Close()

	cfg := config.MCPServerConfig{
		Transport: "streamable-http", URL: server.URL,
		StartupTimeoutMS: 2_000, CallTimeoutMS: 500, HealthTimeoutMS: 200,
		HealthCacheMS: 100, FailureCooldownMS: 500, MaxConcurrency: 16, MaxTools: 10,
	}
	cachedTools := []*mcp.Tool{{
		Name: "echo.read", Title: "Echo read",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}}
	client, err := NewDeferred("coalesced-reconnect", cfg, "test", t.TempDir(), cachedTools)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	const callers = 8
	errs := make(chan error, callers)
	go func() { errs <- client.EnsureConnected(context.Background(), true) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("leader reconnect did not reach upstream")
	}
	var followers sync.WaitGroup
	for index := 1; index < callers; index++ {
		followers.Add(1)
		go func() {
			defer followers.Done()
			errs <- client.EnsureConnected(context.Background(), true)
		}()
	}
	deadline := time.Now().Add(time.Second)
	for client.reconnectCoalesced.Load() < callers-1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	followers.Wait()
	for index := 0; index < callers; index++ {
		if reconnectErr := <-errs; reconnectErr == nil {
			t.Fatal("failed reconnect unexpectedly succeeded")
		}
	}
	waveRequests := requests.Load()
	if waveRequests == 0 {
		t.Fatal("reconnect wave did not reach the upstream")
	}
	if got := client.reconnectCoalesced.Load(); got != callers-1 {
		t.Fatalf("coalesced reconnects = %d, want %d", got, callers-1)
	}

	if err := client.EnsureConnected(context.Background(), true); err == nil {
		t.Fatal("later reconnect unexpectedly succeeded")
	}
	if got := requests.Load(); got <= waveRequests {
		t.Fatalf("later reconnect did not get a fresh attempt: before=%d after=%d", waveRequests, got)
	}
}

func TestRefreshCatalogSwapsSessionAndPersistsLiveContract(t *testing.T) {
	t.Setenv("WORMHOLE_DATA_DIR", t.TempDir())
	var exposeExtra atomic.Bool
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server {
			server := testMCPServer()
			if exposeExtra.Load() {
				server.AddTool(&mcp.Tool{
					Name: "new.read", Title: "New read", Description: "A newly discovered read tool.",
					InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
					Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
				}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "new"}}}, nil
				})
			}
			return server
		},
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	server := httptest.NewServer(mcpHandler)
	defer server.Close()
	cfg := config.MCPServerConfig{
		Transport: "streamable-http", URL: server.URL,
		StartupTimeoutMS: 5_000, CallTimeoutMS: 5_000, HealthTimeoutMS: 2_000,
		FailureCooldownMS: 100, MaxConcurrency: 2, MaxTools: 10,
	}
	client, err := New(context.Background(), "refresh-http", cfg, "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	catalogKey := strings.Repeat("d", 32)
	if err := client.SetToolCatalogKey(catalogKey); err != nil {
		t.Fatal(err)
	}
	if len(client.Tools()) != 2 {
		t.Fatalf("initial tool count = %d", len(client.Tools()))
	}
	exposeExtra.Store(true)
	if err := client.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.Tools()) != 3 {
		t.Fatalf("refreshed tool count = %d", len(client.Tools()))
	}
	catalog, err := LoadToolCatalog(catalogKey)
	if err != nil || len(catalog.Tools) != 3 {
		t.Fatalf("persisted refreshed catalog tools=%d err=%v", len(catalog.Tools), err)
	}
	if health := client.Health(context.Background()); health["reconnect_count"].(int) < 1 {
		t.Fatalf("refresh did not record reconnect: %#v", health)
	}
}

func TestDeferredClientConnectsOnFirstCall(t *testing.T) {
	t.Setenv("WORMHOLE_DATA_DIR", t.TempDir())
	var requests atomic.Int64
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return testMCPServer() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		mcpHandler.ServeHTTP(writer, request)
	}))
	defer server.Close()

	cfg := config.MCPServerConfig{
		Transport: "streamable-http", URL: server.URL,
		StartupTimeoutMS: 5_000, CallTimeoutMS: 5_000, HealthTimeoutMS: 2_000,
		HealthCacheMS: 5_000, FailureCooldownMS: 100, MaxConcurrency: 2, MaxTools: 10,
	}
	cachedTools := []*mcp.Tool{{
		Name: "echo.read", Title: "Echo read", Description: "Cached echo contract.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"message": map[string]any{"type": "string"}},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}}
	client, err := NewDeferred("deferred-http", cfg, "test", t.TempDir(), cachedTools)
	if err != nil {
		t.Fatal(err)
	}
	catalogKey := strings.Repeat("c", 32)
	if err := client.SetToolCatalogKey(catalogKey); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if !client.Deferred() || requests.Load() != 0 || len(client.Tools()) != 1 {
		t.Fatalf("deferred client opened transport during construction: deferred=%t requests=%d tools=%d", client.Deferred(), requests.Load(), len(client.Tools()))
	}
	if health := client.Health(context.Background()); health["available"] != false || health["deferred"] != true {
		t.Fatalf("unexpected deferred health: %#v", health)
	}

	result, err := client.Call(context.Background(), "echo.read", map[string]any{"message": "lazy"}, true)
	if err != nil || result.IsError {
		t.Fatalf("deferred first call failed: err=%v result=%#v", err, result)
	}
	if client.Deferred() || requests.Load() == 0 || len(client.Tools()) != 2 {
		t.Fatalf("first call did not discover live contract: deferred=%t requests=%d tools=%d", client.Deferred(), requests.Load(), len(client.Tools()))
	}
	catalog, err := LoadToolCatalog(catalogKey)
	if err != nil || len(catalog.Tools) != 2 {
		t.Fatalf("first call did not refresh persistent catalog: tools=%d err=%v", len(catalog.Tools), err)
	}
}

func TestHealthDiagnosticsDoNotExposeConfiguredSecrets(t *testing.T) {
	sensitiveValue := "fixture-" + strings.Repeat("x", 24)
	t.Setenv("WORMHOLE_UPSTREAM_TEST_SECRET", sensitiveValue)
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return testMCPServer() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	server := httptest.NewServer(mcpHandler)
	defer server.Close()
	cfg := config.MCPServerConfig{
		Transport: "streamable-http", URL: server.URL,
		HeaderRefs:       map[string]string{"Authorization": "WORMHOLE_UPSTREAM_TEST_SECRET"},
		StartupTimeoutMS: 5_000, CallTimeoutMS: 5_000, HealthTimeoutMS: 2_000,
		HealthCacheMS: 5_000, FailureCooldownMS: 100, MaxConcurrency: 2, MaxTools: 10,
	}
	client, err := New(context.Background(), "secret-health", cfg, "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	raw, err := json.Marshal(client.Health(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sensitiveValue) || strings.Contains(string(raw), "Authorization") {
		t.Fatalf("health diagnostics exposed configured secret/header: %s", raw)
	}
}

func TestRemoteHTTPRequiresExplicitOptIn(t *testing.T) {
	cfg := config.MCPServerConfig{URL: "https://example.com/mcp"}
	if _, _, _, err := buildHTTPConfig(cfg); err == nil || !strings.Contains(err.Error(), "allowRemote=true") {
		t.Fatalf("remote endpoint was not rejected: %v", err)
	}
	cfg.AllowRemote = true
	if host, _, _, err := buildHTTPConfig(cfg); err != nil || host != "example.com" {
		t.Fatalf("remote endpoint opt-in failed: host=%q err=%v", host, err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	cfg := config.MCPServerConfig{
		Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=TestStdioMCPHelper"},
		Env:              map[string]string{"WORMHOLE_MCP_HELPER": "1"},
		StartupTimeoutMS: 5_000, CallTimeoutMS: 5_000, HealthTimeoutMS: 2_000, MaxTools: 10,
	}
	client, err := New(context.Background(), "close-test", cfg, "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Call(ctx, "echo.read", nil, true); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("call after close error = %v", err)
	}
}

func TestConcurrentCallsDoNotSerializeOnClientLifecycleLock(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseCalls := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseCalls()

	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server {
			server := mcp.NewServer(&mcp.Implementation{Name: "concurrency-test", Version: "1"}, nil)
			server.AddTool(&mcp.Tool{
				Name: "slow.read", Title: "Slow read",
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				started <- struct{}{}
				select {
				case <-release:
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			return server
		},
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	server := httptest.NewServer(mcpHandler)
	defer server.Close()

	cfg := config.MCPServerConfig{
		Transport: "streamable-http", URL: server.URL,
		StartupTimeoutMS: 5_000, CallTimeoutMS: 5_000, HealthTimeoutMS: 2_000, MaxTools: 10,
	}
	client, err := New(context.Background(), "concurrent-http", cfg, "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, callErr := client.Call(context.Background(), "slow.read", nil, true)
			errs <- callErr
		}()
	}
	for index := 0; index < 2; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			releaseCalls()
			wg.Wait()
			t.Fatalf("only %d concurrent calls reached the server", index)
		}
	}
	releaseCalls()
	wg.Wait()
	close(errs)
	for callErr := range errs {
		if callErr != nil {
			t.Fatalf("concurrent call failed: %v", callErr)
		}
	}
}

func TestClientLimitsConcurrentCalls(t *testing.T) {
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server {
			server := mcp.NewServer(&mcp.Implementation{Name: "limit-test", Version: "1"}, nil)
			server.AddTool(&mcp.Tool{
				Name: "slow.read", Title: "Slow read",
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				current := active.Add(1)
				for {
					observed := maxActive.Load()
					if current <= observed || maxActive.CompareAndSwap(observed, current) {
						break
					}
				}
				defer active.Add(-1)
				started <- struct{}{}
				select {
				case <-release:
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			return server
		},
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	server := httptest.NewServer(mcpHandler)
	defer server.Close()
	cfg := config.MCPServerConfig{
		Transport: "streamable-http", URL: server.URL,
		StartupTimeoutMS: 5_000, CallTimeoutMS: 5_000, HealthTimeoutMS: 2_000,
		HealthCacheMS: 5_000, FailureCooldownMS: 100, MaxConcurrency: 2, MaxTools: 10,
	}
	client, err := New(context.Background(), "limit-http", cfg, "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	errs := make(chan error, 3)
	for range 3 {
		go func() {
			_, callErr := client.Call(context.Background(), "slow.read", nil, true)
			errs <- callErr
		}()
	}
	for index := 0; index < 2; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatalf("only %d calls reached the concurrency window", index)
		}
	}
	select {
	case <-started:
		close(release)
		t.Fatal("third call bypassed maxConcurrency=2")
	case <-time.After(75 * time.Millisecond):
	}
	close(release)
	for range 3 {
		if callErr := <-errs; callErr != nil {
			t.Fatal(callErr)
		}
	}
	if maxActive.Load() != 2 {
		t.Fatalf("server max active = %d, want 2", maxActive.Load())
	}
	status := client.status(true, "")
	if status["max_in_flight_calls"] != int64(2) || status["completed_calls"] != uint64(3) {
		t.Fatalf("unexpected concurrency metrics: %#v", status)
	}
}

func TestHealthUsesSharedCache(t *testing.T) {
	var requests atomic.Int64
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return testMCPServer() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		mcpHandler.ServeHTTP(writer, request)
	}))
	defer server.Close()
	cfg := config.MCPServerConfig{
		Transport: "streamable-http", URL: server.URL,
		StartupTimeoutMS: 5_000, CallTimeoutMS: 5_000, HealthTimeoutMS: 2_000,
		HealthCacheMS: 5_000, FailureCooldownMS: 100, MaxConcurrency: 2, MaxTools: 10,
	}
	client, err := New(context.Background(), "health-cache", cfg, "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	requests.Store(0)
	first := client.Health(context.Background())
	firstRequests := requests.Load()
	second := client.Health(context.Background())
	if first["available"] != true || second["available"] != true || firstRequests == 0 {
		t.Fatalf("unexpected health results: first=%#v second=%#v requests=%d", first, second, firstRequests)
	}
	if requests.Load() != firstRequests || second["health_cached"] != true {
		t.Fatalf("second health call was not cached: firstRequests=%d total=%d result=%#v", firstRequests, requests.Load(), second)
	}
}

func TestHealthCacheRejectsStaleSessionGeneration(t *testing.T) {
	oldState := &clientSessionState{}
	newState := &clientSessionState{}
	client := &Client{
		cfg: effectiveClientConfig(config.MCPServerConfig{}), session: newState,
		stderr: newBoundedCounter(stderrLimit), callSlots: make(chan struct{}, config.DefaultMCPMaxConcurrency),
	}
	entry := healthCacheEntry{checkedAt: time.Now().UTC(), available: true, session: oldState}
	if client.storeHealth(entry, oldState) {
		t.Fatal("stale health generation was stored")
	}
	client.mu.Lock()
	client.healthCache = entry
	client.mu.Unlock()
	if _, ok := client.cachedHealth(); ok {
		t.Fatal("stale health generation was returned from cache")
	}
}

func TestCircuitBreakerIgnoresCallerCancellation(t *testing.T) {
	cfg := effectiveClientConfig(config.MCPServerConfig{FailureCooldownMS: 500, MaxConcurrency: 1})
	client := &Client{name: "cancellation", cfg: cfg, stderr: newBoundedCounter(stderrLimit), callSlots: make(chan struct{}, 1)}
	for range 5 {
		client.setError(context.Canceled)
		client.setError(context.DeadlineExceeded)
	}
	status := client.status(false, "")
	if status["consecutive_failures"] != 0 || status["circuit_open"] != false || status["breaker_trips"] != uint64(0) {
		t.Fatalf("caller cancellation affected circuit state: %#v", status)
	}
}

func TestCircuitBreakerRejectsReconnectDuringCooldown(t *testing.T) {
	cfg := effectiveClientConfig(config.MCPServerConfig{FailureCooldownMS: 500, MaxConcurrency: 1})
	client := &Client{name: "breaker", cfg: cfg, stderr: newBoundedCounter(stderrLimit), callSlots: make(chan struct{}, 1)}
	client.mu.Lock()
	for index := 0; index < circuitFailureThreshold; index++ {
		client.recordFailureLocked(errors.New("transport failure"))
	}
	client.mu.Unlock()
	if _, err := client.Call(context.Background(), "unavailable", nil, true); err == nil || !strings.Contains(err.Error(), "cooling down") {
		t.Fatalf("circuit breaker error = %v", err)
	}
	status := client.status(false, "")
	if status["circuit_open"] != true || status["circuit_rejected"] != uint64(1) {
		t.Fatalf("unexpected circuit metrics: %#v", status)
	}
}

func TestInvalidationDefersCloseUntilBorrowersRelease(t *testing.T) {
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return testMCPServer() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	server := httptest.NewServer(mcpHandler)
	defer server.Close()
	cfg := config.MCPServerConfig{
		Transport: "streamable-http", URL: server.URL,
		StartupTimeoutMS: 5_000, CallTimeoutMS: 5_000, HealthTimeoutMS: 2_000,
		HealthCacheMS: 5_000, FailureCooldownMS: 100, MaxConcurrency: 2, MaxTools: 10,
	}
	client, err := New(context.Background(), "retire-http", cfg, "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	state, err := client.borrowCurrentSession()
	if err != nil {
		t.Fatal(err)
	}
	client.invalidateSession(state, errors.New("synthetic transport failure"))
	if state.session == nil {
		t.Fatal("active borrowed session was closed during invalidation")
	}
	callCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	result, err := state.session.CallTool(callCtx, &mcp.CallToolParams{Name: "echo.read", Arguments: map[string]any{"message": "still-active"}})
	cancel()
	if err != nil || result.IsError {
		t.Fatalf("retired borrowed session stopped early: err=%v result=%#v", err, result)
	}
	client.releaseSession(state)
	if state.session != nil {
		t.Fatal("retired session remained attached after final borrower released")
	}
}

func TestCallDeadlineDoesNotInvalidateSharedSession(t *testing.T) {
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server {
			server := mcp.NewServer(&mcp.Implementation{Name: "deadline-test", Version: "1"}, nil)
			server.AddTool(&mcp.Tool{
				Name: "slow.read", Title: "Slow read",
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				select {
				case <-time.After(100 * time.Millisecond):
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "late"}}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			server.AddTool(&mcp.Tool{
				Name: "fast.read", Title: "Fast read",
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
			})
			return server
		},
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	server := httptest.NewServer(mcpHandler)
	defer server.Close()

	cfg := config.MCPServerConfig{
		Transport: "streamable-http", URL: server.URL,
		StartupTimeoutMS: 5_000, CallTimeoutMS: 5_000, HealthTimeoutMS: 2_000, MaxTools: 10,
	}
	client, err := New(context.Background(), "deadline-http", cfg, "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err = client.Call(ctx, "slow.read", nil, true)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow call error = %v, want deadline exceeded", err)
	}
	result, err := client.Call(context.Background(), "fast.read", nil, false)
	if err != nil || result.IsError {
		t.Fatalf("session was not usable after call deadline: err=%v result=%#v", err, result)
	}
	if reconnects := client.status(true, "")["reconnect_count"]; reconnects != 0 {
		t.Fatalf("call deadline caused reconnect count %v", reconnects)
	}
}
