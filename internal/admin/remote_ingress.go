// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"net/http"
	"time"
)

const maxRemoteIngressStatuses = 32

// RemoteIngressRuntimeStatus is the bounded, secret-free live state exposed to
// the local Admin UI for one active remote MCP ingress.
type RemoteIngressRuntimeStatus struct {
	Name                    string `json:"name"`
	Provider                string `json:"provider"`
	Mode                    string `json:"mode"`
	WorkspaceID             string `json:"workspaceId"`
	ToolProfile             string `json:"toolProfile"`
	LocalPort               int    `json:"localPort"`
	PublicURL               string `json:"publicUrl,omitempty"`
	AuthTokenEnv            string `json:"authTokenEnv"`
	AuthTokenFallbackEnv    string `json:"authTokenFallbackEnv,omitempty"`
	AuthConfigured          bool   `json:"authConfigured"`
	PrimaryAuthConfigured   bool   `json:"primaryAuthConfigured"`
	PrimaryAuthReady        bool   `json:"primaryAuthReady"`
	FallbackAuthConfigured  *bool  `json:"fallbackAuthConfigured,omitempty"`
	FallbackAuthReady       *bool  `json:"fallbackAuthReady,omitempty"`
	ProviderTokenConfigured *bool  `json:"providerTokenConfigured,omitempty"`
	ListenerReachable       bool   `json:"listenerReachable"`
	MCPReady                bool   `json:"mcpReady"`
	ProtocolVersion         string `json:"protocolVersion,omitempty"`
	ToolCount               int    `json:"toolCount"`
	Issue                   string `json:"issue,omitempty"`
}

// RemoteIngressStatusResponse is intentionally bounded because it is polled by
// the Admin UI and each live entry may perform a local MCP readiness probe.
type RemoteIngressStatusResponse struct {
	GeneratedAt time.Time                    `json:"generatedAt"`
	Ingresses   []RemoteIngressRuntimeStatus `json:"ingresses"`
	Truncated   bool                         `json:"truncated"`
}

type RemoteIngressStatusProvider func(context.Context, int) RemoteIngressStatusResponse

// SetRemoteIngressStatusProvider attaches a server-owned status callback without
// introducing an admin -> server package dependency.
func (h *Handler) SetRemoteIngressStatusProvider(provider RemoteIngressStatusProvider) {
	h.remoteIngressStatus = provider
}

func (h *Handler) getRemoteIngressStatus(writer http.ResponseWriter, request *http.Request) {
	if h.remoteIngressStatus == nil {
		h.sendJSON(writer, http.StatusOK, RemoteIngressStatusResponse{
			GeneratedAt: time.Now().UTC(), Ingresses: []RemoteIngressRuntimeStatus{},
		})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 6*time.Second)
	defer cancel()
	response := h.remoteIngressStatus(ctx, maxRemoteIngressStatuses)
	if response.GeneratedAt.IsZero() {
		response.GeneratedAt = time.Now().UTC()
	}
	if response.Ingresses == nil {
		response.Ingresses = []RemoteIngressRuntimeStatus{}
	}
	h.sendJSON(writer, http.StatusOK, response)
}
