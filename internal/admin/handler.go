// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"codebridge/internal/adminauth"
	"codebridge/internal/adminui"
	"codebridge/internal/agent"
	"codebridge/internal/config"
	"codebridge/internal/mcpserver"
	"codebridge/internal/workspaceregistry"
)

const (
	basePath          = "/admin"
	apiPrefix         = "/admin/api/v1"
	csrfCookieName    = "codebridge_admin_csrf"
	sessionCookieName = "codebridge_admin_session"
	maxJSONBody       = 2 << 20
	maxSecretBody     = 128 << 10
	maxAuthBody       = 8 << 10
)

var workspaceOwnedFields = map[string]bool{
	"workspace": true, "port": true, "host": true, "authToken": true,
	"approvalToken": true, "allowedOrigins": true, "noTunnel": true,
	"tunnelBin": true, "tunnelId": true, "organizationId": true,
	"profile": true, "profileDir": true, "runtimeKeyEnv": true, "tunnels": true,
}

// Handler serves a local-only administration application and versioned API.
type Handler struct {
	Runtime  *agent.Runtime
	Runtimes map[string]*agent.Runtime
	Router   *mcpserver.SessionRouter
	assets   fs.FS
	auth     *adminauth.Manager
	mu       sync.Mutex
}

// New creates an admin handler. Every route remains loopback-only regardless
// of the MCP listener configuration.
func New(runtime *agent.Runtime, named map[string]*agent.Runtime, routers ...*mcpserver.SessionRouter) *Handler {
	copyNamed := make(map[string]*agent.Runtime, len(named))
	for id, child := range named {
		copyNamed[id] = child
	}
	var router *mcpserver.SessionRouter
	if len(routers) > 0 {
		router = routers[0]
	}
	if router == nil {
		router = mcpserver.NewSessionRouter(runtime, copyNamed)
	}
	return &Handler{
		Runtime: runtime, Runtimes: copyNamed, Router: router, assets: adminui.FS(),
		auth: adminauth.NewManager(config.AdminAuthPath()),
	}
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	h.securityHeaders(writer, request)
	if !requestIsLoopback(request) {
		h.sendError(writer, http.StatusForbidden, "loopback_only", "The admin interface is available only from the local machine.")
		return
	}
	if !validAdminHost(request.Host) {
		h.sendError(writer, http.StatusForbidden, "invalid_host", "The admin interface accepts only localhost or loopback IP hostnames.")
		return
	}
	csrf, err := h.ensureCSRFCookie(writer, request)
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "csrf_generation_failed", "Unable to initialize the admin security token.")
		return
	}
	if strings.HasPrefix(request.URL.Path, apiPrefix) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions {
			if !sameOrigin(request) {
				h.sendError(writer, http.StatusForbidden, "origin_rejected", "A same-origin request is required.")
				return
			}
			provided := strings.TrimSpace(request.Header.Get("X-Codebridge-CSRF"))
			if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(csrf)) != 1 {
				h.sendError(writer, http.StatusForbidden, "csrf_rejected", "The CSRF token is missing or invalid.")
				return
			}
		}
		suffix := strings.TrimPrefix(request.URL.Path, apiPrefix)
		switch {
		case suffix == "/auth/status" && request.Method == http.MethodGet:
			h.authStatus(writer, request)
		case suffix == "/auth/login" && request.Method == http.MethodPost:
			h.login(writer, request)
		case suffix == "/auth/setup" && request.Method == http.MethodPost:
			h.setupAdmin(writer, request)
		default:
			if !h.requireAuthentication(writer, request) {
				return
			}
			if suffix == "/auth/logout" && request.Method == http.MethodPost {
				h.logout(writer, request)
				return
			}
			h.serveAPI(writer, request)
		}
		return
	}
	h.serveAssets(writer, request)
}

func (h *Handler) authStatus(writer http.ResponseWriter, request *http.Request) {
	if h.auth == nil {
		h.sendJSON(writer, http.StatusOK, map[string]any{
			"configured": true, "authenticated": true, "username": "test-admin",
		})
		return
	}
	token := h.sessionToken(request)
	configured, authenticated, username, err := h.auth.Status(token)
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "admin_auth_read_failed", "Unable to read the local admin credential file. Reset it with the Codebridge CLI.")
		return
	}
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"configured": configured, "authenticated": authenticated, "username": username,
		"credentialPath": config.AdminAuthPath(),
	})
}

func (h *Handler) setupAdmin(writer http.ResponseWriter, request *http.Request) {
	if h.auth == nil {
		h.sendError(writer, http.StatusNotImplemented, "admin_auth_unavailable", "Admin authentication is unavailable.")
		return
	}
	raw, err := readBody(writer, request, maxAuthBody)
	if err != nil {
		h.sendError(writer, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		h.sendError(writer, http.StatusBadRequest, "invalid_body", "Expected a JSON object containing username and password.")
		return
	}
	if err := adminauth.ValidateCredentialInput(input.Username, input.Password); err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "admin_credentials_invalid", err.Error())
		return
	}

	h.mu.Lock()
	_, err = adminauth.SetInitialCredentials(config.AdminAuthPath(), input.Username, input.Password)
	h.mu.Unlock()
	switch {
	case errors.Is(err, adminauth.ErrAlreadyConfigured):
		h.sendError(writer, http.StatusConflict, "admin_already_configured", "The local admin account has already been configured. Sign in or reset it from the local CLI.")
		return
	case err != nil:
		h.sendError(writer, http.StatusInternalServerError, "admin_setup_failed", "Unable to create the owner-only local admin credential file.")
		return
	}

	token, username, _, err := h.auth.Login(input.Username, input.Password)
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "admin_login_failed", "The account was created, but an authenticated browser session could not be created. Sign in with the new account.")
		return
	}
	h.setSessionCookie(writer, request, token)
	h.sendJSON(writer, http.StatusCreated, map[string]any{"configured": true, "authenticated": true, "username": username})
}

func (h *Handler) login(writer http.ResponseWriter, request *http.Request) {
	if h.auth == nil {
		h.sendError(writer, http.StatusNotImplemented, "admin_auth_unavailable", "Admin authentication is unavailable.")
		return
	}
	raw, err := readBody(writer, request, maxAuthBody)
	if err != nil {
		h.sendError(writer, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		h.sendError(writer, http.StatusBadRequest, "invalid_body", "Expected a JSON object containing username and password.")
		return
	}
	token, username, retryAfter, err := h.auth.Login(input.Username, input.Password)
	switch {
	case errors.Is(err, adminauth.ErrNotConfigured):
		h.sendError(writer, http.StatusServiceUnavailable, "admin_setup_required", "Admin credentials are not configured. Create the first account from this loopback-only Admin UI or use the local CLI.")
		return
	case errors.Is(err, adminauth.ErrRateLimited):
		seconds := int(retryAfter.Round(time.Second) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		writer.Header().Set("Retry-After", strconv.Itoa(seconds))
		h.sendError(writer, http.StatusTooManyRequests, "admin_login_rate_limited", "Too many failed login attempts. Try again shortly.")
		return
	case errors.Is(err, adminauth.ErrInvalidCredentials):
		h.sendError(writer, http.StatusUnauthorized, "invalid_credentials", "Invalid admin username or password.")
		return
	case err != nil:
		h.sendError(writer, http.StatusInternalServerError, "admin_login_failed", "Unable to create an admin session.")
		return
	}
	h.setSessionCookie(writer, request, token)
	h.sendJSON(writer, http.StatusOK, map[string]any{"authenticated": true, "username": username})
}

func (h *Handler) logout(writer http.ResponseWriter, request *http.Request) {
	token := h.sessionToken(request)
	if h.auth != nil && token != "" {
		h.auth.Logout(token)
	}
	http.SetCookie(writer, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: basePath,
		MaxAge: -1, SameSite: http.SameSiteStrictMode,
		Secure: request.TLS != nil, HttpOnly: true,
	})
	h.sendJSON(writer, http.StatusOK, map[string]any{"authenticated": false})
}

func (h *Handler) requireAuthentication(writer http.ResponseWriter, request *http.Request) bool {
	if h.auth == nil {
		return true
	}
	configured, authenticated, _, err := h.auth.Status(h.sessionToken(request))
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "admin_auth_read_failed", "Unable to read the local admin credential file. Reset it with the Codebridge CLI.")
		return false
	}
	if !configured {
		h.sendError(writer, http.StatusServiceUnavailable, "admin_setup_required", "Admin credentials are not configured. Create the first account from this loopback-only Admin UI or use the local CLI.")
		return false
	}
	if !authenticated {
		h.sendError(writer, http.StatusUnauthorized, "authentication_required", "Sign in with the local admin account to continue.")
		return false
	}
	return true
}

func (h *Handler) setSessionCookie(writer http.ResponseWriter, request *http.Request, token string) {
	http.SetCookie(writer, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: basePath,
		MaxAge: int(adminauth.SessionTTL / time.Second), SameSite: http.SameSiteStrictMode,
		Secure: request.TLS != nil, HttpOnly: true,
	})
}

func (h *Handler) sessionToken(request *http.Request) string {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func (h *Handler) serveAPI(writer http.ResponseWriter, request *http.Request) {
	suffix := strings.TrimPrefix(request.URL.Path, apiPrefix)
	if suffix == "" {
		suffix = "/"
	}
	switch {
	case suffix == "/bootstrap" && request.Method == http.MethodGet:
		h.getBootstrap(writer)
	case suffix == "/profiles" && request.Method == http.MethodGet:
		h.getProfiles(writer)
	case suffix == "/tools/catalog" && request.Method == http.MethodGet:
		h.getToolCatalog(writer)
	case suffix == "/operations" && request.Method == http.MethodGet:
		h.getOperations(writer, request)
	case suffix == "/approvals" && request.Method == http.MethodGet:
		h.getApprovals(writer, request)
	case strings.HasPrefix(suffix, "/approvals/"):
		h.approvalDecision(writer, request, strings.TrimPrefix(suffix, "/approvals/"))
	case suffix == "/audit" && request.Method == http.MethodGet:
		h.getAudit(writer, request)
	case suffix == "/config" && request.Method == http.MethodGet:
		h.getConfig(writer)
	case suffix == "/config" && request.Method == http.MethodPut:
		h.putConfig(writer, request)
	case suffix == "/config/validate" && request.Method == http.MethodPost:
		h.validateConfig(writer, request)
	case suffix == "/workspaces/browse" && request.Method == http.MethodGet:
		h.browseWorkspaces(writer, request)
	case suffix == "/workspaces" && request.Method == http.MethodGet:
		h.getWorkspaces(writer)
	case suffix == "/workspaces" && request.Method == http.MethodPost:
		h.createWorkspace(writer, request)
	case strings.HasPrefix(suffix, "/workspaces/"):
		h.workspaceConfig(writer, request, strings.TrimPrefix(suffix, "/workspaces/"))
	case suffix == "/secrets" && request.Method == http.MethodGet:
		h.getSecrets(writer)
	case strings.HasPrefix(suffix, "/secrets/"):
		h.secret(writer, request, strings.TrimPrefix(suffix, "/secrets/"))
	default:
		h.sendError(writer, http.StatusNotFound, "not_found", "Admin API route not found.")
	}
}

func (h *Handler) getBootstrap(writer http.ResponseWriter) {
	ids := make([]string, 0, len(h.Runtimes))
	for id := range h.Runtimes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"name": "Codebridge", "version": h.Runtime.Version, "tier": h.Runtime.Tier,
		"activeConfigId": h.Runtime.ConfigID, "workspaceId": h.Runtime.WorkspaceID,
		"activeWorkspaceIds": ids, "configPath": config.ConfigPath(),
		"homePath": config.AppHomeDir(), "restartRequiredAfterSave": true,
		"security": map[string]any{
			"loopbackOnly": true, "sameOriginWrites": true, "csrfProtected": true,
			"adminAuthentication": true, "secretValuesReadable": false,
		},
		"startupWarnings": h.Runtime.StartupWarnings(),
	})
}

type adminToolCatalogEntry struct {
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Groups       []string `json:"groups"`
	ReadOnly     bool     `json:"readOnly"`
	Destructive  bool     `json:"destructive"`
	OpenWorld    bool     `json:"openWorld"`
	WorkspaceIDs []string `json:"workspaceIds"`
	groups       map[string]struct{}
	workspaces   map[string]struct{}
}

func (h *Handler) getToolCatalog(writer http.ResponseWriter) {
	type runtimeEntry struct {
		id      string
		runtime *agent.Runtime
	}
	runtimes := []runtimeEntry{{id: h.Runtime.WorkspaceID, runtime: h.Runtime}}
	namedIDs := make([]string, 0, len(h.Runtimes))
	for id := range h.Runtimes {
		namedIDs = append(namedIDs, id)
	}
	sort.Strings(namedIDs)
	for _, id := range namedIDs {
		runtimes = append(runtimes, runtimeEntry{id: id, runtime: h.Runtimes[id]})
	}

	byName := map[string]*adminToolCatalogEntry{}
	groupTools := map[string]map[string]struct{}{}
	workspaceIDs := map[string]struct{}{}
	for _, item := range runtimes {
		if item.runtime == nil {
			continue
		}
		workspaceID := strings.TrimSpace(item.runtime.WorkspaceID)
		if workspaceID == "" {
			workspaceID = strings.TrimSpace(item.id)
		}
		if workspaceID == "" {
			workspaceID = "default"
		}
		workspaceIDs[workspaceID] = struct{}{}
		for _, spec := range item.runtime.Tools() {
			group := item.runtime.ToolModuleName(spec.Name)
			if group == "" {
				continue
			}
			entry := byName[spec.Name]
			if entry == nil {
				entry = &adminToolCatalogEntry{
					Name: spec.Name, Title: spec.Title, Description: spec.Description,
					ReadOnly: spec.ReadOnly, Destructive: spec.Destructive, OpenWorld: spec.OpenWorld,
					groups: map[string]struct{}{}, workspaces: map[string]struct{}{},
				}
				byName[spec.Name] = entry
			} else {
				entry.ReadOnly = entry.ReadOnly && spec.ReadOnly
				entry.Destructive = entry.Destructive || spec.Destructive
				entry.OpenWorld = entry.OpenWorld || spec.OpenWorld
			}
			entry.groups[group] = struct{}{}
			entry.workspaces[workspaceID] = struct{}{}
			if groupTools[group] == nil {
				groupTools[group] = map[string]struct{}{}
			}
			groupTools[group][spec.Name] = struct{}{}
		}
	}

	toolNames := make([]string, 0, len(byName))
	for name := range byName {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)
	tools := make([]adminToolCatalogEntry, 0, len(toolNames))
	for _, name := range toolNames {
		entry := byName[name]
		for group := range entry.groups {
			entry.Groups = append(entry.Groups, group)
		}
		for workspaceID := range entry.workspaces {
			entry.WorkspaceIDs = append(entry.WorkspaceIDs, workspaceID)
		}
		sort.Strings(entry.Groups)
		sort.Strings(entry.WorkspaceIDs)
		tools = append(tools, *entry)
	}

	groupNames := make([]string, 0, len(groupTools))
	for name := range groupTools {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	groups := make([]map[string]any, 0, len(groupNames))
	for _, name := range groupNames {
		groups = append(groups, map[string]any{"name": name, "toolCount": len(groupTools[name])})
	}
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"tools": tools, "groups": groups, "workspaceCount": len(workspaceIDs),
	})
}

func (h *Handler) getProfiles(writer http.ResponseWriter) {
	router := h.Router
	if router == nil {
		router = mcpserver.NewSessionRouter(h.Runtime, h.Runtimes)
	}
	tunnelsByMode := map[string][]map[string]any{"fast": []map[string]any{}, "full": []map[string]any{}}
	for _, tunnel := range h.Runtime.Config.EffectiveTunnels() {
		mode := strings.ToLower(strings.TrimSpace(tunnel.Config.Mode))
		if mode == "" {
			mode = "full"
		}
		if mode != "fast" && mode != "full" {
			continue
		}
		tunnelsByMode[mode] = append(tunnelsByMode[mode], map[string]any{
			"name": tunnel.Name, "enabled": tunnel.Config.IsEnabled(),
			"tunnelId": tunnel.Config.TunnelID, "profile": tunnel.Config.Profile,
		})
	}
	profiles := []map[string]any{
		{
			"id": "fast", "name": "Fast", "endpoint": mcpserver.SessionFastEndpoint,
			"description": "Compact profile with workspace routing and high-value coding tools.",
			"tools":       router.ProfileTools(mcpserver.ToolProfileFast), "tunnels": tunnelsByMode["fast"],
		},
		{
			"id": "full", "name": "Full", "endpoint": mcpserver.SessionEndpoint,
			"description": "Complete profile with workspace routing and every enabled runtime tool.",
			"tools":       router.ProfileTools(mcpserver.ToolProfileFull), "tunnels": tunnelsByMode["full"],
		},
	}
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"profiles": profiles, "workspaceCount": 1 + len(h.Runtimes),
	})
}

func (h *Handler) getConfig(writer http.ResponseWriter) {
	snapshot, err := h.loadConfigSnapshot()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "config_read_failed", err.Error())
		return
	}
	writer.Header().Set("ETag", quoteETag(snapshot.Revision))
	h.sendJSON(writer, http.StatusOK, snapshot)
}

func (h *Handler) putConfig(writer http.ResponseWriter, request *http.Request) {
	raw, err := readBody(writer, request, maxJSONBody)
	if err != nil {
		h.sendError(writer, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	cfg, err := config.ParseJSONWithRuntimeSecrets(
		raw, "admin request", h.Runtime.Config.AuthToken, h.Runtime.Config.ApprovalToken,
	)
	if err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "config_invalid", err.Error())
		return
	}
	persisted := cfg
	persisted.AuthToken = h.Runtime.Config.AuthToken
	persisted.ApprovalToken = h.Runtime.Config.ApprovalToken
	if err := persisted.Validate(true); err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "config_invalid", err.Error())
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	current, err := fileRevision(config.ConfigPath())
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "config_read_failed", err.Error())
		return
	}
	if !etagMatches(request.Header.Get("If-Match"), current) {
		h.sendError(writer, http.StatusPreconditionFailed, "revision_conflict", "The configuration changed after it was loaded. Reload before saving.")
		return
	}
	if err := config.Save(persisted); err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "config_save_failed", err.Error())
		return
	}
	snapshot, err := h.loadConfigSnapshot()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "config_read_failed", err.Error())
		return
	}
	writer.Header().Set("ETag", quoteETag(snapshot.Revision))
	h.sendJSON(writer, http.StatusOK, snapshot)
}

func (h *Handler) validateConfig(writer http.ResponseWriter, request *http.Request) {
	raw, err := readBody(writer, request, maxJSONBody)
	if err != nil {
		h.sendError(writer, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	cfg, err := config.ParseJSONWithRuntimeSecrets(
		raw, "admin validation", h.Runtime.Config.AuthToken, h.Runtime.Config.ApprovalToken,
	)
	if err == nil {
		validated := cfg
		validated.AuthToken = h.Runtime.Config.AuthToken
		validated.ApprovalToken = h.Runtime.Config.ApprovalToken
		err = validated.Validate(true)
	}
	if err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "config_invalid", err.Error())
		return
	}
	h.sendJSON(writer, http.StatusOK, map[string]any{"valid": true, "config": cfg})
}

type configSnapshot struct {
	Config          config.Config `json:"config"`
	Revision        string        `json:"revision"`
	Path            string        `json:"path"`
	RestartRequired bool          `json:"restartRequired"`
}

func (h *Handler) loadConfigSnapshot() (configSnapshot, error) {
	cfg, err := h.editableConfig()
	if err != nil {
		return configSnapshot{}, err
	}
	cfg.AuthToken, cfg.ApprovalToken = "", ""
	revision, err := fileRevision(config.ConfigPath())
	if err != nil {
		return configSnapshot{}, err
	}
	return configSnapshot{Config: cfg, Revision: revision, Path: config.ConfigPath(), RestartRequired: true}, nil
}

func (h *Handler) editableConfig() (config.Config, error) {
	_, err := os.Stat(config.ConfigPath())
	switch {
	case err == nil:
		return config.LoadFileForEditing(
			config.ConfigPath(), h.Runtime.Config.AuthToken, h.Runtime.Config.ApprovalToken,
		)
	case errors.Is(err, os.ErrNotExist):
		return config.Prepare(h.Runtime.Config)
	default:
		return config.Config{}, err
	}
}

func (h *Handler) getWorkspaces(writer http.ResponseWriter) {
	registry, err := workspaceregistry.Load()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "registry_read_failed", err.Error())
		return
	}
	items := make([]map[string]any, 0, len(registry.Workspaces))
	for _, id := range workspaceregistry.SortedIDs(registry) {
		entry := registry.Workspaces[id]
		revision, revisionErr := fileRevision(entry.ConfigPath)
		if revisionErr != nil {
			h.sendError(writer, http.StatusInternalServerError, "workspace_read_failed", revisionErr.Error())
			return
		}
		_, active := h.Runtimes[id]
		items = append(items, map[string]any{
			"id": id, "workspace": entry.Workspace, "enabled": entry.Enabled,
			"active": active, "configPath": entry.ConfigPath, "dataDir": entry.DataDir,
			"revision": revision, "createdAt": entry.CreatedAt, "updatedAt": entry.UpdatedAt,
		})
	}
	revision, err := fileRevision(workspaceregistry.Path())
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "registry_read_failed", err.Error())
		return
	}
	writer.Header().Set("ETag", quoteETag(revision))
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"primary": map[string]any{
			"id": h.Runtime.WorkspaceID, "workspace": h.Runtime.Workspace.Primary,
			"active": true, "configPath": config.ConfigPath(),
		},
		"workspaces": items, "revision": revision,
	})
}

func (h *Handler) workspaceConfig(writer http.ResponseWriter, request *http.Request, rawID string) {
	id, err := url.PathUnescape(strings.Trim(rawID, "/"))
	if err != nil || id == "" || strings.Contains(id, "/") {
		h.sendError(writer, http.StatusBadRequest, "workspace_invalid", "Invalid workspace ID.")
		return
	}
	id = workspaceregistry.NormalizeID(id)
	registry, err := workspaceregistry.Load()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "registry_read_failed", err.Error())
		return
	}
	entry, exists := registry.Workspaces[id]
	if !exists {
		h.sendError(writer, http.StatusNotFound, "workspace_not_found", "Workspace is not registered.")
		return
	}
	switch request.Method {
	case http.MethodGet:
		h.getWorkspaceConfig(writer, entry)
	case http.MethodPut:
		h.putWorkspaceConfig(writer, request, entry)
	case http.MethodDelete:
		h.deleteWorkspace(writer, request, entry)
	default:
		h.sendError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET, PUT, or DELETE for workspace configuration.")
	}
}

func (h *Handler) getWorkspaceConfig(writer http.ResponseWriter, entry workspaceregistry.Registration) {
	base, err := h.editableConfig()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "config_read_failed", err.Error())
		return
	}
	base.AuthToken = h.Runtime.Config.AuthToken
	base.ApprovalToken = h.Runtime.Config.ApprovalToken
	override, err := config.ReadOverrideFile(entry.ConfigPath)
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "workspace_read_failed", err.Error())
		return
	}
	effective, err := effectiveWorkspaceConfig(base, entry, override)
	if err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "workspace_invalid", err.Error())
		return
	}
	revision, err := fileRevision(entry.ConfigPath)
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "workspace_read_failed", err.Error())
		return
	}
	writer.Header().Set("ETag", quoteETag(revision))
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"registration": entry, "override": override, "effective": effective,
		"revision": revision, "restartRequired": true,
	})
}

func (h *Handler) putWorkspaceConfig(writer http.ResponseWriter, request *http.Request, entry workspaceregistry.Registration) {
	raw, err := readBody(writer, request, maxJSONBody)
	if err != nil {
		h.sendError(writer, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	override, err := config.ParseOverrideJSON(raw, "admin workspace override")
	if err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "workspace_invalid", err.Error())
		return
	}
	for field := range override {
		if workspaceOwnedFields[field] {
			h.sendError(writer, http.StatusUnprocessableEntity, "workspace_owned_field", fmt.Sprintf("Field %q is owned by the shared daemon or workspace registry and cannot be overridden.", field))
			return
		}
	}
	base, err := h.editableConfig()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "config_read_failed", err.Error())
		return
	}
	base.AuthToken = h.Runtime.Config.AuthToken
	base.ApprovalToken = h.Runtime.Config.ApprovalToken
	if _, err := effectiveWorkspaceConfig(base, entry, override); err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "workspace_invalid", err.Error())
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	current, err := fileRevision(entry.ConfigPath)
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "workspace_read_failed", err.Error())
		return
	}
	if !etagMatches(request.Header.Get("If-Match"), current) {
		h.sendError(writer, http.StatusPreconditionFailed, "revision_conflict", "The workspace override changed after it was loaded. Reload before saving.")
		return
	}
	if err := config.SaveOverrideFile(entry.ConfigPath, base, override); err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "workspace_save_failed", err.Error())
		return
	}
	h.getWorkspaceConfig(writer, entry)
}

func effectiveWorkspaceConfig(base config.Config, entry workspaceregistry.Registration, override map[string]any) (config.Config, error) {
	effective, err := config.ApplyOverride(base, override)
	if err != nil {
		return effective, err
	}
	effective.Workspace = entry.Workspace
	effective.Host = base.Host
	effective.Port = base.Port
	effective.AuthToken = base.AuthToken
	effective.ApprovalToken = base.ApprovalToken
	effective.AllowedOrigins = append([]string(nil), base.AllowedOrigins...)
	effective.NoTunnel = true
	effective.TunnelID = ""
	effective.Tunnels = nil
	if err := effective.Validate(true); err != nil {
		return effective, err
	}
	effective.AuthToken = ""
	effective.ApprovalToken = ""
	return effective, nil
}

func (h *Handler) getSecrets(writer http.ResponseWriter) {
	references, err := h.secretReferences()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "secrets_read_failed", err.Error())
		return
	}
	keys, err := config.DotEnvKeys(config.DotEnvPath())
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "secrets_read_failed", err.Error())
		return
	}
	configured := make(map[string]bool, len(keys))
	for _, key := range keys {
		configured[key] = true
	}
	names := make([]string, 0, len(references))
	for name := range references {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		managed := configured[name]
		environmentValue, environmentExists := os.LookupEnv(name)
		environmentConfigured := environmentExists && environmentValue != ""
		source := "missing"
		switch {
		case managed:
			source = "dotenv"
		case environmentConfigured:
			source = "environment"
		}
		items = append(items, map[string]any{
			"name": name, "configured": managed || environmentConfigured,
			"managed": managed, "source": source, "referencedBy": references[name],
		})
	}
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"path": config.DotEnvPath(), "secrets": items, "valuesReadable": false,
	})
}

func (h *Handler) secret(writer http.ResponseWriter, request *http.Request, rawName string) {
	name, err := url.PathUnescape(strings.Trim(rawName, "/"))
	if err != nil || name == "" || strings.Contains(name, "/") {
		h.sendError(writer, http.StatusBadRequest, "secret_invalid", "Invalid secret name.")
		return
	}
	references, err := h.secretReferences()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "secrets_read_failed", err.Error())
		return
	}
	if _, allowed := references[name]; !allowed {
		h.sendError(writer, http.StatusForbidden, "secret_not_referenced", "Only environment variables referenced by the current configuration can be managed here.")
		return
	}
	switch request.Method {
	case http.MethodPut:
		raw, readErr := readBody(writer, request, maxSecretBody)
		if readErr != nil {
			h.sendError(writer, http.StatusBadRequest, "invalid_body", readErr.Error())
			return
		}
		var input struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			h.sendError(writer, http.StatusBadRequest, "invalid_body", "Expected a JSON object containing value.")
			return
		}
		if input.Value == "" {
			h.sendError(writer, http.StatusUnprocessableEntity, "secret_empty", "Use DELETE to remove a secret; empty values are not stored.")
			return
		}
		h.mu.Lock()
		err = config.UpdateDotEnv(config.DotEnvPath(), map[string]*string{name: &input.Value})
		h.mu.Unlock()
		if err != nil {
			h.sendError(writer, http.StatusInternalServerError, "secret_save_failed", err.Error())
			return
		}
		_ = os.Setenv(name, input.Value)
		h.sendJSON(writer, http.StatusOK, map[string]any{"name": name, "configured": true, "restartRequired": true})
	case http.MethodDelete:
		h.mu.Lock()
		keys, keysErr := config.DotEnvKeys(config.DotEnvPath())
		if keysErr != nil {
			h.mu.Unlock()
			h.sendError(writer, http.StatusInternalServerError, "secrets_read_failed", keysErr.Error())
			return
		}
		managed := false
		for _, key := range keys {
			if key == name {
				managed = true
				break
			}
		}
		if !managed {
			h.mu.Unlock()
			h.sendError(writer, http.StatusConflict, "secret_not_managed", "This value comes from the process environment and cannot be deleted from Codebridge .env.")
			return
		}
		err = config.UpdateDotEnv(config.DotEnvPath(), map[string]*string{name: nil})
		h.mu.Unlock()
		if err != nil {
			h.sendError(writer, http.StatusInternalServerError, "secret_delete_failed", err.Error())
			return
		}
		_ = os.Unsetenv(name)
		h.sendJSON(writer, http.StatusOK, map[string]any{"name": name, "configured": false, "restartRequired": true})
	default:
		h.sendError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "Use PUT or DELETE for secret values.")
	}
}

func (h *Handler) secretReferences() (map[string][]string, error) {
	cfg, err := h.editableConfig()
	if err != nil {
		return nil, err
	}
	refs := map[string][]string{}
	add := func(name, owner string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		refs[name] = append(refs[name], owner)
	}
	if len(cfg.Tunnels) == 0 {
		add(cfg.RuntimeKeyEnv, "tunnel.runtimeKeyEnv")
	}
	for _, tunnel := range cfg.EffectiveTunnels() {
		add(tunnel.Config.RuntimeKeyEnv, "tunnels."+tunnel.Name+".runtimeKeyEnv")
	}
	add(cfg.Memory.SecretEnv, "memory.secretEnv")
	for serverName, server := range cfg.MCPServers {
		for target, source := range server.EnvRefs {
			add(source, "mcpServers."+serverName+".envRefs."+target)
		}
		for header, source := range server.HeaderRefs {
			add(source, "mcpServers."+serverName+".headerRefs."+header)
		}
	}
	for name := range refs {
		sort.Strings(refs[name])
	}
	return refs, nil
}

func (h *Handler) serveAssets(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		h.sendError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "Admin assets are read-only.")
		return
	}
	if request.URL.Path == basePath {
		http.Redirect(writer, request, basePath+"/", http.StatusTemporaryRedirect)
		return
	}
	assetPath := strings.TrimPrefix(request.URL.Path, basePath+"/")
	assetPath = path.Clean("/" + assetPath)
	assetPath = strings.TrimPrefix(assetPath, "/")
	if assetPath == "" || assetPath == "." {
		assetPath = "index.html"
	}
	info, err := fs.Stat(h.assets, assetPath)
	if err != nil || info.IsDir() {
		assetPath = "index.html"
		info, err = fs.Stat(h.assets, assetPath)
	}
	if err != nil {
		h.sendError(writer, http.StatusNotFound, "asset_not_found", "Admin application asset not found.")
		return
	}
	raw, err := fs.ReadFile(h.assets, assetPath)
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "asset_read_failed", err.Error())
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(assetPath))
	if contentType == "" {
		contentType = http.DetectContentType(raw)
	}
	writer.Header().Set("Content-Type", contentType)
	if assetPath == "index.html" {
		writer.Header().Set("Cache-Control", "no-store")
	} else if strings.Contains(path.Base(assetPath), "-") {
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		writer.Header().Set("Cache-Control", "no-cache")
	}
	writer.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	writer.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(raw)
	}
}

func (h *Handler) securityHeaders(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if strings.HasPrefix(request.URL.Path, apiPrefix) {
		writer.Header().Set("Cache-Control", "no-store")
	}
}

func (h *Handler) ensureCSRFCookie(writer http.ResponseWriter, request *http.Request) (string, error) {
	if cookie, err := request.Cookie(csrfCookieName); err == nil && validCSRFToken(cookie.Value) {
		return cookie.Value, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	value := hex.EncodeToString(raw)
	http.SetCookie(writer, &http.Cookie{
		Name: csrfCookieName, Value: value, Path: basePath,
		MaxAge: 8 * 60 * 60, SameSite: http.SameSiteStrictMode,
		Secure: request.TLS != nil, HttpOnly: false,
	})
	return value, nil
}

func validCSRFToken(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func requestIsLoopback(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func validAdminHost(value string) bool {
	host := value
	if parsed, _, err := net.SplitHostPort(value); err == nil {
		host = parsed
	} else if strings.Count(value, ":") > 1 {
		host = strings.Trim(value, "[]")
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sameOrigin(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return false
	}
	expectedScheme := "http"
	if request.TLS != nil {
		expectedScheme = "https"
	}
	return parsed.Scheme == expectedScheme && strings.EqualFold(parsed.Host, request.Host)
}

func fileRevision(filename string) (string, error) {
	raw, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		raw = []byte("codebridge:missing-file")
	} else if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func quoteETag(revision string) string { return strconv.Quote(revision) }

func etagMatches(header, revision string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(candidate), "W/"))
		if unquoted, err := strconv.Unquote(candidate); err == nil && unquoted == revision {
			return true
		}
	}
	return false
}

func readBody(writer http.ResponseWriter, request *http.Request, limit int64) ([]byte, error) {
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, errors.New("request body is required")
	}
	return raw, nil
}

func (h *Handler) sendJSON(writer http.ResponseWriter, status int, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "response_encode_failed", err.Error())
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	writer.WriteHeader(status)
	_, _ = writer.Write(raw)
}

func (h *Handler) sendError(writer http.ResponseWriter, status int, code, message string) {
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": message}})
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	writer.WriteHeader(status)
	_, _ = writer.Write(raw)
}
