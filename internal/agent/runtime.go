// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/memory"
	"codebridge/internal/patch"
	"codebridge/internal/processx"
	"codebridge/internal/security"
	"codebridge/internal/state"
	"codebridge/internal/workspace"
)

type Runtime struct {
	Config                  config.Config
	WorkspaceID             string
	DataDir                 string
	Workspace               *workspace.Manager
	Store                   *state.Store
	AuditWriter             *state.AuditWriter
	Approvals               *security.ApprovalManager
	Patches                 *patch.Engine
	Processes               *processx.Registry
	Memory                  memory.Provider
	MemoryRecorder          *memory.Recorder
	MemoryProject           string
	MemoryFallbackSessionID string
	Version                 string
	Tier                    string
	ConfigID                string

	shared          *SharedServices
	ownsShared      bool
	modules         map[string]ToolModule
	toolModules     map[string]ToolModule
	toolSpecs       []ToolSpec
	toolSpecIndex   map[string]ToolSpec
	moduleOrder     []string
	moduleMu        sync.RWMutex
	modulesClosed   bool
	closeOnce       sync.Once
	closeErr        error
	startupWarnings []string

	profileMu              sync.RWMutex
	profile                map[string]any
	repoIndexMu            sync.Mutex
	repoCacheMu            sync.Mutex
	repoGeneration         uint64
	repoInventories        map[string]repoInventory
	repoViews              map[repoViewKey]repoIndex
	repoSymbols            map[repoSymbolKey]repoSymbolCacheEntry
	gitStatusCache         map[string]gitStatusSnapshot
	memoryHealthMu         sync.Mutex
	memoryHealthValue      memory.HealthResult
	memoryHealthAt         time.Time
	memoryHealthRefreshing bool
	metricsOnce            sync.Once
	metrics                *runtimeCallTracker
}

// StartupReporter receives human-readable startup phase updates. Reporters
// should return quickly because startup waits for each callback to complete.
type StartupReporter func(stage, message string)

func New(cfg config.Config, version, tier, configID string) (*Runtime, error) {
	return NewContext(context.Background(), cfg, version, tier, configID)
}

func NewContext(ctx context.Context, cfg config.Config, version, tier, configID string) (*Runtime, error) {
	return NewContextWithReporter(ctx, cfg, version, tier, configID, nil)
}

func NewContextWithReporter(ctx context.Context, cfg config.Config, version, tier, configID string, reporter StartupReporter) (*Runtime, error) {
	return NewWorkspaceContextWithReporter(ctx, "default", "", cfg, version, tier, configID, reporter)
}

// NewWorkspaceContextWithReporter creates one fully isolated runtime inside a
// standalone resource owner. Daemon composition should use
// NewWorkspaceContextWithSharedServices so compatible providers and upstream
// sessions can be pooled across workspace runtimes.
func NewWorkspaceContextWithReporter(ctx context.Context, workspaceID, dataDir string, cfg config.Config, version, tier, configID string, reporter StartupReporter) (*Runtime, error) {
	shared := NewSharedServices(version)
	runtime, err := newWorkspaceContext(ctx, workspaceID, dataDir, cfg, version, tier, configID, shared, true, reporter)
	if err != nil {
		_ = shared.Close()
	}
	return runtime, err
}

// NewWorkspaceContextWithSharedServices creates a workspace-local runtime that
// borrows daemon-wide resources. The caller owns SharedServices and must close
// it only after every borrowed runtime has shut down.
func NewWorkspaceContextWithSharedServices(ctx context.Context, workspaceID, dataDir string, cfg config.Config, version, tier, configID string, shared *SharedServices, reporter StartupReporter) (*Runtime, error) {
	if shared == nil {
		return nil, errors.New("shared services are required")
	}
	return newWorkspaceContext(ctx, workspaceID, dataDir, cfg, version, tier, configID, shared, false, reporter)
}

func newWorkspaceContext(ctx context.Context, workspaceID, dataDir string, cfg config.Config, version, tier, configID string, shared *SharedServices, ownsShared bool, reporter StartupReporter) (*Runtime, error) {
	workspaceID = strings.ToLower(strings.TrimSpace(workspaceID))
	if workspaceID == "" {
		workspaceID = "default"
	}
	reportStartup(reporter, "workspace", fmt.Sprintf("preparing %s", cfg.Workspace))
	profile := loadProfileFile(cfg.Workspace)
	var ignored []string
	if values, ok := profile["ignoredDirs"].([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok {
				ignored = append(ignored, text)
			}
		}
	}
	manager, err := workspace.New(cfg.Workspace, cfg.ExtraRoots, ignored)
	if err != nil {
		return nil, err
	}
	reportStartup(reporter, "workspace", fmt.Sprintf("ready: %s", manager.Primary))
	store, err := state.NewAt(manager.Primary, dataDir)
	if err != nil {
		return nil, err
	}
	var auditWriter *state.AuditWriter
	if cfg.Audit {
		auditWriter, err = shared.acquireAudit(store.AuditPath)
		if err != nil {
			return nil, err
		}
	}
	if cfg.Memory.Enabled {
		reportStartup(reporter, "memory", fmt.Sprintf("initializing provider=%s required=%t", cfg.Memory.Provider, cfg.Memory.Required))
	}
	memoryLease, err := shared.acquireMemory(cfg.Memory)
	if err != nil {
		return nil, err
	}
	memoryProvider := memoryLease.Provider
	if memoryLease.ProviderReused {
		reportStartup(reporter, "memory", fmt.Sprintf("reusing provider=%s", memoryProvider.Name()))
	}
	if cfg.Memory.Enabled && cfg.Memory.Required {
		reportStartup(reporter, "memory", fmt.Sprintf("checking %s", cfg.Memory.Endpoint))
		health := memoryProvider.Health(ctx)
		if !health.Available {
			return nil, fmt.Errorf("required memory provider %q is unavailable: %s", memoryProvider.Name(), health.Error)
		}
		reportStartup(reporter, "memory", fmt.Sprintf("connected: %s", health.Endpoint))
	} else if cfg.Memory.Enabled {
		reportStartup(reporter, "memory", "initialized; availability will be checked on demand")
	}
	memoryProject := memory.ResolveProject(manager.Primary, cfg.Memory.ProjectStrategy)
	memorySessionID := fmt.Sprintf("codebridge-process-%d-%d", os.Getpid(), time.Now().UnixNano())
	memoryRecorder := memoryLease.Recorder
	runtime := &Runtime{
		Config: cfg, WorkspaceID: workspaceID, DataDir: store.DataDir,
		Workspace: manager, Store: store, AuditWriter: auditWriter,
		Approvals: security.NewApprovalManager(store, cfg.ApprovalToken, 10*time.Minute),
		Processes: processx.NewRegistry(cfg.MaxProcesses),
		Memory:    memoryProvider, MemoryRecorder: memoryRecorder,
		MemoryProject: memoryProject, MemoryFallbackSessionID: memorySessionID,
		Version: version, Tier: tier, ConfigID: configID, profile: profile,
		shared: shared, ownsShared: ownsShared,
	}
	runtime.metricsTracker()
	runtime.Patches = &patch.Engine{Workspace: manager, Store: store}
	modules := []ToolModule{
		newBasicModule(runtime),
		newFilesystemModule(runtime),
		newRepoModule(runtime),
		newWorkflowModule(runtime),
		newMemoryModule(runtime),
		newExecutionModule(runtime),
	}
	for _, module := range modules {
		if err := runtime.RegisterModule(module); err != nil {
			for index := len(modules) - 1; index >= 0; index-- {
				_ = modules[index].Close()
			}
			return nil, err
		}
	}
	if err := runtime.registerConfiguredUpstreamMCP(ctx, reporter); err != nil {
		_ = runtime.Shutdown()
		return nil, err
	}
	reportStartup(reporter, "runtime", fmt.Sprintf("ready with %d tools", len(runtime.Tools())))
	return runtime, nil
}

func reportStartup(reporter StartupReporter, stage, message string) {
	if reporter != nil && strings.TrimSpace(message) != "" {
		reporter(stage, message)
	}
}

func (r *Runtime) Shutdown() error {
	r.closeOnce.Do(func() {
		moduleErr := r.closeModules()
		var sharedErr error
		if r.ownsShared && r.shared != nil {
			sharedErr = r.shared.Close()
		}
		r.closeErr = errors.Join(moduleErr, sharedErr)
	})
	return r.closeErr
}

func (r *Runtime) Close() { _ = r.Shutdown() }

func (r *Runtime) addStartupWarning(message string) {
	if strings.TrimSpace(message) != "" {
		r.startupWarnings = append(r.startupWarnings, message)
	}
}

func (r *Runtime) StartupWarnings() []string {
	return append([]string(nil), r.startupWarnings...)
}

func (r *Runtime) SharedResourceStats() map[string]any {
	if r == nil || r.shared == nil {
		return map[string]any{"enabled": false}
	}
	stats := r.shared.Stats()
	stats["tool_contracts"] = sharedModuleContractStats()
	return stats
}

func (r *Runtime) Handle(ctx context.Context, name string, args map[string]any) (any, error) {
	return r.HandleSession(ctx, "", name, args)
}

func (r *Runtime) HandleSession(ctx context.Context, sessionID, name string, args map[string]any) (value any, err error) {
	if args == nil {
		args = map[string]any{}
	}
	if sessionID == "" {
		sessionID = r.MemoryFallbackSessionID
	}
	call := r.beginToolCall(name)
	identity := CallIdentity{SessionID: sessionID, WorkspaceID: r.WorkspaceID}
	outcome := toolCallOutcome{}
	auditAttempted := false
	returnedNormally := false
	defer func() {
		if !returnedNormally {
			outcome.Err = errToolCallPanicked
			if !auditAttempted {
				outcome.AuditErr = r.audit(
					call, identity, name, args, nil, false, errToolCallPanicked,
					"failed", time.Since(call.Started),
				)
			}
		} else {
			outcome.Err = err
		}
		outcome.Duration = time.Since(call.Started)
		r.finishToolCall(call, outcome)
	}()

	ctx = context.WithValue(ctx, memorySessionContextKey{}, identity.SessionID)
	if policyErr := r.enforcePolicy(name, args); policyErr != nil {
		err = policyErr
		outcome.PolicyRejected = true
		auditAttempted = true
		outcome.AuditErr = r.audit(
			call, identity, name, args, nil, false, err, "policy_rejected", time.Since(call.Started),
		)
		returnedNormally = true
		return nil, err
	}
	value, err = r.dispatch(ctx, identity, name, args)
	if strings.HasPrefix(call.Module, "mcp_") {
		if spec, ok := r.ToolSpec(name); ok && !spec.ReadOnly {
			// Upstream mutation tools may report an error after partially changing
			// workspace-visible state, so invalidate conservatively on every attempt.
			r.invalidateRepositoryCaches()
		}
	}
	auditAttempted = true
	outcome.AuditErr = r.audit(
		call, identity, name, args, value, err == nil, err,
		classifyToolCallStatus(err, false), time.Since(call.Started),
	)
	outcome.ObservationAttempted, outcome.ObservationAccepted = r.captureMemoryObservation(
		call.ID, sessionID, name, args, value, err,
	)
	returnedNormally = true
	return value, err
}

type memorySessionContextKey struct{}

func memorySessionID(ctx context.Context) string {
	value, _ := ctx.Value(memorySessionContextKey{}).(string)
	return value
}

func (r *Runtime) audit(call trackedToolCall, identity CallIdentity, tool string, args map[string]any, value any, ok bool, callErr error, status string, duration time.Duration) error {
	if !r.Config.Audit {
		return nil
	}
	record := map[string]any{
		"ts": time.Now().UTC().Format(time.RFC3339Nano), "call_id": call.ID,
		"tool": tool, "tool_module": call.Module, "status": status, "ok": ok,
		"duration_us":  max(int64(0), duration.Microseconds()),
		"workspace_id": r.WorkspaceID, "workspace": r.Workspace.Primary,
		"session_id": identity.SessionID,
	}
	module, _ := r.ToolModule(tool)
	auditProvider, customAudit := module.(ToolAuditProvider)
	if r.Config.AuditArgs {
		if customAudit {
			record["args"] = auditProvider.AuditArguments(tool, args)
		} else {
			record["args"] = security.RedactDeep(args, 0)
		}
	}
	if customAudit {
		if metadata := auditProvider.AuditMetadata(tool, args, value); len(metadata) > 0 {
			record["module"] = metadata
		}
	}
	if callErr != nil {
		if customAudit {
			record["error"] = auditProvider.AuditError(callErr)
		} else {
			record["error"] = callErr.Error()
		}
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal audit record: %w", err)
	}
	line := append(raw, '\n')
	if r.AuditWriter != nil && filepath.Clean(r.AuditWriter.Path()) == filepath.Clean(r.Store.AuditPath) {
		if err := r.AuditWriter.Append(line); err != nil {
			return fmt.Errorf("enqueue audit record: %w", err)
		}
	} else if err := r.Store.AppendLine(r.Store.AuditPath, line); err != nil {
		return fmt.Errorf("append audit record: %w", err)
	}
	return nil
}

func (r *Runtime) FlushAudit(ctx context.Context) error {
	if r == nil || r.AuditWriter == nil {
		return nil
	}
	return r.AuditWriter.Flush(ctx)
}

func (r *Runtime) enforcePolicy(tool string, args map[string]any) error {
	policyTools := map[string]bool{
		"policy_status": true, "explain_risk": true, "request_approval": true,
		"request_approval_batch": true, "approve_request": true, "deny_request": true,
	}
	if policyTools[tool] {
		return nil
	}
	if r.Config.Mode != "full" && explicitQualityCommand(tool, args) {
		return errors.New("explicit quality commands are disabled in safe mode; omit command to use the detected/profile command or switch to mode=full")
	}

	spec, registered := r.ToolSpec(tool)
	if r.Config.Policy == "strict" && registered && !spec.ReadOnly {
		return fmt.Errorf("tool %q is blocked by policy=strict", tool)
	}
	policy := r.toolCallPolicy(tool, args, spec, registered)
	if policy.AlwaysRequireApproval {
		return r.consumeToolApproval(tool, policy.ApprovalAction)
	}
	if r.Config.Policy == "full" || r.Config.Policy != "balanced" || !policy.RequiresApproval {
		return nil
	}
	return r.consumeToolApproval(tool, policy.ApprovalAction)
}

func (r *Runtime) toolCallPolicy(tool string, args map[string]any, spec ToolSpec, registered bool) ToolCallPolicy {
	r.moduleMu.RLock()
	module := r.toolModules[tool]
	r.moduleMu.RUnlock()
	if provider, ok := module.(ToolPolicyProvider); ok {
		return r.scopeToolCallPolicy(provider.ToolPolicy(tool, args))
	}
	if registered && !spec.ReadOnly {
		return r.scopeToolCallPolicy(ToolCallPolicy{
			ApprovalAction:   genericToolApprovalAction(tool, args),
			RequiresApproval: true,
		})
	}
	return ToolCallPolicy{}
}

func (r *Runtime) scopeToolCallPolicy(policy ToolCallPolicy) ToolCallPolicy {
	if policy.ApprovalAction == "" || r.WorkspaceID == "" || r.WorkspaceID == "default" {
		return policy
	}
	policy.ApprovalAction = "workspace:" + r.WorkspaceID + ":" + policy.ApprovalAction
	return policy
}

func (r *Runtime) consumeToolApproval(tool, action string) error {
	if action == "" {
		return nil
	}
	if err := r.Approvals.Consume(action); err != nil {
		return fmt.Errorf("approval required: call request_approval with action=%q, then approve_request: %w", action, err)
	}
	return nil
}

func genericToolApprovalAction(tool string, args map[string]any) string {
	material, _ := json.Marshal(map[string]any{"tool": tool, "args": args})
	sum := sha256.Sum256(material)
	return fmt.Sprintf("%s:sha256:%s", tool, hex.EncodeToString(sum[:12]))
}

func approvalAction(tool string, args map[string]any) string {
	switch tool {
	case "delete_path":
		return "delete_path:" + stringArg(args, "path", "")
	case "memory_forget":
		raw, _ := json.Marshal(args)
		return "memory_forget:" + string(raw)
	case "run_command", "proc_start":
		command := stringArg(args, "command", "")
		if security.Classify(command).NeedsApproval {
			return tool + ":" + command
		}
	case "run_tests", "run_build", "run_lint", "run_changed_tests":
		if command := strings.TrimSpace(stringArg(args, "command", "")); command != "" {
			return tool + ":" + command
		}
	case "run_commands":
		var risky []any
		for _, item := range arrayArg(args, "commands") {
			entry, _ := item.(map[string]any)
			if security.Classify(stringArg(entry, "command", "")).NeedsApproval {
				risky = append(risky, entry)
			}
		}
		if len(risky) > 0 {
			raw, _ := json.Marshal(risky)
			return "run_commands:" + string(raw)
		}
	case "git":
		argv := stringsArg(args, "args")
		if !security.IsReadOnlyGit(argv) {
			raw, _ := json.Marshal(argv)
			return "git:" + string(raw)
		}
	case "apply_patch":
		raw, _ := json.Marshal(args)
		if strings.Contains(string(raw), `"op":"delete"`) || strings.Contains(string(raw), "+++ /dev/null") {
			return "apply_patch:delete"
		}
	}
	return ""
}

func explicitQualityCommand(tool string, args map[string]any) bool {
	switch tool {
	case "run_tests", "run_build", "run_lint", "run_changed_tests":
		return strings.TrimSpace(stringArg(args, "command", "")) != ""
	default:
		return false
	}
}

func workspaceProfilePath(root string) string {
	return filepath.Join(root, ".codebridge", "profile.json")
}

func legacyWorkspaceProfilePath(root string) string {
	return filepath.Join(root, ".agent", "profile.json")
}

func activeWorkspaceProfilePath(root string) string {
	canonical := workspaceProfilePath(root)
	if _, err := os.Stat(canonical); err == nil {
		return canonical
	}
	legacy := legacyWorkspaceProfilePath(root)
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return canonical
}

func loadProfileFile(root string) map[string]any {
	raw, err := os.ReadFile(activeWorkspaceProfilePath(root))
	if err != nil {
		return nil
	}
	var profile map[string]any
	if json.Unmarshal(raw, &profile) != nil {
		return nil
	}
	return profile
}

func (r *Runtime) reloadProfile() map[string]any {
	profile := loadProfileFile(r.Workspace.Primary)
	r.profileMu.Lock()
	r.profile = profile
	r.profileMu.Unlock()
	r.invalidateRepositoryCaches()
	return profile
}

func (r *Runtime) currentProfile() map[string]any {
	r.profileMu.RLock()
	defer r.profileMu.RUnlock()
	if r.profile == nil {
		return nil
	}
	out := map[string]any{}
	for key, value := range r.profile {
		out[key] = value
	}
	return out
}

func (r *Runtime) statePath(name string) string {
	return filepath.Join(r.Store.WorkspaceDir, name)
}

func readJSONFile(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func stringArg(args map[string]any, key, fallback string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return fallback
}

func boolArg(args map[string]any, key string, fallback bool) bool {
	if value, ok := args[key].(bool); ok {
		return value
	}
	return fallback
}

func intArg(args map[string]any, key string, fallback int) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		number, _ := value.Int64()
		return int(number)
	default:
		return fallback
	}
}

func arrayArg(args map[string]any, key string) []any {
	value, _ := args[key].([]any)
	return value
}

func objectArg(args map[string]any, key string) map[string]any {
	value, _ := args[key].(map[string]any)
	return value
}

func stringsArg(args map[string]any, key string) []string {
	var out []string
	for _, value := range arrayArg(args, key) {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func required(args map[string]any, keys ...string) error {
	for _, key := range keys {
		value, ok := args[key]
		if !ok || value == nil || (fmt.Sprint(value) == "") {
			return fmt.Errorf("%s is required", key)
		}
	}
	return nil
}

func capText(value string, max int) (string, bool) {
	if max > 0 && len(value) > max {
		return value[:max], true
	}
	return value, false
}

func names(values ...string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

var errNotFound = errors.New("not found")
