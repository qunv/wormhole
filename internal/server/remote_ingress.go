// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"wormhole/internal/agent"
	"wormhole/internal/config"
	"wormhole/internal/mcpserver"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// configureRemoteIngresses creates loopback-only HTTP listeners that expose
// exactly one fixed workspace/profile contract. They intentionally do not
// mount Admin, session routing, workspace switching, or the daemon health API.
func (h *HTTP) configureRemoteIngresses(primaryID string) error {
	for _, ingress := range h.Runtime.Config.EnabledRemoteIngresses() {
		runtime, workspaceID, err := h.remoteIngressRuntime(primaryID, ingress)
		if err != nil {
			return err
		}
		profile, ok := mcpserver.ResolveProfile(runtime.Config, ingress.Config.ToolProfile)
		if !ok {
			return fmt.Errorf("remote ingress %q references unavailable tool profile %q", ingress.Name, ingress.Config.ToolProfile)
		}
		authToken := strings.TrimSpace(os.Getenv(ingress.Config.AuthTokenEnv))
		if authToken == "" {
			return fmt.Errorf("remote ingress %q requires MCP bearer token in %s", ingress.Name, ingress.Config.AuthTokenEnv)
		}
		mcpServer := mcpserver.NewWorkspaceProfileDefinition(runtime, workspaceID, profile)
		mcpHandler := mcp.NewStreamableHTTPHandler(
			func(*http.Request) *mcp.Server { return mcpServer },
			&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
		)
		mux := http.NewServeMux()
		guarded := h.guardMCPMethods(authToken, runtime.Config.MaxBodyBytes, true, mcpHandler)
		mux.Handle("/mcp", h.remoteIngressOriginMiddleware(ingress.Config.PublicURL, guarded))
		server := &http.Server{
			Addr:              net.JoinHostPort("127.0.0.1", strconv.Itoa(ingress.Config.LocalPort)),
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       90 * time.Second,
			MaxHeaderBytes:    1 << 20,
		}
		h.RemoteIngresses[ingress.Name] = server
	}
	return nil
}

func (h *HTTP) remoteIngressRuntime(primaryID string, ingress config.NamedRemoteIngress) (*agent.Runtime, string, error) {
	workspaceID := strings.ToLower(strings.TrimSpace(ingress.Config.WorkspaceID))
	if workspaceID == "" || workspaceID == "default" || workspaceID == primaryID {
		return h.Runtime, primaryID, nil
	}
	runtime := h.Runtimes[workspaceID]
	if runtime == nil {
		return nil, "", fmt.Errorf("remote ingress %q references unknown or disabled workspace %q", ingress.Name, workspaceID)
	}
	return runtime, workspaceID, nil
}

func (h *HTTP) remoteIngressOriginMiddleware(publicURL string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		origin := strings.TrimSpace(request.Header.Get("Origin"))
		if origin != "" && !remoteIngressOriginAllowed(publicURL, origin) {
			h.sendJSON(writer, http.StatusForbidden, map[string]any{
				"jsonrpc": "2.0", "error": map[string]any{"code": -32000, "message": "Origin not allowed."}, "id": nil,
			})
			return
		}
		if origin != "" {
			writer.Header().Add("Vary", "Origin")
		}
		next.ServeHTTP(writer, request)
	})
}

func remoteIngressOriginAllowed(publicURL, origin string) bool {
	public, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil || public.Scheme == "" || public.Host == "" {
		return false
	}
	parsedOrigin, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsedOrigin.Scheme == "" || parsedOrigin.Host == "" || parsedOrigin.User != nil || parsedOrigin.RawQuery != "" || parsedOrigin.Fragment != "" {
		return false
	}
	if parsedOrigin.Path != "" && parsedOrigin.Path != "/" {
		return false
	}
	expected := strings.ToLower(public.Scheme + "://" + public.Host)
	actual := strings.ToLower(parsedOrigin.Scheme + "://" + parsedOrigin.Host)
	return actual == expected
}

func sortedHTTPServerNames(values map[string]*http.Server) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (h *HTTP) remoteIngressSummaries() []map[string]any {
	items := make([]map[string]any, 0, len(h.Runtime.Config.EnabledRemoteIngresses()))
	for _, ingress := range h.Runtime.Config.EnabledRemoteIngresses() {
		workspaceID := ingress.Config.WorkspaceID
		if strings.TrimSpace(workspaceID) == "" {
			workspaceID = h.Runtime.WorkspaceID
		}
		items = append(items, map[string]any{
			"name": ingress.Name, "provider": ingress.Config.Provider,
			"workspace_id": workspaceID, "tool_profile": ingress.Config.ToolProfile,
			"local_port": ingress.Config.LocalPort, "public_url": ingress.Config.PublicURL,
		})
	}
	return items
}
