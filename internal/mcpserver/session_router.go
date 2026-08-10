// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"wormhole/internal/agent"
	"wormhole/internal/workspaceregistry"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	SessionEndpoint        = "/mcp/session"
	SessionFastEndpoint    = "/mcp/session/fast"
	defaultBindingTTL      = 24 * time.Hour
	defaultCleanupInterval = time.Minute
	defaultMaxBindings     = 4096
)

const SessionInstructions = `This Wormhole endpoint routes one ChatGPT conversation to one workspace.

When the user writes "workspace <id>", immediately call workspace_select with that ID. Do not interpret this as a file or shell request.

Before calling any coding, filesystem, repository, process, policy, memory, or upstream MCP tool, a workspace must be selected. workspace_select returns workspace_binding. Preserve that exact value in this conversation and pass it as workspace_binding on every later Wormhole tool call. Do not ask the user to copy or manage the binding.

Each new chat must select its own workspace. Never reuse a binding copied from another conversation. Use workspace_current to verify the selected workspace, workspace_list to discover IDs, and workspace_clear only when the user asks to detach the chat.

If a coding tool reports that no workspace is selected or that a binding expired, call workspace_select again using the workspace requested in the conversation.

` + Instructions

type ProfileToolInfo struct {
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Scope        string   `json:"scope"`
	ReadOnly     bool     `json:"readOnly"`
	Destructive  bool     `json:"destructive"`
	OpenWorld    bool     `json:"openWorld"`
	WorkspaceIDs []string `json:"workspaceIds"`
}

var sessionControlProfileTools = []ProfileToolInfo{
	{Name: "workspace_clear", Title: "Clear workspace", Description: "Remove the workspace binding from this chat.", Scope: "session", WorkspaceIDs: []string{}},
	{Name: "workspace_current", Title: "Current workspace", Description: "Return the workspace currently bound to this chat.", Scope: "session", ReadOnly: true, WorkspaceIDs: []string{}},
	{Name: "workspace_list", Title: "List workspaces", Description: "List workspaces available to the session router.", Scope: "session", ReadOnly: true, WorkspaceIDs: []string{}},
	{Name: "workspace_select", Title: "Select workspace", Description: "Bind this chat to one Wormhole workspace.", Scope: "session", WorkspaceIDs: []string{}},
}

type workspaceBinding struct {
	Token       string
	WorkspaceID string
	SessionID   string
	CreatedAt   time.Time
	LastUsedAt  time.Time
}

// SessionRouter binds logical MCP sessions or opaque conversation tokens to
// immutable workspace runtimes. Runtimes are created by the daemon and remain
// workspace-local; only dispatch selection is shared here.
type SessionRouter struct {
	version         string
	primaryID       string
	ttl             time.Duration
	cleanupInterval time.Duration
	maxBindings     int
	now             func() time.Time

	runtimes           map[string]*agent.Runtime
	workspaceIDsCached []string
	specs              []agent.ToolSpec
	profiles           map[string]ProfileDefinition

	mu               sync.Mutex
	sessions         map[string]string
	bindingSessions  map[string]map[string]struct{}
	bindings         map[string]workspaceBinding
	schemaValidators sync.Map
	nextCleanup      time.Time
	expiredCount     uint64
	evictedCount     uint64
}

func NewSessionRouter(primaryRuntime *agent.Runtime, named map[string]*agent.Runtime) *SessionRouter {
	runtimes := map[string]*agent.Runtime{}
	version := ""
	primaryID := ""
	if primaryRuntime != nil {
		primaryID = workspaceregistry.NormalizeID(primaryRuntime.WorkspaceID)
		if primaryID != "" {
			runtimes[primaryID] = primaryRuntime
		}
		version = primaryRuntime.Version
	}
	for rawID, runtime := range named {
		id := workspaceregistry.NormalizeID(rawID)
		if id == "" || id == primaryID || runtime == nil {
			continue
		}
		runtimes[id] = runtime
		if version == "" {
			version = runtime.Version
		}
	}
	router := &SessionRouter{
		version: version, primaryID: primaryID, ttl: defaultBindingTTL,
		cleanupInterval: defaultCleanupInterval, maxBindings: defaultMaxBindings, now: time.Now,
		runtimes: runtimes, sessions: map[string]string{}, bindingSessions: map[string]map[string]struct{}{},
		bindings: map[string]workspaceBinding{},
	}
	router.workspaceIDsCached = router.computeWorkspaceIDs()
	router.specs = router.collectToolSpecs()
	router.profiles = map[string]ProfileDefinition{}
	var profileRuntime *agent.Runtime
	if primaryRuntime != nil {
		profileRuntime = primaryRuntime
	} else {
		for _, id := range router.workspaceIDsCached {
			profileRuntime = router.runtimes[id]
			if profileRuntime != nil {
				break
			}
		}
	}
	if profileRuntime != nil {
		for _, profile := range ProfileDefinitions(profileRuntime.Config) {
			router.profiles[profile.ID] = profile
		}
	} else {
		for _, profile := range []ProfileDefinition{BuiltInProfile(ToolProfileRemoteRead), BuiltInProfile(ToolProfileFast), BuiltInProfile(ToolProfileFull)} {
			router.profiles[profile.ID] = profile
		}
	}
	return router
}

func NewSessionGateway(router *SessionRouter) *mcp.Server {
	return NewSessionGatewayProfile(router, ToolProfileFull)
}

func NewSessionGatewayProfile(router *SessionRouter, profile ToolProfile) *mcp.Server {
	if router == nil {
		router = NewSessionRouter(nil, nil)
	}
	definition, ok := router.ResolveProfile(string(profile))
	if !ok {
		definition = BuiltInProfile(ToolProfileFull)
	}
	return NewSessionGatewayDefinition(router, definition)
}

func NewSessionGatewayDefinition(router *SessionRouter, profile ProfileDefinition) *mcp.Server {
	if router == nil {
		router = NewSessionRouter(nil, nil)
	}
	name := "Wormhole · workspace session"
	instructions := SessionInstructions
	if profile.ID != "full" {
		name += " · " + profile.ID
		instructions += "\n\nThis session endpoint uses the " + profile.Name + " tool profile while preserving workspace selection."
		if profile.CompactDefaults {
			instructions += " Compact defaults are applied when supported arguments are omitted."
		}
	}
	server := mcp.NewServer(
		&mcp.Implementation{Name: name, Version: router.version},
		&mcp.ServerOptions{Instructions: instructions, PageSize: 100},
	)
	registerWidget(server)
	router.registerControlTools(server)
	for _, original := range router.specs {
		if !router.profileToolEnabled(profile, original.Name) {
			continue
		}
		spec := original
		readOnly, openWorld, destructive := spec.ReadOnly, spec.OpenWorld, spec.Destructive
		server.AddTool(&mcp.Tool{
			Name: spec.Name, Title: spec.Title,
			Description: spec.Description + " Requires workspace_select first; reuse its workspace_binding in this conversation.",
			InputSchema: routedToolSchema(spec.Schema), Meta: mcp.Meta(spec.Meta),
			Annotations: &mcp.ToolAnnotations{
				Title: spec.Title, ReadOnlyHint: readOnly, OpenWorldHint: &openWorld,
				DestructiveHint: &destructive, IdempotentHint: readOnly,
			},
		}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, err := decodeToolArguments(request)
			if err != nil {
				return toolError(err), nil
			}
			applyProfileDefaultsDefinition(profile, spec.Name, args)
			bindingToken := strings.TrimSpace(stringValue(args["workspace_binding"]))
			if bindingToken == "" {
				bindingToken = strings.TrimSpace(stringValue(args["_workspace_binding"]))
			}
			delete(args, "workspace_binding")
			delete(args, "_workspace_binding")
			if bindingToken == "" {
				return toolError(errors.New("workspace_binding is required; call workspace_select and pass the returned binding")), nil
			}

			sessionID := requestSessionID(request.Session)
			runtime, binding, err := router.resolve(sessionID, bindingToken)
			if err != nil {
				return toolError(err), nil
			}
			if !profileToolEnabledDefinition(runtime, profile, spec.Name) {
				return toolError(fmt.Errorf("tool %q is unavailable in profile %q for workspace %q", spec.Name, profile.ID, binding.WorkspaceID)), nil
			}
			targetSpec, ok := runtime.ToolSpec(spec.Name)
			if !ok {
				return toolError(fmt.Errorf("tool %q is not registered in workspace %q", spec.Name, binding.WorkspaceID)), nil
			}
			if err := router.validateToolArguments(binding.WorkspaceID, spec.Name, targetSpec.Schema, args); err != nil {
				return toolError(err), nil
			}
			value, err := runtime.HandleSession(ctx, binding.SessionID, spec.Name, args)
			if err != nil {
				return toolError(err), nil
			}
			if forwarded, ok := value.(*mcp.CallToolResult); ok {
				return forwarded, nil
			}
			return toolSuccessWithMode(value, profileOutputModeDefinition(profile, spec.OutputMode)), nil
		})
	}
	return server
}

func (r *SessionRouter) registerControlTools(server *mcp.Server) {
	server.AddTool(&mcp.Tool{
		Name: "workspace_select", Title: "Select workspace",
		Description: "Bind this chat to one Wormhole workspace. Call this when the user says 'workspace <id>'.",
		InputSchema: objectSchema(map[string]any{
			"id": map[string]any{"type": "string", "description": "Registered workspace ID, for example loyalty-api."},
		}, []string{"id"}),
		Annotations: &mcp.ToolAnnotations{Title: "Select workspace", ReadOnlyHint: false, IdempotentHint: true},
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeToolArguments(request)
		if err != nil {
			return toolError(err), nil
		}
		binding, runtime, err := r.selectWorkspace(requestSessionID(request.Session), stringValue(args["id"]))
		if err != nil {
			return toolError(err), nil
		}
		return toolSuccess(map[string]any{
			"selected": true, "workspace_id": binding.WorkspaceID,
			"workspace_binding": binding.Token, "root": runtime.Workspace.Primary,
			"workspace_access":   "wormhole_tools_only",
			"expires_in_seconds": int64(r.ttl / time.Second),
			"instruction": "Pass workspace_binding unchanged on every later Wormhole tool call in this chat. " +
				WorkspaceAccessInstructions,
		}), nil
	})

	server.AddTool(&mcp.Tool{
		Name: "workspace_current", Title: "Current workspace",
		Description: "Return the workspace currently bound to this chat.",
		InputSchema: objectSchema(map[string]any{
			"workspace_binding": bindingSchema(true),
		}, []string{"workspace_binding"}),
		Annotations: &mcp.ToolAnnotations{Title: "Current workspace", ReadOnlyHint: true, IdempotentHint: true},
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeToolArguments(request)
		if err != nil {
			return toolError(err), nil
		}
		runtime, binding, err := r.resolve(requestSessionID(request.Session), stringValue(args["workspace_binding"]))
		if err != nil {
			return toolError(err), nil
		}
		return toolSuccess(map[string]any{
			"selected": true, "workspace_id": binding.WorkspaceID,
			"workspace_binding": binding.Token, "root": runtime.Workspace.Primary,
			"workspace_access": "wormhole_tools_only",
			"last_used_at":     binding.LastUsedAt.UTC().Format(time.RFC3339),
			"instruction":      WorkspaceAccessInstructions,
		}), nil
	})

	server.AddTool(&mcp.Tool{
		Name: "workspace_list", Title: "List workspaces",
		Description: "List workspaces available to the session router.",
		InputSchema: objectSchema(map[string]any{
			"workspace_binding": bindingSchema(false),
		}, nil),
		Annotations: &mcp.ToolAnnotations{Title: "List workspaces", ReadOnlyHint: true, IdempotentHint: true},
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeToolArguments(request)
		if err != nil {
			return toolError(err), nil
		}
		return toolSuccess(map[string]any{
			"workspaces":  r.workspaceList(requestSessionID(request.Session), stringValue(args["workspace_binding"])),
			"instruction": "Select one by calling workspace_select with its id.",
		}), nil
	})

	server.AddTool(&mcp.Tool{
		Name: "workspace_clear", Title: "Clear workspace",
		Description: "Remove the workspace binding from this chat.",
		InputSchema: objectSchema(map[string]any{
			"workspace_binding": bindingSchema(true),
		}, []string{"workspace_binding"}),
		Annotations: &mcp.ToolAnnotations{Title: "Clear workspace", ReadOnlyHint: false, IdempotentHint: true},
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeToolArguments(request)
		if err != nil {
			return toolError(err), nil
		}
		cleared := r.clear(requestSessionID(request.Session), stringValue(args["workspace_binding"]))
		return toolSuccess(map[string]any{"cleared": cleared, "selected": false}), nil
	})
}

func (r *SessionRouter) selectWorkspace(sessionID, rawID string) (workspaceBinding, *agent.Runtime, error) {
	id, runtime := r.lookupRuntime(rawID)
	if runtime == nil {
		return workspaceBinding{}, nil, fmt.Errorf("workspace %q is not available; call workspace_list", strings.TrimSpace(rawID))
	}
	token, err := newBindingToken()
	if err != nil {
		return workspaceBinding{}, nil, fmt.Errorf("create workspace binding: %w", err)
	}
	now := r.now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanupDueLocked(now)
	if sessionID != "" {
		if previous := r.sessions[sessionID]; previous != "" {
			r.removeBindingLocked(previous)
		}
	}
	maxBindings := r.maxBindings
	if maxBindings <= 0 {
		maxBindings = defaultMaxBindings
	}
	for len(r.bindings) >= maxBindings {
		if !r.evictOldestLocked() {
			return workspaceBinding{}, nil, errors.New("workspace binding capacity is exhausted")
		}
	}
	binding := workspaceBinding{
		Token: token, WorkspaceID: id, SessionID: bindingSessionID(id, token),
		CreatedAt: now, LastUsedAt: now,
	}
	r.bindings[token] = binding
	r.bindSessionLocked(sessionID, token)
	return binding, runtime, nil
}

func (r *SessionRouter) resolve(sessionID, explicitToken string) (*agent.Runtime, workspaceBinding, error) {
	now := r.now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanupDueLocked(now)
	token := strings.TrimSpace(explicitToken)
	if token == "" {
		token = r.sessions[sessionID]
	}
	if token == "" {
		return nil, workspaceBinding{}, errors.New("no workspace selected for this chat; call workspace_select first")
	}
	binding, ok := r.bindings[token]
	if !ok {
		return nil, workspaceBinding{}, errors.New("workspace binding is invalid or expired; call workspace_select again")
	}
	if now.Sub(binding.LastUsedAt) > r.ttl {
		r.removeBindingLocked(token)
		r.expiredCount++
		return nil, workspaceBinding{}, errors.New("workspace binding is invalid or expired; call workspace_select again")
	}
	runtime := r.runtimes[binding.WorkspaceID]
	if runtime == nil {
		r.removeBindingLocked(token)
		return nil, workspaceBinding{}, fmt.Errorf("workspace %q is no longer available; call workspace_select again", binding.WorkspaceID)
	}
	binding.LastUsedAt = now
	r.bindings[token] = binding
	return runtime, binding, nil
}

func (r *SessionRouter) clear(sessionID, explicitToken string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	token := strings.TrimSpace(explicitToken)
	if token == "" {
		token = r.sessions[sessionID]
	}
	if token == "" {
		return false
	}
	if _, ok := r.bindings[token]; !ok {
		return false
	}
	r.removeBindingLocked(token)
	return true
}

func (r *SessionRouter) ResolveProfile(id string) (ProfileDefinition, bool) {
	if r == nil {
		return ProfileDefinition{}, false
	}
	profile, ok := r.profiles[strings.ToLower(strings.TrimSpace(id))]
	return profile, ok
}

func (r *SessionRouter) Profiles() []ProfileDefinition {
	if r == nil {
		return nil
	}
	ids := make([]string, 0, len(r.profiles))
	for id := range r.profiles {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		order := func(id string) int {
			switch id {
			case "fast":
				return 0
			case "full":
				return 1
			default:
				return 2
			}
		}
		left, right := order(ids[i]), order(ids[j])
		if left != right {
			return left < right
		}
		return ids[i] < ids[j]
	})
	profiles := make([]ProfileDefinition, 0, len(ids))
	for _, id := range ids {
		profiles = append(profiles, r.profiles[id])
	}
	return profiles
}

func (r *SessionRouter) profileToolEnabled(profile ProfileDefinition, name string) bool {
	for _, id := range r.workspaceIDs() {
		if profileToolEnabledDefinition(r.runtimes[id], profile, name) {
			return true
		}
	}
	return false
}

func (r *SessionRouter) ProfileTools(profile ToolProfile) []ProfileToolInfo {
	definition, ok := r.ResolveProfile(string(profile))
	if !ok {
		return nil
	}
	return r.ProfileToolsDefinition(definition)
}

func (r *SessionRouter) ProfileToolsDefinition(profile ProfileDefinition) []ProfileToolInfo {
	tools := append([]ProfileToolInfo(nil), sessionControlProfileTools...)
	workspaceIDs := r.workspaceIDs()
	for _, spec := range r.specs {
		if !r.profileToolEnabled(profile, spec.Name) {
			continue
		}
		availableIn := make([]string, 0, len(workspaceIDs))
		for _, id := range workspaceIDs {
			runtime := r.runtimes[id]
			if !profileToolEnabledDefinition(runtime, profile, spec.Name) {
				continue
			}
			if _, registered := runtime.ToolSpec(spec.Name); registered {
				availableIn = append(availableIn, id)
			}
		}
		tools = append(tools, ProfileToolInfo{
			Name: spec.Name, Title: spec.Title, Description: spec.Description,
			Scope: "workspace", ReadOnly: spec.ReadOnly, Destructive: spec.Destructive,
			OpenWorld: spec.OpenWorld, WorkspaceIDs: availableIn,
		})
	}
	sort.Slice(tools, func(left, right int) bool { return tools[left].Name < tools[right].Name })
	return tools
}

func (r *SessionRouter) Stats() map[string]any {
	now := r.now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanupDueLocked(now)
	cleanupInterval := r.cleanupInterval
	if cleanupInterval <= 0 {
		cleanupInterval = defaultCleanupInterval
	}
	maxBindings := r.maxBindings
	if maxBindings <= 0 {
		maxBindings = defaultMaxBindings
	}
	return map[string]any{
		"endpoint": SessionEndpoint, "fast_endpoint": SessionFastEndpoint,
		"binding_ttl_seconds":      int64(r.ttl / time.Second),
		"cleanup_interval_seconds": int64(cleanupInterval / time.Second),
		"active_bindings":          len(r.bindings), "max_bindings": maxBindings,
		"bound_sessions": len(r.sessions), "expired_bindings": r.expiredCount,
		"evicted_bindings": r.evictedCount,
		"workspace_count":  len(r.runtimes), "tool_count": len(r.ProfileTools(ToolProfileFull)),
		"fast_tool_count": len(r.ProfileTools(ToolProfileFast)),
	}
}

// BodyLimit returns the smallest positive request limit across routed
// workspaces. The HTTP layer must read the JSON-RPC envelope before the binding
// is available, so the conservative shared limit preserves every runtime's
// configured ceiling.
func (r *SessionRouter) BodyLimit() int {
	limit := 0
	for _, runtime := range r.runtimes {
		value := runtime.Config.MaxBodyBytes
		if value > 0 && (limit == 0 || value < limit) {
			limit = value
		}
	}
	return limit
}

func (r *SessionRouter) collectToolSpecs() []agent.ToolSpec {
	byName := map[string]agent.ToolSpec{}
	for _, id := range r.workspaceIDs() {
		runtime := r.runtimes[id]
		for _, spec := range runtime.Tools() {
			if !runtime.ToolEnabled(spec.Name) {
				continue
			}
			if existing, exists := byName[spec.Name]; exists {
				byName[spec.Name] = mergeRoutedToolSpec(existing, spec)
			} else {
				byName[spec.Name] = spec
			}
		}
	}
	for _, reserved := range []string{"workspace_select", "workspace_current", "workspace_list", "workspace_clear"} {
		delete(byName, reserved)
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]agent.ToolSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, byName[name])
	}
	return specs
}

func mergeRoutedToolSpec(current, next agent.ToolSpec) agent.ToolSpec {
	merged := current
	merged.ReadOnly = current.ReadOnly && next.ReadOnly
	merged.Destructive = current.Destructive || next.Destructive
	merged.OpenWorld = current.OpenWorld || next.OpenWorld
	if current.OutputMode != next.OutputMode {
		merged.OutputMode = agent.ToolOutputBoth
	}
	if !sameJSON(current.Schema, next.Schema) {
		merged.Schema = map[string]any{
			"type": "object", "properties": map[string]any{}, "additionalProperties": true,
		}
		if !strings.Contains(merged.Description, "Arguments vary by workspace") {
			merged.Description += " Arguments vary by workspace; select the workspace before constructing arguments."
		}
	}
	if !sameJSON(current.Meta, next.Meta) {
		merged.Meta = nil
	}
	return merged
}

func sameJSON(left, right any) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftRaw) == string(rightRaw)
}

func (r *SessionRouter) workspaceList(sessionID, explicitToken string) []map[string]any {
	selected := ""
	now := r.now().UTC()
	r.mu.Lock()
	r.cleanupDueLocked(now)
	token := strings.TrimSpace(explicitToken)
	if token == "" {
		token = r.sessions[sessionID]
	}
	if binding, ok := r.bindings[token]; ok && now.Sub(binding.LastUsedAt) <= r.ttl {
		selected = binding.WorkspaceID
	}
	r.mu.Unlock()
	items := make([]map[string]any, 0, len(r.runtimes))
	for _, id := range r.workspaceIDs() {
		runtime := r.runtimes[id]
		items = append(items, map[string]any{
			"id": id, "selected": id == selected, "root": runtime.Workspace.Primary,
			"tool_count": len(runtime.Tools()),
		})
	}
	return items
}

func (r *SessionRouter) workspaceIDs() []string {
	return append([]string(nil), r.workspaceIDsCached...)
}

func (r *SessionRouter) computeWorkspaceIDs() []string {
	ids := make([]string, 0, len(r.runtimes))
	for id := range r.runtimes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool {
		if ids[left] == r.primaryID {
			return true
		}
		if ids[right] == r.primaryID {
			return false
		}
		return ids[left] < ids[right]
	})
	return ids
}

func (r *SessionRouter) lookupRuntime(rawID string) (string, *agent.Runtime) {
	id := workspaceregistry.NormalizeID(rawID)
	if runtime := r.runtimes[id]; runtime != nil {
		return id, runtime
	}
	slug := workspaceregistry.SlugID(rawID)
	if runtime := r.runtimes[slug]; runtime != nil {
		return slug, runtime
	}
	return "", nil
}

func (r *SessionRouter) cleanupDueLocked(now time.Time) {
	cleanupInterval := r.cleanupInterval
	if cleanupInterval <= 0 {
		cleanupInterval = defaultCleanupInterval
	}
	if !r.nextCleanup.IsZero() && now.Before(r.nextCleanup) {
		return
	}
	for token, binding := range r.bindings {
		if now.Sub(binding.LastUsedAt) <= r.ttl {
			continue
		}
		r.removeBindingLocked(token)
		r.expiredCount++
	}
	r.nextCleanup = now.Add(cleanupInterval)
}

func (r *SessionRouter) bindSessionLocked(sessionID, token string) {
	if sessionID == "" || token == "" {
		return
	}
	if previous := r.sessions[sessionID]; previous != "" && previous != token {
		if sessions := r.bindingSessions[previous]; sessions != nil {
			delete(sessions, sessionID)
			if len(sessions) == 0 {
				delete(r.bindingSessions, previous)
			}
		}
	}
	r.sessions[sessionID] = token
	sessions := r.bindingSessions[token]
	if sessions == nil {
		sessions = map[string]struct{}{}
		r.bindingSessions[token] = sessions
	}
	sessions[sessionID] = struct{}{}
}

func (r *SessionRouter) removeBindingLocked(token string) {
	delete(r.bindings, token)
	for sessionID := range r.bindingSessions[token] {
		if r.sessions[sessionID] == token {
			delete(r.sessions, sessionID)
		}
	}
	delete(r.bindingSessions, token)
}

func (r *SessionRouter) evictOldestLocked() bool {
	oldestToken := ""
	var oldest time.Time
	for token, binding := range r.bindings {
		if oldestToken == "" || binding.LastUsedAt.Before(oldest) {
			oldestToken = token
			oldest = binding.LastUsedAt
		}
	}
	if oldestToken == "" {
		return false
	}
	r.removeBindingLocked(oldestToken)
	r.evictedCount++
	return true
}

func newBindingToken() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "cbw_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func bindingSessionID(workspaceID, token string) string {
	digest := sha256.Sum256([]byte(token))
	return fmt.Sprintf("workspace:%s:chat:%x", workspaceID, digest[:8])
}

func routedToolSchema(schema map[string]any) map[string]any {
	clone := cloneObject(schema)
	if clone == nil {
		clone = map[string]any{}
	}
	clone["type"] = "object"
	properties, _ := clone["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	properties["workspace_binding"] = bindingSchema(true)
	clone["properties"] = properties
	required := stringArray(clone["required"])
	if !containsString(required, "workspace_binding") {
		required = append(required, "workspace_binding")
	}
	clone["required"] = required
	return clone
}

func bindingSchema(required bool) map[string]any {
	description := "Opaque binding returned by workspace_select for this chat."
	if required {
		description += " Pass it unchanged."
	}
	return map[string]any{"type": "string", "description": description, "minLength": 1}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type": "object", "properties": properties, "additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func decodeToolArguments(request *mcp.CallToolRequest) (map[string]any, error) {
	args := map[string]any{}
	if request == nil || len(request.Params.Arguments) == 0 {
		return args, nil
	}
	if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	return args, nil
}

func cloneObject(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var clone map[string]any
	if json.Unmarshal(raw, &clone) != nil {
		return nil
	}
	return clone
}

func stringArray(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := stringValue(item); text != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
