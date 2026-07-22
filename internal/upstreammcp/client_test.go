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
	"testing"
	"time"

	"codebridge/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
			"parent_secret_visible": os.Getenv("CODEBRIDGE_SHOULD_NOT_LEAK") != "",
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
	if os.Getenv("CODEBRIDGE_MCP_HELPER") != "1" {
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
	t.Setenv("CODEBRIDGE_SHOULD_NOT_LEAK", "parent-secret")
	cfg := config.MCPServerConfig{
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestStdioMCPHelper"},
		Env: map[string]string{
			"CODEBRIDGE_MCP_HELPER": "1",
			"EXPLICIT_VALUE":        "visible",
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
		seenHeader = r.Header.Get("X-Codebridge-Test")
		mcpHandler.ServeHTTP(w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := config.MCPServerConfig{
		Transport:        "streamable-http",
		URL:              server.URL,
		Headers:          map[string]string{"X-Codebridge-Test": "tenant-a"},
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
	result, err := client.Call(context.Background(), "echo.read", map[string]any{"message": "http"}, true)
	if err != nil || result.IsError {
		t.Fatalf("HTTP call failed: err=%v result=%#v", err, result)
	}
	if seenHeader != "tenant-a" {
		t.Fatalf("forwarded header = %q, want tenant-a", seenHeader)
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
		Env:              map[string]string{"CODEBRIDGE_MCP_HELPER": "1"},
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
	result, err := client.Call(context.Background(), "fast.read", nil, true)
	if err != nil || result.IsError {
		t.Fatalf("session was not usable after call deadline: err=%v result=%#v", err, result)
	}
	if reconnects := client.status(true, "")["reconnect_count"]; reconnects != 0 {
		t.Fatalf("call deadline caused reconnect count %v", reconnects)
	}
}
