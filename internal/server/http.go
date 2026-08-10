// Wormhole
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
	"sort"
	"strconv"
	"strings"
	"time"

	"wormhole/internal/admin"
	"wormhole/internal/agent"
	"wormhole/internal/mcpserver"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type HTTP struct {
	Runtime            *agent.Runtime
	Runtimes           map[string]*agent.Runtime
	SessionRouter      *mcpserver.SessionRouter
	Server             *http.Server
	RemoteIngresses    map[string]*http.Server
	RemoteIngressError error
}

func (h *HTTP) internalHealth(writer http.ResponseWriter, request *http.Request) {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		h.sendJSON(writer, http.StatusForbidden, map[string]any{"error": "loopback_only"})
		return
	}
	value := map[string]any{
		"status": "ok", "version": h.Runtime.Version, "tier": h.Runtime.Tier,
		"pid": os.Getpid(), "mode": h.Runtime.Config.Mode, "policy": h.Runtime.Config.Policy,
		"auth":              ternary(h.Runtime.Config.AuthToken != "", "bearer", "none"),
		"config_id":         h.Runtime.ConfigID,
		"startup_warnings":  h.allStartupWarnings(),
		"workspaces":        h.workspaceSummaries(),
		"runtime":           h.Runtime.RuntimeMetrics(false, 0),
		"remote_ingresses":  h.remoteIngressSummaries(),
		"workspace_runtime": h.workspaceRuntimeMetrics(),
		"shared_resources":  h.Runtime.SharedResourceStats(),
		"session_router":    h.SessionRouter.Stats(),
	}
	if request.URL.Query().Get("deep") == "1" {
		ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
		defer cancel()
		value["modules"] = h.Runtime.ModuleHealth(ctx)
		workspaceModules := map[string]any{}
		for _, id := range h.namedWorkspaceIDs() {
			workspaceModules[id] = h.Runtimes[id].ModuleHealth(ctx)
		}
		value["workspace_modules"] = workspaceModules
	}
	h.sendJSON(writer, http.StatusOK, value)
}

func New(runtime *agent.Runtime) *HTTP {
	return NewMulti(runtime, nil)
}

// NewMulti creates one HTTP daemon with a compatibility endpoint for the
// primary workspace and one fixed endpoint for every named workspace runtime.
func NewMulti(runtime *agent.Runtime, named map[string]*agent.Runtime) *HTTP {
	instance := &HTTP{Runtime: runtime, Runtimes: map[string]*agent.Runtime{}, RemoteIngresses: map[string]*http.Server{}}
	primaryID := strings.ToLower(strings.TrimSpace(runtime.WorkspaceID))
	for id, child := range named {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" || id == primaryID || child == nil {
			continue
		}
		instance.Runtimes[id] = child
	}
	instance.SessionRouter = mcpserver.NewSessionRouter(runtime, instance.Runtimes)

	mux := http.NewServeMux()
	adminHandler := admin.New(runtime, instance.Runtimes, instance.SessionRouter)
	mux.Handle("/admin", adminHandler)
	mux.Handle("/admin/", adminHandler)
	mux.HandleFunc("GET /{$}", instance.root)
	mux.HandleFunc("GET /healthz", instance.health)
	mux.HandleFunc("GET /internal/healthz", instance.internalHealth)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", instance.oauthMetadata)
	mux.Handle(
		mcpserver.SessionEndpoint,
		instance.guardMCPValues(runtime.Config.AuthToken, instance.SessionRouter.BodyLimit(), sessionStreamableHandler(instance.SessionRouter)),
	)
	mux.Handle(
		mcpserver.SessionFastEndpoint,
		instance.guardMCPValues(runtime.Config.AuthToken, instance.SessionRouter.BodyLimit(), sessionStreamableProfileHandler(instance.SessionRouter, mcpserver.ToolProfileFast)),
	)
	mux.Handle("/mcp", instance.guardMCP(runtime, streamableHandler(runtime, primaryID)))
	mux.Handle("/mcp/fast", instance.guardMCP(runtime, streamableProfileHandler(runtime, primaryID, mcpserver.ToolProfileFast)))
	for _, profile := range instance.SessionRouter.Profiles() {
		if profile.ID == "fast" || profile.ID == "full" {
			continue
		}
		mux.Handle(
			mcpserver.SessionProfileEndpoint(profile.ID),
			instance.guardMCPValues(runtime.Config.AuthToken, instance.SessionRouter.BodyLimit(), sessionStreamableDefinitionHandler(instance.SessionRouter, profile)),
		)
		mux.Handle(
			mcpserver.FixedProfileEndpoint(primaryID, profile.ID),
			instance.guardMCP(runtime, streamableDefinitionHandler(runtime, primaryID, profile)),
		)
	}
	for _, id := range instance.namedWorkspaceIDs() {
		endpoint := "/mcp/workspaces/" + id
		child := instance.Runtimes[id]
		mux.Handle(endpoint, instance.guardMCP(child, streamableHandler(child, id)))
		mux.Handle(endpoint+"/fast", instance.guardMCP(child, streamableProfileHandler(child, id, mcpserver.ToolProfileFast)))
		for _, profile := range instance.SessionRouter.Profiles() {
			if profile.ID == "fast" || profile.ID == "full" {
				continue
			}
			mux.Handle(
				mcpserver.FixedProfileEndpoint(id, profile.ID),
				instance.guardMCP(child, streamableDefinitionHandler(child, id, profile)),
			)
		}
	}
	mux.HandleFunc("/mcp/workspaces/", instance.unknownWorkspace)

	instance.Server = &http.Server{
		Addr:              net.JoinHostPort(runtime.Config.Host, strconv.Itoa(runtime.Config.Port)),
		Handler:           instance.originMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	instance.RemoteIngressError = instance.configureRemoteIngresses(primaryID)
	return instance
}

func streamableHandler(runtime *agent.Runtime, workspaceID string) http.Handler {
	return streamableProfileHandler(runtime, workspaceID, mcpserver.ToolProfileFull)
}

func streamableProfileHandler(runtime *agent.Runtime, workspaceID string, profile mcpserver.ToolProfile) http.Handler {
	server := mcpserver.NewWorkspaceProfile(runtime, workspaceID, profile)
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
}

func streamableDefinitionHandler(runtime *agent.Runtime, workspaceID string, profile mcpserver.ProfileDefinition) http.Handler {
	server := mcpserver.NewWorkspaceProfileDefinition(runtime, workspaceID, profile)
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
}

func sessionStreamableHandler(router *mcpserver.SessionRouter) http.Handler {
	return sessionStreamableProfileHandler(router, mcpserver.ToolProfileFull)
}

func sessionStreamableProfileHandler(router *mcpserver.SessionRouter, profile mcpserver.ToolProfile) http.Handler {
	server := mcpserver.NewSessionGatewayProfile(router, profile)
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
}

func sessionStreamableDefinitionHandler(router *mcpserver.SessionRouter, profile mcpserver.ProfileDefinition) http.Handler {
	server := mcpserver.NewSessionGatewayDefinition(router, profile)
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
}

func (h *HTTP) ListenAndServe(ctx context.Context) error {
	if h.RemoteIngressError != nil {
		return h.RemoteIngressError
	}
	type boundServer struct {
		name     string
		server   *http.Server
		listener net.Listener
	}
	bound := make([]boundServer, 0, len(h.RemoteIngresses)+1)
	closeBound := func() {
		for _, item := range bound {
			_ = item.listener.Close()
		}
	}
	for _, name := range sortedHTTPServerNames(h.RemoteIngresses) {
		server := h.RemoteIngresses[name]
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			closeBound()
			return fmt.Errorf("bind remote ingress %q on %s: %w", name, server.Addr, err)
		}
		bound = append(bound, boundServer{name: "remote-ingress:" + name, server: server, listener: listener})
	}
	mainListener, err := net.Listen("tcp", h.Server.Addr)
	if err != nil {
		closeBound()
		return err
	}
	bound = append(bound, boundServer{name: "server", server: h.Server, listener: mainListener})

	errs := make(chan error, len(bound))
	for _, item := range bound {
		item := item
		go func() {
			err := item.server.Serve(item.listener)
			if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
				err = nil
			}
			errs <- err
		}()
	}
	fmt.Printf("Wormhole v%s listening on http://%s\n", h.Runtime.Version, h.Server.Addr)
	fmt.Printf("Mode: %s  Policy: %s  Auth: %s\n", h.Runtime.Config.Mode, h.Runtime.Config.Policy, ternary(h.Runtime.Config.AuthToken != "", "bearer", "none"))
	fmt.Printf("Workspace: %s\nMCP endpoint: http://%s/mcp\nFast MCP endpoint: http://%s/mcp/fast\n", h.Runtime.Workspace.Primary, h.Server.Addr, h.Server.Addr)
	for _, name := range sortedHTTPServerNames(h.RemoteIngresses) {
		fmt.Printf("Remote ingress %s: http://%s/mcp\n", name, h.RemoteIngresses[name].Addr)
	}

	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		for _, item := range bound {
			_ = item.server.Shutdown(shutdownCtx)
		}
	}
	select {
	case <-ctx.Done():
		shutdown()
		return nil
	case err := <-errs:
		shutdown()
		if err == nil {
			return errors.New("HTTP listener exited unexpectedly")
		}
		return err
	}
}

func (h *HTTP) root(writer http.ResponseWriter, _ *http.Request) {
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"status": "ok", "name": "Wormhole", "version": h.Runtime.Version,
		"workspace_count": 1 + len(h.Runtimes), "session_endpoint": mcpserver.SessionEndpoint,
		"session_fast_endpoint": mcpserver.SessionFastEndpoint, "fast_endpoint": "/mcp/fast",
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
		"scopes_supported": []string{}, "resource_name": "Wormhole MCP",
		"resource_documentation": "http://" + h.Server.Addr + "/",
	})
}

func (h *HTTP) unknownWorkspace(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		h.sendJSON(writer, http.StatusMethodNotAllowed, map[string]any{
			"jsonrpc": "2.0", "error": map[string]any{"code": -32000, "message": "Method not allowed."}, "id": nil,
		})
		return
	}
	id := strings.TrimPrefix(request.URL.Path, "/mcp/workspaces/")
	h.sendJSON(writer, http.StatusNotFound, map[string]any{
		"jsonrpc": "2.0", "error": map[string]any{
			"code": -32004, "message": fmt.Sprintf("Unknown or disabled workspace %q.", id),
		}, "id": nil,
	})
}

func (h *HTTP) guardMCP(runtime *agent.Runtime, next http.Handler) http.Handler {
	return h.guardMCPValues(runtime.Config.AuthToken, runtime.Config.MaxBodyBytes, next)
}

func (h *HTTP) guardMCPValues(authToken string, maxBodyBytes int, next http.Handler) http.Handler {
	if maxBodyBytes <= 0 {
		maxBodyBytes = h.Runtime.Config.MaxBodyBytes
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			h.sendJSON(writer, http.StatusMethodNotAllowed, map[string]any{
				"jsonrpc": "2.0", "error": map[string]any{"code": -32000, "message": "Method not allowed."}, "id": nil,
			})
			return
		}
		if authToken != "" {
			header := request.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				h.sendJSON(writer, http.StatusUnauthorized, map[string]any{
					"jsonrpc": "2.0", "error": map[string]any{"code": -32001, "message": "Unauthorized."}, "id": nil,
				})
				return
			}
			token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
			if subtle.ConstantTimeCompare([]byte(token), []byte(authToken)) != 1 {
				h.sendJSON(writer, http.StatusUnauthorized, map[string]any{
					"jsonrpc": "2.0", "error": map[string]any{"code": -32001, "message": "Unauthorized."}, "id": nil,
				})
				return
			}
		}
		if request.ContentLength > int64(maxBodyBytes) {
			h.sendJSON(writer, http.StatusRequestEntityTooLarge, map[string]any{
				"jsonrpc": "2.0", "error": map[string]any{"code": -32002, "message": "Payload too large."}, "id": nil,
			})
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, int64(maxBodyBytes))
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

func (h *HTTP) namedWorkspaceIDs() []string {
	ids := make([]string, 0, len(h.Runtimes))
	for id := range h.Runtimes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (h *HTTP) workspaceSummaries() []map[string]any {
	items := []map[string]any{{
		"id": h.Runtime.WorkspaceID, "endpoint": "/mcp", "root": h.Runtime.Workspace.Primary,
		"fast_endpoint": "/mcp/fast", "memory_project": h.Runtime.MemoryProject,
		"tool_count":      len(h.Runtime.Tools()),
		"fast_tool_count": mcpserver.ProfileToolCount(h.Runtime, mcpserver.ToolProfileFast),
	}}
	for _, id := range h.namedWorkspaceIDs() {
		runtime := h.Runtimes[id]
		items = append(items, map[string]any{
			"id": id, "endpoint": "/mcp/workspaces/" + id, "root": runtime.Workspace.Primary,
			"fast_endpoint":  "/mcp/workspaces/" + id + "/fast",
			"memory_project": runtime.MemoryProject, "tool_count": len(runtime.Tools()),
			"fast_tool_count":  mcpserver.ProfileToolCount(runtime, mcpserver.ToolProfileFast),
			"startup_warnings": runtime.StartupWarnings(),
		})
	}
	return items
}

func (h *HTTP) workspaceRuntimeMetrics() map[string]any {
	metrics := map[string]any{}
	for _, id := range h.namedWorkspaceIDs() {
		metrics[id] = h.Runtimes[id].RuntimeMetrics(false, 0)
	}
	return metrics
}

func (h *HTTP) allStartupWarnings() []string {
	warnings := append([]string(nil), h.Runtime.StartupWarnings()...)
	for _, id := range h.namedWorkspaceIDs() {
		for _, warning := range h.Runtimes[id].StartupWarnings() {
			warnings = append(warnings, "workspace "+id+": "+warning)
		}
	}
	return warnings
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
