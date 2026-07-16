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

func TestMCPContractAndFilesystemRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = workspace, true, "full"
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
	if got, want := len(list.Tools), 78; got != want {
		t.Fatalf("tools/list returned %d, want %d", got, want)
	}

	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s protocol error: %v", name, err)
		}
		return result
	}
	if result := call("write_file", map[string]any{"path": "demo.txt", "content": "original\n"}); result.IsError {
		t.Fatalf("write_file failed: %s", resultText(result))
	}
	if result := call("replace_in_file", map[string]any{"path": "demo.txt", "old_text": "original", "new_text": "changed"}); result.IsError {
		t.Fatalf("replace_in_file failed: %s", resultText(result))
	}
	read := call("read_file", map[string]any{"path": "demo.txt"})
	if read.IsError || !strings.Contains(resultText(read), "changed") {
		t.Fatalf("read_file result: %s", resultText(read))
	}
	if result := call("undo_last_patch", map[string]any{}); result.IsError {
		t.Fatalf("undo_last_patch failed: %s", resultText(result))
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "demo.txt"))
	if err != nil || string(raw) != "original\n" {
		t.Fatalf("undo content = %q, err=%v", raw, err)
	}
	escape := call("read_file", map[string]any{"path": "../../../etc/passwd"})
	if !escape.IsError || !strings.Contains(resultText(escape), "outside the allowed roots") {
		t.Fatalf("path escape was not blocked: %s", resultText(escape))
	}
	rootDelete := call("delete_path", map[string]any{"path": ".", "recursive": true})
	if !rootDelete.IsError || !strings.Contains(resultText(rootDelete), "configured root") {
		t.Fatalf("root deletion was not blocked: %s", resultText(rootDelete))
	}
}

func resultText(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	if text, ok := result.Content[0].(*mcp.TextContent); ok {
		return text.Text
	}
	raw, _ := json.Marshal(result.Content[0])
	return string(raw)
}
