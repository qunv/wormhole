package mcpserver

import (
	"context"
	"testing"

	"codebridge/internal/agent"
	"codebridge/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpTestModule struct {
	identities chan agent.CallIdentity
}

func (*mcpTestModule) Name() string { return "custom" }
func (*mcpTestModule) Specs() []agent.ToolSpec {
	return []agent.ToolSpec{{
		Name: "custom_identity", Title: "Custom identity", Description: "Return custom module identity.",
		ReadOnly: true,
		Schema:   map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	}}
}
func (m *mcpTestModule) Handle(_ context.Context, identity agent.CallIdentity, _ string, _ map[string]any) (any, error) {
	m.identities <- identity
	return map[string]any{"session_id": identity.SessionID}, nil
}
func (*mcpTestModule) Health(context.Context) any { return map[string]any{"available": true} }
func (*mcpTestModule) Close() error               { return nil }

func TestMCPRegistersRuntimeModulesAndPropagatesIdentity(t *testing.T) {
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = t.TempDir(), true, "full"
	runtime, err := agent.New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	module := &mcpTestModule{identities: make(chan agent.CallIdentity, 1)}
	if err := runtime.RegisterModule(module); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := New(runtime).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
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
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "custom_identity", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("custom_identity failed: err=%v result=%s", err, resultText(result))
	}
	identity := <-module.identities
	if want := requestSessionID(serverSession); identity.SessionID != want || want == "" {
		t.Fatalf("module identity = %q, want %q", identity.SessionID, want)
	}
}
