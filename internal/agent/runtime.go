// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codebridge/internal/assets"
	"codebridge/internal/config"
	"codebridge/internal/figma"
	"codebridge/internal/patch"
	"codebridge/internal/processx"
	"codebridge/internal/security"
	"codebridge/internal/state"
	"codebridge/internal/workspace"
)

type Runtime struct {
	Config    config.Config
	Workspace *workspace.Manager
	Store     *state.Store
	Approvals *security.ApprovalManager
	Patches   *patch.Engine
	Processes *processx.Registry
	Figma     figma.Client
	Version   string
	Tier      string
	ConfigID  string

	profileMu sync.RWMutex
	profile   map[string]any
}

func New(cfg config.Config, version, tier, configID string) (*Runtime, error) {
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
	store, err := state.New(manager.Primary)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{
		Config: cfg, Workspace: manager, Store: store,
		Approvals: security.NewApprovalManager(store, cfg.ApprovalToken, 10*time.Minute),
		Processes: processx.NewRegistry(cfg.MaxProcesses),
		Figma: figma.Client{
			Endpoint: cfg.FigmaDesktopURL, Timeout: time.Duration(cfg.FigmaDesktopTimeoutMS) * time.Millisecond,
			AllowRemote: cfg.FigmaDesktopAllowRemote, Version: version,
		},
		Version: version, Tier: tier, ConfigID: configID, profile: profile,
	}
	runtime.Patches = &patch.Engine{Workspace: manager, Store: store}
	return runtime, nil
}

func (r *Runtime) Close() { r.Processes.StopAll() }

func (r *Runtime) Handle(ctx context.Context, name string, args map[string]any) (any, error) {
	if args == nil {
		args = map[string]any{}
	}
	if err := r.enforcePolicy(name, args); err != nil {
		r.audit(name, args, false, err)
		return nil, err
	}
	value, err := r.dispatch(ctx, name, args)
	r.audit(name, args, err == nil, err)
	return value, err
}

func (r *Runtime) audit(tool string, args map[string]any, ok bool, callErr error) {
	if !r.Config.Audit {
		return
	}
	record := map[string]any{
		"ts": time.Now().UTC().Format(time.RFC3339Nano), "tool": tool, "ok": ok,
		"workspace": r.Workspace.Primary,
	}
	if r.Config.AuditArgs {
		record["args"] = security.RedactDeep(args, 0)
	}
	if callErr != nil {
		record["error"] = callErr.Error()
	}
	raw, _ := json.Marshal(record)
	_ = r.Store.AppendLine(r.Store.AuditPath, append(raw, '\n'))
}

func (r *Runtime) enforcePolicy(tool string, args map[string]any) error {
	policyTools := map[string]bool{
		"policy_status": true, "explain_risk": true, "request_approval": true,
		"request_approval_batch": true, "approve_request": true, "deny_request": true,
	}
	if policyTools[tool] || r.Config.Policy == "full" {
		return nil
	}
	if r.Config.Policy == "strict" && mutationTools[tool] {
		return fmt.Errorf("tool %q is blocked by policy=strict", tool)
	}
	if r.Config.Policy != "balanced" {
		return nil
	}
	action := approvalAction(tool, args)
	if action == "" {
		return nil
	}
	if err := r.Approvals.Consume(action); err != nil {
		return fmt.Errorf("approval required: call request_approval with action=%q, then approve_request: %w", action, err)
	}
	return nil
}

var mutationTools = map[string]bool{
	"figma_call_tool": true, "save_note": true, "checkpoint": true, "write_file": true,
	"replace_in_file": true, "apply_patch": true, "make_dir": true, "move_path": true,
	"delete_path": true, "run_command": true, "run_commands": true, "proc_start": true,
	"proc_stop": true, "git": true, "create_skill": true, "delete_skill": true,
	"undo_last_patch": true, "quality_gate": true, "run_tests": true, "run_build": true,
	"run_lint": true, "run_changed_tests": true, "task_plan": true, "task_state": true,
	"decision_log": true,
}

func approvalAction(tool string, args map[string]any) string {
	switch tool {
	case "figma_call_tool":
		name := stringArg(args, "tool", "")
		readOnly := map[string]bool{
			"get_code_connect_map": true, "get_code_connect_suggestions": true,
			"get_design_context": true, "get_figjam": true, "get_metadata": true,
			"get_screenshot": true, "get_shader_effect": true, "get_shader_fill": true,
			"get_variable_defs": true, "list_shader_effects": true,
		}
		if name != "" && !readOnly[name] {
			raw, _ := json.Marshal(args["arguments"])
			return "figma:" + name + ":" + string(raw)
		}
	case "delete_path":
		return "delete_path:" + stringArg(args, "path", "")
	case "delete_skill":
		return "delete_skill:" + stringArg(args, "name", "")
	case "run_command", "proc_start":
		command := stringArg(args, "command", "")
		if security.Classify(command).NeedsApproval {
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

func loadProfileFile(root string) map[string]any {
	raw, err := os.ReadFile(filepath.Join(root, ".agent", "profile.json"))
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

func (r *Runtime) dispatch(ctx context.Context, name string, args map[string]any) (any, error) {
	switch {
	case basicTools[name]:
		return r.handleBasic(ctx, name, args)
	case fsTools[name]:
		return r.handleFS(ctx, name, args)
	case execTools[name]:
		return r.handleExec(ctx, name, args)
	case figmaTools[name]:
		return r.handleFigma(ctx, name, args)
	case repoTools[name]:
		return r.handleRepo(ctx, name, args)
	case workflowTools[name]:
		return r.handleWorkflow(ctx, name, args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

var (
	basicTools = names("ping", "workspace_info", "lca", "save_note", "list_notes", "checkpoint", "resume",
		"list_skills", "read_skill", "create_skill", "delete_skill", "workspace_search", "slash_commands",
		"compose_prompt", "lca_input", "policy_status", "explain_risk", "request_approval",
		"request_approval_batch", "approve_request", "deny_request", "profile_status", "reload_profile")
	fsTools = names("list_files", "read_file", "stat_path", "search_text", "find_files", "read_many",
		"repo_overview", "write_file", "replace_in_file", "apply_patch", "make_dir", "move_path",
		"delete_path", "preview_patch", "validate_patch", "undo_last_patch")
	execTools = names("run_command", "run_commands", "proc_start", "proc_list", "proc_output", "proc_stop",
		"git", "git_status", "git_diff")
	figmaTools = names("figma_status", "figma_list_tools", "figma_call_tool", "figma_get_design_context",
		"figma_get_screenshot", "figma_get_metadata", "figma_get_variable_defs", "figma_get_code_connect_map",
		"figma_get_figjam")
	repoTools = names("workspace_doctor", "workspace_snapshot", "project_profile", "important_files",
		"repo_map", "repo_symbols", "index_status", "quality_gate", "detect_test_commands",
		"run_tests", "run_build", "run_lint", "run_changed_tests", "session_report", "review_diff",
		"security_scan", "todo_scan", "change_summary")
	workflowTools = names("task_plan", "task_state", "decision_log")
)

func names(values ...string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func builtInSkills() map[string]string {
	values, _ := assets.Skills()
	return values
}

var errNotFound = errors.New("not found")
