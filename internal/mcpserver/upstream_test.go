package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"codebridge/internal/agent"
	"codebridge/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func gatewayUpstreamServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "gateway-upstream", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{
		Name: "echo", Title: "Echo", Description: "Echo through an upstream MCP server.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"message": map[string]any{"type": "string"}},
			"required":   []string{"message"},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
			return nil, err
		}
		value := map[string]any{"message": args["message"], "via": "upstream"}
		raw, _ := json.Marshal(value)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}, StructuredContent: value,
		}, nil
	})
	return server
}

func TestCodebridgeGatewayExposesAndForwardsUpstreamTools(t *testing.T) {
	upstreamHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return gatewayUpstreamServer() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	upstream := httptest.NewServer(upstreamHandler)
	defer upstream.Close()

	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = t.TempDir(), true, "full"
	cfg.MCPServers["echo"] = config.MCPServerConfig{
		Transport: "streamable-http", URL: upstream.URL,
		StartupTimeoutMS: 5_000, CallTimeoutMS: 5_000, HealthTimeoutMS: 2_000, MaxTools: 10,
		Policy: config.MCPServerPolicyConfig{Default: "approval", ReadOnlyTools: []string{"echo"}},
	}
	runtime, err := agent.New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := New(runtime).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "gateway-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(list.Tools), 76; got != want {
		t.Fatalf("tools/list returned %d, want %d", got, want)
	}
	var exposed *mcp.Tool
	for _, tool := range list.Tools {
		if tool.Name == "echo__echo" {
			exposed = tool
			break
		}
	}
	if exposed == nil || exposed.Annotations == nil || !exposed.Annotations.ReadOnlyHint {
		t.Fatalf("upstream tool missing or annotations incorrect: %#v", exposed)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "echo__echo", Arguments: map[string]any{"message": "hello gateway"},
	})
	if err != nil || result.IsError {
		t.Fatalf("gateway call failed: err=%v result=%s", err, resultText(result))
	}
	value, ok := result.StructuredContent.(map[string]any)
	if !ok || value["message"] != "hello gateway" || value["via"] != "upstream" {
		t.Fatalf("unexpected forwarded structured content: %#v", result.StructuredContent)
	}
}

func TestUpstreamModuleParticipatesInToolExposure(t *testing.T) {
	upstreamHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return gatewayUpstreamServer() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	upstream := httptest.NewServer(upstreamHandler)
	defer upstream.Close()

	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel = t.TempDir(), true
	cfg.MCPServers["echo"] = config.MCPServerConfig{
		Transport: "streamable-http", URL: upstream.URL,
		StartupTimeoutMS: 5_000, CallTimeoutMS: 5_000, HealthTimeoutMS: 2_000, MaxTools: 10,
		Policy: config.MCPServerPolicyConfig{Default: "read-only"},
	}
	cfg.Tools.AllowedGroups = []string{"mcp_echo"}
	runtime, err := agent.New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := New(runtime).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "exposure-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "echo__echo" {
		t.Fatalf("allowedGroups did not isolate upstream module: %#v", list.Tools)
	}
}
