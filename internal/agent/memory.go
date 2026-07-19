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
	"time"

	"codebridge/internal/memory"
	"codebridge/internal/security"
)

func (r *Runtime) handleMemory(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "memory_status":
		return r.memoryStatus(ctx), nil
	case "memory_context":
		if err := required(args, "query"); err != nil {
			return nil, err
		}
		project, cwd, err := r.memoryScope(args)
		if err != nil {
			return nil, err
		}
		request := memory.ContextRequest{
			Query: stringArg(args, "query", ""), Project: project, CWD: cwd,
			AgentID: r.Config.Memory.AgentID, Limit: intArg(args, "limit", 8),
			SessionID:   memorySessionID(ctx),
			TokenBudget: intArg(args, "token_budget", r.Config.Memory.TokenBudget),
		}
		if r.Memory.Capabilities().Context {
			return r.Memory.Context(ctx, request)
		}
		if !r.Memory.Capabilities().Search {
			return nil, errors.New("configured memory provider does not support context or search")
		}
		search, err := r.Memory.Search(ctx, memory.SearchRequest{
			Query: request.Query, Project: request.Project, CWD: request.CWD,
			AgentID: request.AgentID, Limit: request.Limit, Format: "narrative",
			TokenBudget: request.TokenBudget,
		})
		if err != nil {
			return nil, err
		}
		return memory.ContextResult{Provider: search.Provider, Project: search.Project, Query: search.Query,
			Text: search.Context, Memories: search.Memories, Count: search.Count,
			TokenBudget: request.TokenBudget, Truncated: search.Truncated}, nil
	case "memory_search":
		if err := required(args, "query"); err != nil {
			return nil, err
		}
		project, cwd, err := r.memoryScope(args)
		if err != nil {
			return nil, err
		}
		format := stringArg(args, "format", "compact")
		if format != "full" && format != "compact" && format != "narrative" {
			return nil, errors.New("format must be full, compact, or narrative")
		}
		return r.Memory.Search(ctx, memory.SearchRequest{
			Query: stringArg(args, "query", ""), Project: project, CWD: cwd,
			AgentID: r.Config.Memory.AgentID, Limit: intArg(args, "limit", 10), Format: format,
			TokenBudget: intArg(args, "token_budget", r.Config.Memory.TokenBudget),
		})
	case "memory_remember":
		if err := required(args, "content"); err != nil {
			return nil, err
		}
		project, _, err := r.memoryScope(args)
		if err != nil {
			return nil, err
		}
		files, err := r.memoryFiles(stringsArg(args, "files"))
		if err != nil {
			return nil, err
		}
		return r.Memory.Remember(ctx, memory.RememberRequest{
			Content: stringArg(args, "content", ""), Kind: stringArg(args, "kind", "fact"),
			Project: project, AgentID: r.Config.Memory.AgentID,
			SessionID: memorySessionID(ctx),
			Concepts:  stringsArg(args, "concepts"), Files: files,
			TTLDays: intArg(args, "ttl_days", 0),
		})
	case "memory_commit":
		project, _, err := r.memoryScope(args)
		if err != nil {
			return nil, err
		}
		files, err := r.memoryFiles(stringsArg(args, "files"))
		if err != nil {
			return nil, err
		}
		content, derivedFiles := r.memoryCommitContent(ctx, args)
		if content == "" {
			return nil, errors.New("memory_commit has no summary or local session state to store")
		}
		files = uniqueStrings(append(files, derivedFiles...))
		if next := stringsArg(args, "next_steps"); len(next) > 0 {
			content += "\n\nNext steps:\n- " + strings.Join(next, "\n- ")
		}
		concepts := append([]string{"codebridge-session"}, stringsArg(args, "concepts")...)
		return r.Memory.Remember(ctx, memory.RememberRequest{
			Content: content, Kind: "session", Project: project,
			AgentID: r.Config.Memory.AgentID, SessionID: memorySessionID(ctx),
			Concepts: uniqueStrings(concepts), Files: files,
		})
	case "memory_forget":
		memoryID := stringArg(args, "memory_id", "")
		sessionID := stringArg(args, "session_id", "")
		if memoryID == "" && sessionID == "" {
			return nil, errors.New("memory_id or session_id is required")
		}
		return r.Memory.Forget(ctx, memory.ForgetRequest{
			MemoryID: memoryID, SessionID: sessionID,
			ObservationIDs: stringsArg(args, "observation_ids"),
		})
	case "memory_export":
		project, _, err := r.memoryScope(args)
		if err != nil {
			return nil, err
		}
		exporter, ok := r.Memory.(memory.Exporter)
		if !ok {
			return nil, fmt.Errorf("memory provider %q does not support export", r.Memory.Name())
		}
		result, err := exporter.Export(ctx, memory.ExportRequest{Project: project, AgentID: r.Config.Memory.AgentID})
		if err != nil {
			return nil, err
		}
		if stringArg(args, "format", "object") == "jsonl" {
			lines := make([]string, 0, len(result.Memories))
			for _, item := range result.Memories {
				raw, marshalErr := json.Marshal(item)
				if marshalErr != nil {
					return nil, marshalErr
				}
				lines = append(lines, string(raw))
			}
			return map[string]any{
				"schema_version": result.SchemaVersion, "provider": result.Provider,
				"project": result.Project, "count": result.Count,
				"jsonl": strings.Join(lines, "\n") + ternary(len(lines) > 0, "\n", ""),
			}, nil
		}
		return result, nil
	case "memory_import":
		project, _, err := r.memoryScope(args)
		if err != nil {
			return nil, err
		}
		importer, ok := r.Memory.(memory.Importer)
		if !ok {
			return nil, fmt.Errorf("memory provider %q does not support import", r.Memory.Name())
		}
		items, err := decodeMemoryImport(args)
		if err != nil {
			return nil, err
		}
		return importer.Import(ctx, memory.ImportRequest{
			Project: project, AgentID: r.Config.Memory.AgentID, Memories: items,
		})
	default:
		return nil, fmt.Errorf("unsupported memory tool: %s", name)
	}
}

func decodeMemoryImport(args map[string]any) ([]memory.Item, error) {
	var items []memory.Item
	if rawItems, exists := args["memories"]; exists {
		raw, err := json.Marshal(rawItems)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("decode memories: %w", err)
		}
	}
	if jsonl := strings.TrimSpace(stringArg(args, "jsonl", "")); jsonl != "" {
		for lineNumber, line := range strings.Split(jsonl, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var item memory.Item
			if err := json.Unmarshal([]byte(line), &item); err != nil {
				return nil, fmt.Errorf("decode JSONL line %d: %w", lineNumber+1, err)
			}
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return nil, errors.New("memories or jsonl is required")
	}
	if len(items) > 10_000 {
		return nil, errors.New("memory import is limited to 10000 items per call")
	}
	return items, nil
}

var selectedMemoryCaptureTools = names(
	"save_note", "checkpoint", "decision_log", "task_plan", "task_state",
	"write_file", "replace_in_file", "apply_patch", "move_path", "delete_path",
	"run_command", "run_commands", "git", "quality_gate", "run_tests", "run_build", "run_lint", "run_changed_tests",
	"codegraph_explore", "review_diff", "change_summary", "session_report",
)

func (r *Runtime) captureMemoryObservation(sessionID, name string, args map[string]any, value any, callErr error) {
	if r.MemoryRecorder == nil || memoryTools[name] || databaseTools[name] || name == "ping" || name == "proc_output" {
		return
	}
	mode := r.Config.Memory.CaptureMode
	if mode == "off" || (mode == "selected" && !selectedMemoryCaptureTools[name]) {
		return
	}
	project, cwd := r.MemoryProject, r.Workspace.Primary
	pathArg := stringArg(args, "cwd", stringArg(args, "path", "."))
	if root, err := r.Workspace.Resolve(pathArg); err == nil {
		if info, statErr := os.Stat(root); statErr == nil && !info.IsDir() {
			root = filepath.Dir(root)
		} else if statErr != nil && args["path"] != nil && args["cwd"] == nil {
			root = filepath.Dir(root)
		}
		cwd = root
		if owner, ownerErr := r.Workspace.OwningRoot(root); ownerErr == nil {
			project = memory.ResolveProject(owner, r.Config.Memory.ProjectStrategy)
		}
	}
	hookType := "PostToolUse"
	response := memoryCaptureResult(value)
	if callErr != nil {
		hookType = "PostToolUseFailure"
		response = map[string]any{"ok": false, "error": "tool failed; inspect the local Codebridge audit for details"}
	}
	input := memoryCaptureInput(name, args, mode)
	r.MemoryRecorder.Record(memory.ObservationRequest{
		HookType: hookType, SessionID: sessionID, Project: project,
		CWD: cwd, Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Data: map[string]any{
			"hook_event_name": hookType, "tool_name": name,
			"tool_input": input, "tool_response": response,
		},
	})
}

func memoryCaptureInput(name string, args map[string]any, mode string) map[string]any {
	if mode != "selected" {
		input := map[string]any{}
		for _, key := range []string{"path", "cwd", "staged", "recursive", "kind"} {
			if entry, exists := args[key]; exists {
				input[key] = security.RedactDeep(entry, 0)
			}
		}
		return input
	}
	if name == "git" {
		input := map[string]any{}
		if argv := stringsArg(args, "args"); len(argv) > 0 {
			input["operation"] = argv[0]
			input["args_count"] = len(argv)
		}
		if cwd := stringArg(args, "cwd", ""); cwd != "" {
			input["cwd"] = cwd
		}
		return input
	}
	input, _ := security.RedactDeep(args, 0).(map[string]any)
	if input == nil {
		return map[string]any{}
	}
	return input
}

func memoryCaptureResult(value any) map[string]any {
	response := map[string]any{"ok": true}
	result, ok := value.(map[string]any)
	if !ok {
		return response
	}
	for _, key := range []string{
		"ok", "kind", "verdict", "summary", "exit_code", "count", "progress", "status",
		"path", "files", "counts", "goal", "steps_count", "requested", "cached", "fresh", "root",
	} {
		if entry, exists := result[key]; exists {
			response[key] = security.RedactDeep(entry, 0)
		}
	}
	return response
}

func (r *Runtime) memoryStatus(ctx context.Context) map[string]any {
	health := r.memoryHealth(ctx, false)
	return map[string]any{
		"enabled": r.Config.Memory.Enabled, "required": r.Config.Memory.Required,
		"provider": r.Memory.Name(), "project": r.MemoryProject,
		"agent_id": r.Config.Memory.AgentID, "capture_mode": r.Config.Memory.CaptureMode,
		"project_strategy": r.Config.Memory.ProjectStrategy, "health": health,
		"recorder": r.MemoryRecorder.Stats(),
	}
}

func (r *Runtime) memoryHealth(ctx context.Context, force bool) memory.HealthResult {
	r.memoryHealthMu.Lock()
	defer r.memoryHealthMu.Unlock()
	cacheFor := time.Duration(r.Config.Memory.HealthCacheMS) * time.Millisecond
	if !force && !r.memoryHealthAt.IsZero() && time.Since(r.memoryHealthAt) < cacheFor {
		return r.memoryHealthValue
	}
	r.memoryHealthValue = r.Memory.Health(ctx)
	r.memoryHealthAt = time.Now()
	return r.memoryHealthValue
}

func (r *Runtime) memoryScope(args map[string]any) (string, string, error) {
	root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
	if err != nil {
		return "", "", err
	}
	if info, statErr := os.Stat(root); statErr == nil && !info.IsDir() {
		root = filepath.Dir(root)
	}
	owner, err := r.Workspace.OwningRoot(root)
	if err != nil {
		return "", "", err
	}
	project := memory.ResolveProject(owner, r.Config.Memory.ProjectStrategy)
	return project, root, nil
}

func (r *Runtime) memoryCommitContent(ctx context.Context, args map[string]any) (string, []string) {
	var sections []string
	var files []string
	if summary := strings.TrimSpace(stringArg(args, "summary", "")); summary != "" {
		sections = append(sections, summary)
	}
	if boolArg(args, "include_task", true) {
		var task map[string]any
		if readJSONFile(r.statePath("current-task.json"), &task) == nil && len(task) > 0 {
			sections = append(sections, "Task state:\n"+prettyJSON(task))
		}
		var checkpoint map[string]any
		if r.Store.ReadJSON(r.Store.Checkpoint, &checkpoint) == nil && len(checkpoint) > 0 {
			sections = append(sections, "Checkpoint:\n"+prettyJSON(checkpoint))
			files = append(files, stringSliceAny(checkpoint["files_touched"])...)
		}
	}
	path := stringArg(args, "path", ".")
	if boolArg(args, "include_git", true) {
		if value, err := r.changeSummary(ctx, map[string]any{"path": path}); err == nil {
			sections = append(sections, "Git changes:\n"+prettyJSON(value))
			if result, ok := value.(map[string]any); ok {
				if entries, ok := result["files"].([]map[string]any); ok {
					for _, entry := range entries {
						if file := fmt.Sprint(entry["path"]); file != "" {
							files = append(files, file)
						}
					}
				}
			}
		}
	}
	if boolArg(args, "include_review", true) {
		if review, err := r.reviewDiff(ctx, map[string]any{"cwd": path}); err == nil {
			sections = append(sections, "Review:\n"+prettyJSON(review))
		}
	}
	return strings.Join(sections, "\n\n"), uniqueStrings(files)
}

func stringSliceAny(value any) []string {
	var out []string
	switch values := value.(type) {
	case []string:
		return append(out, values...)
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok && text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func (r *Runtime) memoryFiles(values []string) ([]string, error) {
	files := make([]string, 0, len(values))
	for _, value := range values {
		resolved, err := r.Workspace.Resolve(value)
		if err != nil {
			return nil, fmt.Errorf("memory file %q: %w", value, err)
		}
		files = append(files, r.Workspace.Relative(resolved))
	}
	return uniqueStrings(files), nil
}
