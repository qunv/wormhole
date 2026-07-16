package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"codebridge/internal/agent"
	"codebridge/internal/config"
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
