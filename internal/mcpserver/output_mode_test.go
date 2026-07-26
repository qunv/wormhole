package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codebridge/internal/agent"
	"codebridge/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDefaultOutputModePreservesJSONTextCompatibility(t *testing.T) {
	result := toolSuccessWithMode(
		map[string]any{"kind": "compatibility", "count": 2},
		agent.ToolOutputBoth,
	)
	text := resultText(result)
	if !strings.Contains(text, `"kind":"compatibility"`) || result.StructuredContent == nil {
		t.Fatalf("full profile compatibility output changed: text=%q structured=%#v", text, result.StructuredContent)
	}
}

func TestStructuredOutputModeAvoidsDuplicatingLargeJSONText(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = workspace, true, "full"
	cfg.Audit = false
	cfg.Memory.Enabled = false
	runtime, err := agent.New(cfg, "test", "pro", "output-mode")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := NewWorkspaceProfile(runtime, "default", ToolProfileFast).Connect(ctx, serverTransport, nil)
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

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "workspace_snapshot", Arguments: map[string]any{"detail_level": "compact"},
	})
	if err != nil || result.IsError {
		t.Fatalf("workspace_snapshot failed: err=%v result=%s", err, resultText(result))
	}
	object, ok := result.StructuredContent.(map[string]any)
	if !ok || object["kind"] != "workspace_snapshot" {
		t.Fatalf("structured content missing: %#v", result.StructuredContent)
	}
	text := resultText(result)
	if !strings.HasPrefix(text, "Structured result") || strings.Contains(text, `"tree"`) {
		t.Fatalf("structured result duplicated JSON text: %q", text)
	}
	raw, _ := json.Marshal(object)
	if len(text)*4 >= len(raw) {
		t.Fatalf("summary text is not materially smaller: text=%d structured=%d", len(text), len(raw))
	}

	read, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_file", Arguments: map[string]any{"path": filepath.Base("main.go")},
	})
	if err != nil || read.IsError || !strings.Contains(resultText(read), `"content"`) {
		t.Fatalf("default output compatibility changed: err=%v result=%s", err, resultText(read))
	}
}
