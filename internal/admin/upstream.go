// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"wormhole/internal/security"
)

func (h *Handler) getUpstreamMCP(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	workspaces := make([]map[string]any, 0, len(h.runtimeEntries()))
	for _, item := range h.runtimeEntries() {
		workspaces = append(workspaces, map[string]any{
			"id": item.ID, "root": item.Runtime.Workspace.Primary,
			"servers": item.Runtime.UpstreamMCPStatuses(ctx),
		})
	}
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"generatedAt": time.Now().UTC(), "workspaces": workspaces,
	})
}

func (h *Handler) upstreamMCPAction(writer http.ResponseWriter, request *http.Request, rawPath string) {
	if request.Method != http.MethodPost {
		h.sendError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST for upstream MCP control actions.")
		return
	}
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	if len(parts) != 3 || parts[2] != "refresh" {
		h.sendError(writer, http.StatusBadRequest, "upstream_path_invalid", "Expected /upstream/<workspace>/<server>/refresh.")
		return
	}
	workspaceID, workspaceErr := url.PathUnescape(parts[0])
	serverName, serverErr := url.PathUnescape(parts[1])
	if workspaceErr != nil || serverErr != nil || workspaceID == "" || serverName == "" {
		h.sendError(writer, http.StatusBadRequest, "upstream_path_invalid", "Invalid workspace or upstream server name.")
		return
	}
	runtime, ok := h.runtimeByID(workspaceID)
	if !ok {
		h.sendError(writer, http.StatusNotFound, "workspace_not_found", "The upstream workspace is not active.")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 60*time.Second)
	defer cancel()
	status, err := runtime.Runtime.RefreshUpstreamMCP(ctx, serverName)
	if err != nil {
		h.sendError(writer, http.StatusBadGateway, "upstream_refresh_failed", security.RedactText(err.Error(), 2<<10))
		return
	}
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"workspaceId": runtime.ID, "server": status,
		"restartRequired": status["restartRequired"],
	})
}
