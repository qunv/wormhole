// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wormhole/internal/agent"
	"wormhole/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testBearerTransport struct {
	token  string
	origin string
}

func (t testBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	if t.origin != "" {
		clone.Header.Set("Origin", t.origin)
	}
	return http.DefaultTransport.RoundTrip(clone)
}

func TestRemoteIngressIsFixedScopedAndBearerProtected(t *testing.T) {
	defaultRoot, apiRoot := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(apiRoot, "sample.txt"), []byte("api-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMOTE_NOTION_AUTH", "bearer-test-value")

	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = defaultRoot, true, "full"
	cfg.ToolProfiles = map[string]config.ToolProfileConfig{
		"notion-read": {AllowedTools: []string{"read_file"}, OutputMode: "structured"},
	}
	cfg.RemoteIngresses = map[string]config.RemoteIngressConfig{
		"notion": {
			Provider: "cloudflare", WorkspaceID: "api", ToolProfile: "notion-read", LocalPort: 18133,
			PublicURL:    "https://wormhole.example.com/mcp",
			AuthTokenEnv: "REMOTE_NOTION_AUTH", ProviderTokenEnv: "REMOTE_NOTION_TUNNEL", Binary: "cloudflared",
		},
	}
	defaultRuntime, err := agent.NewWorkspaceContextWithReporter(context.Background(), "default", t.TempDir(), cfg, "test", "pro", "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer defaultRuntime.Close()
	apiCfg := cfg
	apiCfg.Workspace = apiRoot
	apiCfg.RemoteIngresses = nil
	apiRuntime, err := agent.NewWorkspaceContextWithReporter(context.Background(), "api", t.TempDir(), apiCfg, "test", "pro", "api", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer apiRuntime.Close()

	instance := NewMulti(defaultRuntime, map[string]*agent.Runtime{"api": apiRuntime})
	if instance.RemoteIngressError != nil {
		t.Fatal(instance.RemoteIngressError)
	}
	remote := instance.RemoteIngresses["notion"]
	if remote == nil || remote.Addr != "127.0.0.1:18133" {
		t.Fatalf("unexpected remote ingress: %#v", remote)
	}

	request := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	response := httptest.NewRecorder()
	remote.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("remote ingress exposed admin route: %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
	response = httptest.NewRecorder()
	remote.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("remote ingress accepted missing bearer: %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+os.Getenv("REMOTE_NOTION_AUTH"))
	request.Header.Set("Origin", "https://evil.example")
	response = httptest.NewRecorder()
	remote.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote ingress accepted invalid Origin: %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer bearer-test-value")
	response = httptest.NewRecorder()
	remote.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("stateless remote ingress GET = %d, want 405 from MCP handler", response.Code)
	}

	httpServer := httptest.NewServer(remote.Handler)
	defer httpServer.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "remote-ingress-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp", DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: testBearerTransport{token: os.Getenv("REMOTE_NOTION_AUTH"), origin: "https://wormhole.example.com"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if initialized := session.InitializeResult(); initialized == nil || initialized.ProtocolVersion != "2026-07-28" {
		t.Fatalf("unexpected negotiated protocol after SDK upgrade: %#v", initialized)
	}
	tools, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "read_file" {
		t.Fatalf("unexpected remote tool contract: %#v", tools.Tools)
	}
	result := callTool(t, context.Background(), session, "read_file", map[string]any{"path": "sample.txt"})
	if !strings.Contains(toolText(result), "Structured result") || result.StructuredContent == nil {
		t.Fatalf("remote read did not use fixed profile/output: %s %#v", toolText(result), result.StructuredContent)
	}
}

func TestOccupiedRemoteIngressPortFailsBeforeMainListenerStarts(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	occupiedPort := occupied.Addr().(*net.TCPAddr).Port

	mainReservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mainPort := mainReservation.Addr().(*net.TCPAddr).Port
	_ = mainReservation.Close()

	t.Setenv("REMOTE_PORT_AUTH", "bearer")
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Port = t.TempDir(), true, mainPort
	cfg.RemoteIngresses = map[string]config.RemoteIngressConfig{
		"notion": {
			Provider: "cloudflare", ToolProfile: "fast", LocalPort: occupiedPort,
			AuthTokenEnv: "REMOTE_PORT_AUTH", ProviderTokenEnv: "REMOTE_PORT_PROVIDER", Binary: "cloudflared",
		},
	}
	runtime, err := agent.NewWorkspaceContextWithReporter(context.Background(), "default", t.TempDir(), cfg, "test", "pro", "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	instance := New(runtime)
	if err := instance.ListenAndServe(context.Background()); err == nil || !strings.Contains(err.Error(), "bind remote ingress") {
		t.Fatalf("unexpected occupied ingress result: %v", err)
	}
	connection, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(mainPort)))
	if err == nil {
		_ = connection.Close()
		t.Fatal("main listener became reachable after remote ingress bind failure")
	}
}

func TestRemoteIngressFailsStartupWithoutBearerOrWorkspace(t *testing.T) {
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel = t.TempDir(), true
	cfg.RemoteIngresses = map[string]config.RemoteIngressConfig{
		"notion": {
			Provider: "cloudflare", WorkspaceID: "missing", ToolProfile: "fast", LocalPort: 18134,
			AuthTokenEnv: "REMOTE_MISSING_AUTH", ProviderTokenEnv: "REMOTE_MISSING_PROVIDER", Binary: "cloudflared",
		},
	}
	runtime, err := agent.NewWorkspaceContextWithReporter(context.Background(), "default", t.TempDir(), cfg, "test", "pro", "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	instance := New(runtime)
	if instance.RemoteIngressError == nil || !strings.Contains(instance.RemoteIngressError.Error(), "unknown or disabled workspace") {
		t.Fatalf("unexpected missing workspace error: %v", instance.RemoteIngressError)
	}
}
