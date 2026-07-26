package server

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExternalStreamableHTTP(t *testing.T) {
	endpoint := os.Getenv("CODEBRIDGE_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set CODEBRIDGE_TEST_ENDPOINT to run the external transport smoke test")
	}
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "codebridge-smoke", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: endpoint, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 76 {
		t.Fatalf("tools/list = %d, want 76", len(tools.Tools))
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ping", Arguments: map[string]any{"message": "smoke"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) == 0 {
		t.Fatalf("ping failed: %#v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "smoke") {
		t.Fatalf("unexpected ping result: %#v", result.Content)
	}
}
