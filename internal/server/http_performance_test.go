package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codebridge/internal/agent"
	"codebridge/internal/config"
	"codebridge/internal/mcpserver"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const benchmarkToolsListBody = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

func BenchmarkCachedMCPToolsList(b *testing.B) {
	runtime := benchmarkHTTPRuntime(b)
	handler := streamableHandler(runtime, "default")
	benchmarkMCPToolsList(b, handler)
}

func BenchmarkRebuiltMCPToolsListBaseline(b *testing.B) {
	runtime := benchmarkHTTPRuntime(b)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpserver.NewWorkspace(runtime, "default") },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	benchmarkMCPToolsList(b, handler)
}

func benchmarkHTTPRuntime(b *testing.B) *agent.Runtime {
	b.Helper()
	cfg := config.Default()
	cfg.Workspace = b.TempDir()
	cfg.NoTunnel = true
	cfg.Policy = "full"
	cfg.Audit = false
	cfg.Memory.Enabled = false
	runtime, err := agent.New(cfg, "benchmark", "pro", "benchmark")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(runtime.Close)
	return runtime
}

func benchmarkMCPToolsList(b *testing.B, handler http.Handler) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(benchmarkToolsListBody))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			b.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
		}
	}
}
