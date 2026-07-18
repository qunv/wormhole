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
	"regexp"
	"sort"
	"strings"
	"time"

	"codebridge/internal/security"
)

type note struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

func (r *Runtime) handleBasic(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "ping":
		message := stringArg(args, "message", "")
		text := fmt.Sprintf("Codebridge online (mode=%s).", r.Config.Mode)
		if message != "" {
			text += " Echo: " + message
		}
		return text, nil
	case "workspace_info", "lca":
		return r.workspaceInfo(), nil
	case "save_note":
		if err := required(args, "title", "body"); err != nil {
			return nil, err
		}
		var notes []note
		_ = r.Store.ReadJSON(r.Store.NotesPath, &notes)
		item := note{
			ID: fmt.Sprintf("note-%d", time.Now().UnixNano()), Title: stringArg(args, "title", ""),
			Body: stringArg(args, "body", ""), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		notes = append([]note{item}, notes...)
		if err := r.Store.WriteJSON(r.Store.NotesPath, notes); err != nil {
			return nil, err
		}
		return fmt.Sprintf("Saved note %q (%s).", item.Title, item.ID), nil
	case "list_notes":
		var notes []note
		if err := r.Store.ReadJSON(r.Store.NotesPath, &notes); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		limit := intArg(args, "limit", 10)
		if limit < len(notes) {
			notes = notes[:limit]
		}
		return map[string]any{"count": len(notes), "notes": notes}, nil
	case "checkpoint":
		if err := required(args, "summary"); err != nil {
			return nil, err
		}
		value := map[string]any{
			"saved_at": time.Now().UTC().Format(time.RFC3339Nano),
			"summary":  stringArg(args, "summary", ""), "next_steps": stringsArg(args, "next_steps"),
			"files_touched": stringsArg(args, "files_touched"),
		}
		if err := r.Store.WriteJSON(r.Store.Checkpoint, value); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "message": "Checkpoint saved. Open a fresh chat and call resume."}, nil
	case "resume":
		var value map[string]any
		if err := r.Store.ReadJSON(r.Store.Checkpoint, &value); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return map[string]any{"found": false, "message": "No checkpoint saved yet."}, nil
			}
			return nil, err
		}
		return value, nil
	case "list_skills":
		return r.listSkills()
	case "read_skill":
		return r.readSkill(stringArg(args, "name", ""))
	case "create_skill":
		return r.createSkill(stringArg(args, "name", ""), stringArg(args, "description", ""), stringArg(args, "body", ""))
	case "delete_skill":
		return r.deleteSkill(stringArg(args, "name", ""))
	case "workspace_search":
		return r.workspaceSearch(args)
	case "slash_commands":
		return r.slashCommands(args), nil
	case "compose_prompt":
		return r.composePrompt(args)
	case "lca_input":
		return map[string]any{
			"initial_input": stringArg(args, "initial_input", ""), "workspace": r.Workspace.Primary,
			"shortcuts": workflowCommands(),
		}, nil
	case "policy_status":
		return r.policyStatus(), nil
	case "explain_risk":
		if err := required(args, "action"); err != nil {
			return nil, err
		}
		return security.ExplainRisk(stringArg(args, "action", ""), r.Config.Policy), nil
	case "request_approval":
		if err := required(args, "action", "reason"); err != nil {
			return nil, err
		}
		record, err := r.Approvals.Request([]string{stringArg(args, "action", "")}, stringArg(args, "reason", ""), 0)
		if err != nil {
			return nil, err
		}
		return record, nil
	case "request_approval_batch":
		actions := stringsArg(args, "actions")
		if len(actions) < 2 || len(actions) > 20 {
			return nil, errors.New("actions must contain 2-20 exact actions")
		}
		if len(uniqueStrings(actions)) < 2 {
			return nil, errors.New("actions must contain at least two distinct exact actions")
		}
		if stringArg(args, "reason", "") == "" {
			return nil, errors.New("reason is required")
		}
		ttl := time.Duration(intArg(args, "expires_in_minutes", 10)) * time.Minute
		return r.Approvals.Request(actions, stringArg(args, "reason", ""), ttl)
	case "approve_request", "deny_request":
		decision := "approved"
		if name == "deny_request" {
			decision = "denied"
		}
		return r.Approvals.Decide(stringArg(args, "id", ""), stringArg(args, "approval_token", ""), decision)
	case "profile_status":
		profile := r.currentProfile()
		path := filepath.Join(r.Workspace.Primary, ".agent", "profile.json")
		if profile == nil {
			return map[string]any{
				"loaded": false, "path": path,
				"message": "No profile.json found.",
				"schema": map[string]any{
					"mode": "safe|full", "policy": "strict|balanced|full", "extraRoots": []string{},
					"testCommands": map[string]string{"test": "", "build": "", "lint": ""},
					"ignoredDirs":  []string{}, "conventions": "string", "description": "string",
				},
			}, nil
		}
		return map[string]any{"loaded": true, "path": path, "profile": profile}, nil
	case "reload_profile":
		profile := r.reloadProfile()
		return map[string]any{"ok": true, "loaded": profile != nil, "profile": profile}, nil
	default:
		return nil, fmt.Errorf("unsupported basic tool: %s", name)
	}
}

func (r *Runtime) workspaceInfo() map[string]any {
	return map[string]any{
		"name": "Codebridge", "version": r.Version, "tier": r.Tier,
		"primary_root": r.Workspace.Primary, "roots": r.Workspace.Roots,
		"mode": r.Config.Mode, "policy": r.Config.Policy, "host": r.Config.Host,
		"port": r.Config.Port, "auth": ternary(r.Config.AuthToken != "", "bearer", "none"),
		"config_id": r.ConfigID,
		"memory": map[string]any{
			"enabled": r.Config.Memory.Enabled, "provider": r.Memory.Name(),
			"project": r.MemoryProject, "capture_mode": r.Config.Memory.CaptureMode,
		},
		"limits": map[string]any{
			"max_read_chars": r.Config.MaxReadChars, "max_batch_read_chars": r.Config.MaxBatchReadChars,
			"max_command_output": r.Config.MaxCommandOutput, "max_processes": r.Config.MaxProcesses,
		},
		"safety": map[string]any{
			"file_tools_root_confined": true, "command_cwd_root_confined": true,
			"command_os_sandbox": false, "symlink_escape_blocked": true,
		},
	}
}

var skillName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func (r *Runtime) skillsDir() string { return filepath.Join(r.Workspace.Primary, ".agent", "skills") }

func (r *Runtime) listSkills() (any, error) {
	type skill struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	var out []skill
	for name := range builtInSkills() {
		out = append(out, skill{Name: name, Source: "embedded"})
	}
	entries, _ := os.ReadDir(r.skillsDir())
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			out = append(out, skill{Name: strings.TrimSuffix(entry.Name(), ".md"), Source: "workspace"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return map[string]any{"count": len(out), "skills": out}, nil
}

func (r *Runtime) readSkill(name string) (any, error) {
	if !skillName.MatchString(name) {
		return nil, errors.New("invalid skill name")
	}
	if raw, err := os.ReadFile(filepath.Join(r.skillsDir(), name+".md")); err == nil {
		return map[string]any{"name": name, "source": "workspace", "body": string(raw)}, nil
	}
	if body, ok := builtInSkills()[name]; ok {
		return map[string]any{"name": name, "source": "embedded", "body": body}, nil
	}
	return nil, fmt.Errorf("skill not found: %s", name)
}

func (r *Runtime) createSkill(name, description, body string) (any, error) {
	if !skillName.MatchString(name) {
		return nil, errors.New("skill name must use lowercase letters, digits, hyphen, or underscore")
	}
	if body == "" {
		return nil, errors.New("body is required")
	}
	if description != "" && !strings.Contains(body, description) {
		body = "# " + name + "\n\n" + description + "\n\n" + body
	}
	if err := os.MkdirAll(r.skillsDir(), 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(r.skillsDir(), name+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "name": name, "path": r.Workspace.Relative(path)}, nil
}

func (r *Runtime) deleteSkill(name string) (any, error) {
	if !skillName.MatchString(name) {
		return nil, errors.New("invalid skill name")
	}
	path := filepath.Join(r.skillsDir(), name+".md")
	if err := os.Remove(path); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "name": name}, nil
}

func (r *Runtime) workspaceSearch(args map[string]any) (any, error) {
	query := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(stringArg(args, "query", ""))), "@")
	root := stringArg(args, "path", ".")
	limit := intArg(args, "limit", 30)
	entries, err := r.Workspace.List(root, true, max(limit*8, 200))
	if err != nil {
		return nil, err
	}
	var results []map[string]any
	for _, entry := range entries {
		if query != "" && !strings.Contains(strings.ToLower(entry.Path), query) {
			continue
		}
		kind := "file"
		if entry.Type == "directory" {
			kind = "folder"
		}
		results = append(results, map[string]any{"kind": kind, "label": entry.Path, "value": entry.Path})
		if len(results) >= limit {
			break
		}
	}
	if len(results) < limit {
		for name := range builtInSkills() {
			if query == "" || strings.Contains(name, query) {
				results = append(results, map[string]any{"kind": "skill", "label": name, "value": "skill:" + name})
			}
		}
	}
	return map[string]any{"query": query, "count": len(results), "results": results}, nil
}

func workflowCommands() []map[string]string {
	return []map[string]string{
		{"name": "plan", "command": "/plan", "label": "Plan", "description": "Analyze and plan before editing.", "type": "workflow"},
		{"name": "debug", "command": "/debug", "label": "Debug", "description": "Reproduce and isolate a bug.", "type": "workflow"},
		{"name": "review", "command": "/review", "label": "Review", "description": "Review the current diff.", "type": "workflow"},
		{"name": "safe", "command": "/safe", "label": "Safe", "description": "Prefer read-only and low-risk actions.", "type": "mode"},
		{"name": "full", "command": "/full", "label": "Full", "description": "Use the configured full workflow.", "type": "mode"},
	}
}

func (r *Runtime) slashCommands(args map[string]any) any {
	query := strings.TrimPrefix(strings.ToLower(stringArg(args, "query", "")), "/")
	limit := intArg(args, "limit", 30)
	var results []map[string]string
	for _, item := range workflowCommands() {
		if query == "" || strings.Contains(item["name"], query) {
			results = append(results, item)
		}
	}
	for name := range builtInSkills() {
		if query == "" || strings.Contains(name, query) {
			results = append(results, map[string]string{
				"name": "skill:" + name, "command": "/skill:" + name,
				"label": name, "description": "Load skill " + name, "type": "skill",
			})
		}
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return map[string]any{"query": query, "count": len(results), "commands": results}
}

func (r *Runtime) composePrompt(args map[string]any) (any, error) {
	input := strings.TrimSpace(stringArg(args, "input", ""))
	if input == "" {
		return nil, errors.New("input is required")
	}
	mode := stringArg(args, "mode", "")
	if mode == "" {
		for _, item := range workflowCommands() {
			if strings.Contains(input, item["command"]) {
				mode = item["name"]
				break
			}
		}
	}
	contexts := stringsArg(args, "selected_context")
	mentionPattern := regexp.MustCompile(`@([A-Za-z0-9_./\\-]+)`)
	for _, match := range mentionPattern.FindAllStringSubmatch(input, -1) {
		if len(match) > 1 {
			if path, err := r.Workspace.Resolve(match[1]); err == nil {
				contexts = append(contexts, r.Workspace.Relative(path))
			}
		}
	}
	contexts = uniqueStrings(contexts)
	var builder strings.Builder
	if mode != "" {
		builder.WriteString("Workflow: ")
		builder.WriteString(mode)
		builder.WriteString("\n")
	}
	if boolArg(args, "include_context_pack", true) {
		builder.WriteString("Start with workspace_snapshot and targeted context tools.\n")
	}
	if len(contexts) > 0 {
		builder.WriteString("Selected context:\n")
		for _, value := range contexts {
			builder.WriteString("- ")
			builder.WriteString(value)
			builder.WriteByte('\n')
		}
	}
	builder.WriteString("\nTask:\n")
	builder.WriteString(input)
	return map[string]any{"prompt": builder.String(), "mode": mode, "context": contexts, "input": input}, nil
}

func (r *Runtime) policyStatus() map[string]any {
	status := map[string]any{
		"policy": r.Config.Policy, "mode": r.Config.Mode, "approval_ttl_minutes": 10,
	}
	switch r.Config.Policy {
	case "strict":
		status["description"] = "Read and analyze only; mutation tools are blocked."
		status["allowed"] = []string{"read", "search", "analyze"}
		status["blocked"] = []string{"writes", "commands", "processes", "git mutations", "generic Figma passthrough"}
	case "balanced":
		status["description"] = "Read and edit allowed; exact risky actions need one-time approval."
		status["allowed"] = []string{"read", "write", "edit", "local checks"}
		status["needs_approval"] = []string{"delete", "install", "network", "git mutations", "mutating Figma calls"}
	default:
		status["description"] = "Full project access; catastrophic system commands remain blocked."
		status["allowed"] = []string{"*"}
	}
	return status
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func ternary[T any](condition bool, yes, no T) T {
	if condition {
		return yes
	}
	return no
}

func prettyJSON(value any) string {
	raw, _ := json.MarshalIndent(value, "", "  ")
	return string(raw)
}
