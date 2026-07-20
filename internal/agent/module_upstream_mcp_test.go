package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codebridge/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func agentUpstreamServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "agent-upstream-test", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{
		Name: "read.data", Title: "Read data", Description: "Read synthetic upstream data.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		_ = json.Unmarshal(request.Params.Arguments, &args)
		value := map[string]any{"query": args["query"], "source": "upstream"}
		raw, _ := json.Marshal(value)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}, StructuredContent: value,
		}, nil
	})
	server.AddTool(&mcp.Tool{
		Name: "write.data", Title: "Write data", Description: "Mutate synthetic upstream data.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"value": map[string]any{"type": "string"}},
		},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "written"}}}, nil
	})
	return server
}

func newAgentUpstreamHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return agentUpstreamServer() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func agentUpstreamConfig(endpoint string) config.MCPServerConfig {
	return config.MCPServerConfig{
		Transport: "streamable-http", URL: endpoint,
		StartupTimeoutMS: 5_000, CallTimeoutMS: 5_000, HealthTimeoutMS: 2_000, MaxTools: 10,
		Policy: config.MCPServerPolicyConfig{
			Default: "approval", ReadOnlyTools: []string{"read.data"},
			AlwaysApproveTools: []string{"write.data"},
		},
	}
}

func TestRuntimeUsesMCPServerNameAsToolNamespace(t *testing.T) {
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	upstream := newAgentUpstreamHTTPServer(t)
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = t.TempDir(), true, "full"
	cfg.MCPServers["postgres_prod"] = agentUpstreamConfig(upstream.URL)
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	if got := runtime.ToolModuleName("postgres_prod__read_data"); got != "mcp_postgres_prod" {
		t.Fatalf("read tool module = %q", got)
	}
	if got, want := len(runtime.Tools()), 76; got != want {
		t.Fatalf("runtime tools = %d, want %d", got, want)
	}
	readSpec, ok := runtime.ToolSpec("postgres_prod__read_data")
	if !ok || !readSpec.ReadOnly || readSpec.Destructive {
		t.Fatalf("unexpected read spec: %#v ok=%t", readSpec, ok)
	}
	writeSpec, ok := runtime.ToolSpec("postgres_prod__write_data")
	if !ok || writeSpec.ReadOnly || !writeSpec.Destructive {
		t.Fatalf("unexpected write spec: %#v ok=%t", writeSpec, ok)
	}

	result, err := runtime.Handle(context.Background(), "postgres_prod__read_data", map[string]any{
		"query": "private-query-value", "undeclared-payload-marker": "ignored",
	})
	if err != nil {
		t.Fatal(err)
	}
	forwarded, ok := result.(*mcp.CallToolResult)
	if !ok || forwarded.IsError || forwarded.StructuredContent == nil {
		t.Fatalf("unexpected forwarded result: %T %#v", result, result)
	}

	audit, err := os.ReadFile(runtime.Store.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(audit), "private-query-value") || strings.Contains(string(audit), "undeclared-payload-marker") {
		t.Fatalf("raw upstream arguments leaked into audit: %s", audit)
	}
	if !strings.Contains(string(audit), `"argument_keys":["query"]`) || !strings.Contains(string(audit), `"undeclared_argument_count":1`) || !strings.Contains(string(audit), `"upstream_tool":"read.data"`) {
		t.Fatalf("upstream audit metadata missing: %s", audit)
	}

	args := map[string]any{"value": "new-value"}
	err = runtime.enforcePolicy("postgres_prod__write_data", args)
	if err == nil || !strings.Contains(err.Error(), genericToolApprovalAction("postgres_prod__write_data", args)) {
		t.Fatalf("always-approval upstream tool was not protected under policy=full: %v", err)
	}

	health := runtime.ModuleHealth(context.Background())
	status, ok := health["mcp_postgres_prod"].(map[string]any)
	if !ok || status["available"] != true || status["registered_tools"] != 2 {
		t.Fatalf("unexpected upstream health: %#v", health["mcp_postgres_prod"])
	}
}

func TestMultipleMCPServersWithSameToolsUseDistinctNamespaces(t *testing.T) {
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	upstream := newAgentUpstreamHTTPServer(t)
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = t.TempDir(), true, "full"
	cfg.MCPServers["postgres_dev"] = agentUpstreamConfig(upstream.URL)
	cfg.MCPServers["postgres_prod"] = agentUpstreamConfig(upstream.URL)
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	if got, want := len(runtime.Tools()), 78; got != want {
		t.Fatalf("runtime tools = %d, want %d", got, want)
	}
	for tool, module := range map[string]string{
		"postgres_dev__read_data":  "mcp_postgres_dev",
		"postgres_prod__read_data": "mcp_postgres_prod",
	} {
		if got := runtime.ToolModuleName(tool); got != module {
			t.Fatalf("module for %s = %q, want %q", tool, got, module)
		}
		if _, err := runtime.Handle(context.Background(), tool, map[string]any{"query": tool}); err != nil {
			t.Fatalf("call %s: %v", tool, err)
		}
	}
}

func TestOptionalAndRequiredUpstreamStartupBehavior(t *testing.T) {
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	base := config.Default()
	base.Workspace, base.NoTunnel = t.TempDir(), true
	missing := config.MCPServerConfig{
		Command:          "codebridge-command-that-does-not-exist",
		StartupTimeoutMS: 500, CallTimeoutMS: 500, HealthTimeoutMS: 500, MaxTools: 10,
		Policy: config.MCPServerPolicyConfig{Default: "approval"},
	}
	base.MCPServers["missing"] = missing
	var startupEvents []string
	runtime, err := NewContextWithReporter(context.Background(), base, "test", "pro", "test-config", func(stage, message string) {
		startupEvents = append(startupEvents, stage+":"+message)
	})
	if err != nil {
		t.Fatalf("optional upstream prevented startup: %v", err)
	}
	if warnings := runtime.StartupWarnings(); len(warnings) != 1 || !strings.Contains(warnings[0], "was skipped") {
		t.Fatalf("optional startup warning = %#v", warnings)
	}
	if joined := strings.Join(startupEvents, "\n"); !strings.Contains(joined, "mcp:connecting missing") || !strings.Contains(joined, "warning:optional upstream MCP server") {
		t.Fatalf("startup reporter did not expose upstream progress: %s", joined)
	}
	runtime.Close()

	missing.Required = true
	base.MCPServers["missing"] = missing
	if runtime, err := New(base, "test", "pro", "test-config"); err == nil {
		runtime.Close()
		t.Fatal("required unavailable upstream did not fail startup")
	}
}

func TestUpstreamToolsAreExcludedFromAutomaticMemoryCapture(t *testing.T) {
	var observations atomic.Int64
	memoryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agentmemory/observe" {
			observations.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer memoryServer.Close()
	upstream := newAgentUpstreamHTTPServer(t)

	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = t.TempDir(), true, "full"
	cfg.Memory.Enabled = true
	cfg.Memory.Provider = "agentmemory"
	cfg.Memory.Endpoint = memoryServer.URL
	cfg.Memory.CaptureMode = "metadata"
	cfg.MCPServers["community"] = agentUpstreamConfig(upstream.URL)
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	if _, err := runtime.Handle(context.Background(), "save_note", map[string]any{"title": "capture", "body": "enabled"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for observations.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if observations.Load() != 1 {
		t.Fatalf("memory recorder did not capture built-in tool; observations=%d", observations.Load())
	}
	if _, err := runtime.Handle(context.Background(), "community__read_data", map[string]any{"query": "do-not-capture"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if observations.Load() != 1 {
		t.Fatalf("upstream MCP tool was captured to memory; observations=%d", observations.Load())
	}
}

func TestUpstreamToolNormalizationAndSchemaValidation(t *testing.T) {
	module := &upstreamMCPModule{
		serverName: "collision", upstream: map[string]string{}, readOnly: map[string]bool{}, policy: map[string]string{},
	}
	cfg := config.MCPServerConfig{Policy: config.MCPServerPolicyConfig{Default: "approval"}}
	tools := []*mcp.Tool{
		{Name: "foo-bar", Description: "one", InputSchema: map[string]any{"type": "object"}},
		{Name: "foo_bar", Description: "two", InputSchema: map[string]any{"type": "object"}},
	}
	if err := module.buildSpecs(cfg, tools); err == nil || !strings.Contains(err.Error(), "same public name") {
		t.Fatalf("normalized collision error = %v", err)
	}

	module = &upstreamMCPModule{
		serverName: "schema", upstream: map[string]string{}, readOnly: map[string]bool{}, policy: map[string]string{},
	}
	tools = []*mcp.Tool{{Name: "bad", Description: "bad", InputSchema: map[string]any{"type": "string"}}}
	if err := module.buildSpecs(cfg, tools); err == nil || !strings.Contains(err.Error(), "type must be object") {
		t.Fatalf("invalid schema error = %v", err)
	}
}
