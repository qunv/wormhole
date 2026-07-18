// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"codebridge/internal/agent"
	"codebridge/internal/mcpserver"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type HTTP struct {
	Runtime *agent.Runtime
	Server  *http.Server
}

func (h *HTTP) internalHealth(writer http.ResponseWriter, request *http.Request) {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		h.sendJSON(writer, http.StatusForbidden, map[string]any{"error": "loopback_only"})
		return
	}
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"status": "ok", "version": h.Runtime.Version, "tier": h.Runtime.Tier,
		"pid": os.Getpid(), "mode": h.Runtime.Config.Mode, "policy": h.Runtime.Config.Policy,
		"auth":      ternary(h.Runtime.Config.AuthToken != "", "bearer", "none"),
		"config_id": h.Runtime.ConfigID,
	})
}

func New(runtime *agent.Runtime) *HTTP {
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpserver.New(runtime) },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	instance := &HTTP{Runtime: runtime}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", instance.root)
	mux.HandleFunc("GET /healthz", instance.health)
	mux.HandleFunc("GET /internal/healthz", instance.internalHealth)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", instance.oauthMetadata)
	mux.Handle("/mcp", instance.guardMCP(mcpHandler))
	instance.Server = &http.Server{
		Addr:              net.JoinHostPort(runtime.Config.Host, strconv.Itoa(runtime.Config.Port)),
		Handler:           instance.originMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return instance
}

func (h *HTTP) ListenAndServe(ctx context.Context) error {
	errs := make(chan error, 1)
	go func() {
		fmt.Printf("Codebridge v%s listening on http://%s\n", h.Runtime.Version, h.Server.Addr)
		fmt.Printf("Mode: %s  Policy: %s  Auth: %s\n", h.Runtime.Config.Mode, h.Runtime.Config.Policy, ternary(h.Runtime.Config.AuthToken != "", "bearer", "none"))
		fmt.Printf("Workspace: %s\nMCP endpoint: http://%s/mcp\n", h.Runtime.Workspace.Primary, h.Server.Addr)
		errs <- h.Server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = h.Server.Shutdown(shutdownCtx)
		return nil
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (h *HTTP) root(writer http.ResponseWriter, _ *http.Request) {
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"status": "ok", "name": "Codebridge", "version": h.Runtime.Version,
	})
}

func (h *HTTP) health(writer http.ResponseWriter, _ *http.Request) {
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"status": "ok", "version": h.Runtime.Version,
	})
}

func (h *HTTP) oauthMetadata(writer http.ResponseWriter, _ *http.Request) {
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"resource": "http://" + h.Server.Addr + "/mcp", "bearer_methods_supported": []string{"header"},
		"scopes_supported": []string{}, "resource_name": "Codebridge MCP",
		"resource_documentation": "http://" + h.Server.Addr + "/",
	})
}

func (h *HTTP) guardMCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			h.sendJSON(writer, http.StatusMethodNotAllowed, map[string]any{
				"jsonrpc": "2.0", "error": map[string]any{"code": -32000, "message": "Method not allowed."}, "id": nil,
			})
			return
		}
		if h.Runtime.Config.AuthToken != "" {
			header := request.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				h.sendJSON(writer, http.StatusUnauthorized, map[string]any{
					"jsonrpc": "2.0", "error": map[string]any{"code": -32001, "message": "Unauthorized."}, "id": nil,
				})
				return
			}
			token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
			if subtle.ConstantTimeCompare([]byte(token), []byte(h.Runtime.Config.AuthToken)) != 1 {
				h.sendJSON(writer, http.StatusUnauthorized, map[string]any{
					"jsonrpc": "2.0", "error": map[string]any{"code": -32001, "message": "Unauthorized."}, "id": nil,
				})
				return
			}
		}
		if request.ContentLength > int64(h.Runtime.Config.MaxBodyBytes) {
			h.sendJSON(writer, http.StatusRequestEntityTooLarge, map[string]any{
				"jsonrpc": "2.0", "error": map[string]any{"code": -32002, "message": "Payload too large."}, "id": nil,
			})
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, int64(h.Runtime.Config.MaxBodyBytes))
		next.ServeHTTP(writer, request)
	})
}

func (h *HTTP) originMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if h.Runtime.Config.HTTPLog {
			log.Printf("%s %s ua=%s", request.Method, request.URL.Path, request.UserAgent())
		}
		origin := request.Header.Get("Origin")
		if origin != "" && !h.originAllowed(origin) {
			h.sendJSON(writer, http.StatusForbidden, map[string]any{"error": "browser_origin_not_allowed"})
			return
		}
		if origin != "" {
			writer.Header().Set("Access-Control-Allow-Origin", origin)
			writer.Header().Set("Vary", "Origin")
		}
		writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization, Mcp-Session-Id, mcp-session-id")
		writer.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")
		if request.Method == http.MethodOptions {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (h *HTTP) originAllowed(origin string) bool {
	local := map[string]bool{
		"http://" + h.Server.Addr:                                 true,
		fmt.Sprintf("http://127.0.0.1:%d", h.Runtime.Config.Port): true,
		fmt.Sprintf("http://localhost:%d", h.Runtime.Config.Port): true,
		fmt.Sprintf("http://[::1]:%d", h.Runtime.Config.Port):     true,
	}
	if local[origin] {
		return true
	}
	for _, allowed := range h.Runtime.Config.AllowedOrigins {
		if allowed == origin {
			return true
		}
	}
	return false
}

func (h *HTTP) sendJSON(writer http.ResponseWriter, status int, value any) {
	raw, _ := json.Marshal(value)
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	writer.WriteHeader(status)
	_, _ = writer.Write(raw)
}

func ternary[T any](condition bool, yes, no T) T {
	if condition {
		return yes
	}
	return no
}
