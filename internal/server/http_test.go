package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codebridge/internal/agent"
	"codebridge/internal/config"
	"codebridge/internal/mcpserver"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPGuards(t *testing.T) {
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.AuthToken = t.TempDir(), true, "secret"
	runtime, err := agent.New(cfg, "test", "pro", "id")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	handler := New(runtime).Server.Handler

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d", response.Code)
	}
	var health map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"pid", "workspace", "roots", "config_id", "mode", "policy"} {
		if _, exists := health[private]; exists {
			t.Fatalf("health leaked private field %q", private)
		}
	}

	request = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("Origin", "https://evil.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("origin guard status = %d", response.Code)
	}

	for _, authorization := range []string{"", "secret", "Basic secret"} {
		request = httptest.NewRequest(http.MethodPost, "/mcp", nil)
		request.Header.Set("Authorization", authorization)
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("auth %q status = %d", authorization, response.Code)
		}
	}
}

func TestMultiWorkspaceHTTPRoutesAreIsolated(t *testing.T) {
	defaultRoot, apiRoot := t.TempDir(), t.TempDir()
	defaultRuntime := newWorkspaceRuntime(t, "default", defaultRoot, t.TempDir())
	apiRuntime := newWorkspaceRuntime(t, "api", apiRoot, t.TempDir())

	httpServer := httptest.NewServer(NewMulti(defaultRuntime, map[string]*agent.Runtime{"api": apiRuntime}).Server.Handler)
	defer httpServer.Close()

	ctx := context.Background()
	defaultSession := connectMCP(t, ctx, httpServer.URL+"/mcp")
	defer defaultSession.Close()
	apiSession := connectMCP(t, ctx, httpServer.URL+"/mcp/workspaces/api")
	defer apiSession.Close()

	callTool(t, ctx, defaultSession, "write_file", map[string]any{"path": "same.txt", "content": "default\n"})
	callTool(t, ctx, apiSession, "write_file", map[string]any{"path": "same.txt", "content": "api\n"})

	assertFileContent(t, filepath.Join(defaultRoot, "same.txt"), "default\n")
	assertFileContent(t, filepath.Join(apiRoot, "same.txt"), "api\n")

	defaultInfo := callTool(t, ctx, defaultSession, "workspace_info", map[string]any{})
	apiInfo := callTool(t, ctx, apiSession, "workspace_info", map[string]any{})
	if !strings.Contains(toolText(defaultInfo), `"workspace_id":"default"`) {
		t.Fatalf("default endpoint returned wrong identity: %s", toolText(defaultInfo))
	}
	if !strings.Contains(toolText(apiInfo), `"workspace_id":"api"`) {
		t.Fatalf("named endpoint returned wrong identity: %s", toolText(apiInfo))
	}

	request := httptest.NewRequest(http.MethodPost, "/mcp/workspaces/missing", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	NewMulti(defaultRuntime, map[string]*agent.Runtime{"api": apiRuntime}).Server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "Unknown or disabled workspace") {
		t.Fatalf("unknown workspace response = %d %s", response.Code, response.Body.String())
	}
}

func TestSessionWorkspaceHTTPRoutesChatsByBinding(t *testing.T) {
	defaultRoot, aRoot, bRoot := t.TempDir(), t.TempDir(), t.TempDir()
	defaultRuntime := newWorkspaceRuntime(t, "default", defaultRoot, t.TempDir())
	aRuntime := newWorkspaceRuntime(t, "a", aRoot, t.TempDir())
	bRuntime := newWorkspaceRuntime(t, "b", bRoot, t.TempDir())

	instance := NewMulti(defaultRuntime, map[string]*agent.Runtime{"a": aRuntime, "b": bRuntime})
	httpServer := httptest.NewServer(instance.Server.Handler)
	defer httpServer.Close()

	ctx := context.Background()
	chatA := connectMCP(t, ctx, httpServer.URL+mcpserver.SessionEndpoint)
	defer chatA.Close()
	chatB := connectMCP(t, ctx, httpServer.URL+mcpserver.SessionEndpoint)
	defer chatB.Close()

	selectA := callTool(t, ctx, chatA, "workspace_select", map[string]any{"id": "a"})
	selectB := callTool(t, ctx, chatB, "workspace_select", map[string]any{"id": "b"})
	aBinding, _ := toolObject(t, selectA)["workspace_binding"].(string)
	bBinding, _ := toolObject(t, selectB)["workspace_binding"].(string)
	if aBinding == "" || bBinding == "" || aBinding == bBinding {
		t.Fatalf("unexpected bindings: a=%q b=%q", aBinding, bBinding)
	}

	callTool(t, ctx, chatA, "write_file", map[string]any{
		"workspace_binding": aBinding, "path": "same.txt", "content": "chat-a",
	})
	callTool(t, ctx, chatB, "write_file", map[string]any{
		"workspace_binding": bBinding, "path": "same.txt", "content": "chat-b",
	})
	assertFileContent(t, filepath.Join(aRoot, "same.txt"), "chat-a")
	assertFileContent(t, filepath.Join(bRoot, "same.txt"), "chat-b")
	if _, err := os.Stat(filepath.Join(defaultRoot, "same.txt")); !os.IsNotExist(err) {
		t.Fatalf("session-routed write leaked into default workspace: %v", err)
	}

	aInfo := callTool(t, ctx, chatA, "workspace_info", map[string]any{"workspace_binding": aBinding})
	bInfo := callTool(t, ctx, chatB, "workspace_info", map[string]any{"workspace_binding": bBinding})
	if !strings.Contains(toolText(aInfo), `"workspace_id":"a"`) || !strings.Contains(toolText(bInfo), `"workspace_id":"b"`) {
		t.Fatalf("session router returned wrong identities: a=%s b=%s", toolText(aInfo), toolText(bInfo))
	}

	missing, err := chatA.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !missing.IsError || !strings.Contains(toolText(missing), "workspace_binding is required") {
		t.Fatalf("missing binding was accepted: %s", toolText(missing))
	}

	request := httptest.NewRequest(http.MethodGet, "/internal/healthz", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	instance.Server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("internal health status = %d body=%s", response.Code, response.Body.String())
	}
	var health map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	routerStats, _ := health["session_router"].(map[string]any)
	if routerStats["endpoint"] != mcpserver.SessionEndpoint || routerStats["active_bindings"] != float64(2) {
		t.Fatalf("unexpected session router health: %#v", routerStats)
	}
	if strings.Contains(response.Body.String(), aBinding) || strings.Contains(response.Body.String(), bBinding) {
		t.Fatal("internal health exposed a workspace binding token")
	}
}

func TestNamedEndpointUsesItsRuntimeRequestLimits(t *testing.T) {
	defaultConfig := config.Default()
	defaultConfig.Workspace, defaultConfig.NoTunnel, defaultConfig.Policy = t.TempDir(), true, "full"
	defaultConfig.MaxBodyBytes = 1024
	defaultRuntime, err := agent.NewWorkspaceContextWithReporter(
		context.Background(), "default", t.TempDir(), defaultConfig, "test", "pro", "default", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer defaultRuntime.Close()

	apiConfig := defaultConfig
	apiConfig.Workspace = t.TempDir()
	apiConfig.MaxBodyBytes = 4
	authValue := strings.Repeat("a", 16)
	apiConfig.AuthToken = authValue
	apiRuntime, err := agent.NewWorkspaceContextWithReporter(
		context.Background(), "api", t.TempDir(), apiConfig, "test", "pro", "api", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer apiRuntime.Close()

	handler := NewMulti(defaultRuntime, map[string]*agent.Runtime{"api": apiRuntime}).Server.Handler
	request := httptest.NewRequest(http.MethodPost, "/mcp/workspaces/api", strings.NewReader("12345"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("named endpoint auth status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	request = httptest.NewRequest(http.MethodPost, "/mcp/workspaces/api", strings.NewReader("12345"))
	request.Header.Set("Authorization", "Bearer "+authValue)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("named endpoint body-limit status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}

	request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("12345"))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code == http.StatusUnauthorized || response.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("default endpoint inherited named guards: status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, mcpserver.SessionEndpoint, strings.NewReader("12345"))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("session endpoint body-limit status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestInternalHealthListsWorkspaceEndpoints(t *testing.T) {
	defaultRuntime := newWorkspaceRuntime(t, "default", t.TempDir(), t.TempDir())
	apiRuntime := newWorkspaceRuntime(t, "api", t.TempDir(), t.TempDir())
	handler := NewMulti(defaultRuntime, map[string]*agent.Runtime{"api": apiRuntime}).Server.Handler

	request := httptest.NewRequest(http.MethodGet, "/internal/healthz", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("internal health status = %d body=%s", response.Code, response.Body.String())
	}
	var health map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	items, _ := health["workspaces"].([]any)
	if len(items) != 2 {
		t.Fatalf("workspace summaries = %#v", health["workspaces"])
	}
	raw, _ := json.Marshal(items)
	text := string(raw)
	for _, want := range []string{`"id":"default"`, `"endpoint":"/mcp"`, `"id":"api"`, `"endpoint":"/mcp/workspaces/api"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("health summaries missing %s: %s", want, text)
		}
	}
}

func TestInternalHealthReportsSharedResourceReuse(t *testing.T) {
	shared := agent.NewSharedServices("test")
	t.Cleanup(func() { _ = shared.Close() })
	newSharedRuntime := func(id string) *agent.Runtime {
		cfg := config.Default()
		cfg.Workspace, cfg.NoTunnel, cfg.Policy = t.TempDir(), true, "full"
		runtime, err := agent.NewWorkspaceContextWithSharedServices(
			context.Background(), id, t.TempDir(), cfg, "test", "pro", "config-"+id, shared, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(runtime.Close)
		return runtime
	}
	defaultRuntime := newSharedRuntime("default")
	apiRuntime := newSharedRuntime("api")
	handler := NewMulti(defaultRuntime, map[string]*agent.Runtime{"api": apiRuntime}).Server.Handler

	request := httptest.NewRequest(http.MethodGet, "/internal/healthz", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("internal health status = %d body=%s", response.Code, response.Body.String())
	}
	var health map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	sharedStats, _ := health["shared_resources"].(map[string]any)
	memoryStats, _ := sharedStats["memory"].(map[string]any)
	if memoryStats["providers"] != float64(1) || memoryStats["acquires"] != float64(2) || memoryStats["reuses"] != float64(1) {
		t.Fatalf("unexpected shared memory stats: %#v", memoryStats)
	}
	contractStats, _ := sharedStats["tool_contracts"].(map[string]any)
	if contractStats["built_in_modules"] != float64(6) || contractStats["built_in_tools"] != float64(74) {
		t.Fatalf("unexpected shared built-in contract stats: %#v", contractStats)
	}
}

func newWorkspaceRuntime(t *testing.T, id, root, dataDir string) *agent.Runtime {
	t.Helper()
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = root, true, "full"
	runtime, err := agent.NewWorkspaceContextWithReporter(context.Background(), id, dataDir, cfg, "test", "pro", "config-"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return runtime
}

func connectMCP(t *testing.T, ctx context.Context, endpoint string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "server-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: endpoint, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func callTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s protocol error: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("%s failed: %s", name, toolText(result))
	}
	return result
}

func toolText(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	if text, ok := result.Content[0].(*mcp.TextContent); ok {
		return text.Text
	}
	raw, _ := json.Marshal(result.Content[0])
	return string(raw)
}

func toolObject(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if object, ok := result.StructuredContent.(map[string]any); ok {
		return object
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(toolText(result)), &object); err != nil {
		t.Fatalf("decode tool result: %v result=%s", err, toolText(result))
	}
	return object
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != want {
		t.Fatalf("%s = %q, want %q", path, raw, want)
	}
}
