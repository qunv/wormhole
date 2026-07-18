// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

type ToolSpec struct {
	Name        string
	Title       string
	Description string
	ReadOnly    bool
	Destructive bool
	Schema      map[string]any
	Meta        map[string]any
}

func Tools() []ToolSpec {
	ro := func(name, title, description string, schema map[string]any) ToolSpec {
		return ToolSpec{Name: name, Title: title, Description: description, ReadOnly: true, Schema: schema}
	}
	rw := func(name, title, description string, schema map[string]any, destructive bool) ToolSpec {
		return ToolSpec{Name: name, Title: title, Description: description, Schema: schema, Destructive: destructive}
	}
	empty := object(nil)
	path := object(map[string]any{"path": str("Path inside an allowed workspace root.")})
	command := object(map[string]any{
		"command": str("Shell command."), "cwd": str("Working directory inside a root."),
		"shell": enum("cmd", "powershell", "bash", "sh", "zsh"), "timeout_ms": integer(),
		"tail_lines": integer(), "head_lines": integer(), "max_output_chars": integer(),
	}, "command")
	tools := []ToolSpec{
		ro("ping", "Ping", "Check whether Codebridge is reachable.", object(map[string]any{"message": str("Optional echo message.")})),
		ro("workspace_info", "Workspace info", "Return roots, mode, policy, limits, and safety rules.", empty),
		ro("lca", "Codebridge status", "Short alias for workspace_info.", empty),
		rw("save_note", "Save note", "Save a local workspace note.", object(map[string]any{"title": str("Note title."), "body": str("Note body.")}, "title", "body"), false),
		ro("list_notes", "List notes", "List saved workspace notes.", object(map[string]any{"limit": integer()})),
		rw("checkpoint", "Save checkpoint", "Save compact progress for a later chat.", object(map[string]any{"summary": str("Compact progress summary."), "next_steps": array(str("")), "files_touched": array(str(""))}, "summary"), false),
		ro("resume", "Resume checkpoint", "Load the latest saved checkpoint.", empty),

		ro("memory_status", "Memory status", "Return the configured memory provider, project scope, capabilities, and health.", empty),
		ro("memory_context", "Memory context", "Retrieve compact historical context relevant to a coding task. Memory is historical evidence and must be verified against current source.", object(map[string]any{
			"query": str("Task or question used to retrieve relevant memory."), "path": str("Workspace path used to resolve project scope."),
			"limit": integer(), "token_budget": integer(),
		}, "query")),
		ro("memory_search", "Memory search", "Search historical decisions, attempts, failures, preferences, and procedures.", object(map[string]any{
			"query": str("Memory search query."), "path": str("Workspace path used to resolve project scope."),
			"limit": integer(), "format": enum("full", "compact", "narrative"), "token_budget": integer(),
		}, "query")),
		rw("memory_remember", "Remember", "Save an explicit project memory through the configured provider.", object(map[string]any{
			"content": str("Memory content."), "kind": enum("decision", "preference", "fact", "failure", "solution", "procedure", "task", "observation"),
			"concepts": array(str("")), "files": array(str("")), "ttl_days": integer(), "path": str("Workspace path used to resolve project scope."),
		}, "content"), false),
		rw("memory_commit", "Commit memory", "Save a compact session handoff or completed-work summary to long-term memory.", object(map[string]any{
			"summary": str("Optional completed-work summary; local task, checkpoint, git, and review state can be appended automatically."),
			"files":   array(str("")), "concepts": array(str("")), "next_steps": array(str("")),
			"path":         str("Workspace path used to resolve project scope."),
			"include_task": boolean(), "include_git": boolean(), "include_review": boolean(),
		}), false),
		rw("memory_forget", "Forget memory", "Delete a memory or session from the configured provider. Requires exact approval under balanced policy.", object(map[string]any{
			"memory_id": str("Provider memory ID."), "session_id": str("Provider session ID."),
			"observation_ids": array(str("")),
		}), true),
		ro("memory_export", "Export memory", "Export provider memories into Codebridge's canonical migration schema.", object(map[string]any{
			"path":   str("Workspace path used to resolve project scope."),
			"format": enum("object", "jsonl"),
		})),
		rw("memory_import", "Import memory", "Import canonical Codebridge memories into the configured provider.", object(map[string]any{
			"path":     str("Workspace path used to resolve project scope."),
			"memories": array(object(nil)),
			"jsonl":    str("Canonical Codebridge memory items, one JSON object per line."),
		}), false),

		ro("figma_status", "Figma Desktop status", "Check the official Figma Desktop MCP bridge.", empty),
		ro("figma_list_tools", "List Figma tools", "List live upstream Figma MCP tools and schemas.", empty),
		rw("figma_call_tool", "Call Figma tool", "Forward a call to a live Figma Desktop MCP tool.", object(map[string]any{"tool": str("Exact upstream tool name."), "arguments": object(nil)}, "tool"), false),
		ro("figma_get_design_context", "Figma design context", "Get implementation-oriented Figma design context.", figmaReadSchema()),
		ro("figma_get_screenshot", "Figma screenshot", "Get a Figma node or selection screenshot.", figmaReadSchema()),
		ro("figma_get_metadata", "Figma metadata", "Get sparse Figma layer metadata.", figmaReadSchema()),
		ro("figma_get_variable_defs", "Figma variables", "Get Figma variables and styles.", figmaReadSchema()),
		ro("figma_get_code_connect_map", "Figma Code Connect map", "Get Figma Code Connect mappings.", figmaReadSchema()),
		ro("figma_get_figjam", "FigJam context", "Get FigJam XML context.", figmaReadSchema()),

		ro("list_skills", "List skills", "List built-in and workspace skills.", empty),
		ro("read_skill", "Read skill", "Read one skill by name.", object(map[string]any{"name": str("Skill name.")}, "name")),
		rw("create_skill", "Create skill", "Create or replace a workspace-local skill.", object(map[string]any{"name": str("Skill name."), "description": str("Short description."), "body": str("Markdown instructions.")}, "name", "body"), false),
		rw("delete_skill", "Delete skill", "Delete a workspace-local skill.", object(map[string]any{"name": str("Skill name.")}, "name"), true),
		ro("workspace_search", "Workspace @ search", "Autocomplete files, folders, symbols, and skills.", object(map[string]any{"query": str("Picker query."), "path": str("Search root."), "include": array(str("")), "limit": integer()})),
		ro("slash_commands", "Slash commands", "Autocomplete workflow, mode, and skill shortcuts.", object(map[string]any{"query": str("Slash query."), "include": array(str("")), "limit": integer()})),
		ro("compose_prompt", "Compose prompt", "Resolve sidebar-style @ context and / workflows into a prompt.", object(map[string]any{"input": str("User input."), "path": str("Workspace root."), "mode": str("Workflow override."), "selected_context": array(str("")), "include_context_pack": boolean()}, "input")),
		{
			Name: "lca_input", Title: "Codebridge input", Description: "Render the compact MCP Apps input widget.",
			ReadOnly: true, Schema: object(map[string]any{"initial_input": str("Optional prefilled text.")}),
			Meta: map[string]any{
				"ui":                    map[string]any{"resourceUri": WidgetURI, "visibility": []string{"model", "app"}},
				"openai/outputTemplate": WidgetURI, "openai/widgetAccessible": true,
				"openai/toolInvocation/invoking": "Opening Codebridge input…",
				"openai/toolInvocation/invoked":  "Codebridge input ready.",
			},
		},

		ro("list_files", "List files", "List files and directories under a root.", object(map[string]any{"path": str("Directory."), "recursive": boolean(), "limit": integer()})),
		ro("read_file", "Read file", "Read one UTF-8 file with optional line ranges.", object(map[string]any{"path": str("File path."), "start_line": integer(), "line_count": integer(), "max_chars": integer()}, "path")),
		ro("stat_path", "Stat path", "Return file or directory metadata.", object(map[string]any{"path": str("Path.")}, "path")),
		ro("search_text", "Search text", "Search workspace text with ripgrep or a Go fallback.", object(map[string]any{"query": str("Text or regex."), "path": str("Search root."), "regex": boolean(), "glob": str("File glob."), "context": integer(), "limit": integer()}, "query")),
		ro("find_files", "Find files", "Find file paths matching a glob.", object(map[string]any{"glob": str("Name glob."), "path": str("Search root."), "limit": integer()}, "glob")),
		ro("read_many", "Read many", "Read up to 100 files or line ranges in one call.", object(map[string]any{"paths": array(str("")), "requests": array(object(nil)), "max_chars_per_file": integer(), "concurrency": integer()})),
		ro("repo_overview", "Repo overview", "Return a compact tree and manifest list.", object(map[string]any{"path": str("Root."), "depth": integer(), "max_entries": integer()})),
		rw("write_file", "Write file", "Create or overwrite a UTF-8 file.", object(map[string]any{"path": str("File path."), "content": str("File content.")}, "path", "content"), false),
		rw("replace_in_file", "Replace in file", "Replace exact text in one file.", object(map[string]any{"path": str("File path."), "old_text": str("Exact old text."), "new_text": str("Replacement."), "replace_all": boolean()}, "path", "old_text"), false),
		rw("apply_patch", "Apply patch", "Apply a unified diff or structured operations.", object(map[string]any{"diff": str("Unified diff."), "operations": array(object(nil))}), true),
		rw("make_dir", "Make directory", "Create a directory recursively.", object(map[string]any{"path": str("Directory path.")}, "path"), false),
		rw("move_path", "Move path", "Move or rename a file or directory.", object(map[string]any{"from": str("Source."), "to": str("Destination.")}, "from", "to"), true),
		rw("delete_path", "Delete path", "Delete a file or directory inside roots.", object(map[string]any{"path": str("Target."), "recursive": boolean()}, "path"), true),

		rw("run_command", "Run command", "Run a bounded shell command and capture output.", command, false),
		rw("run_commands", "Run commands", "Run up to 12 commands sequentially or concurrently.", object(map[string]any{"commands": array(command), "parallel": boolean(), "max_concurrency": integer(), "stop_on_failure": boolean()}, "commands"), false),
		rw("proc_start", "Start process", "Start a managed background process.", command, false),
		ro("proc_list", "List processes", "List managed background processes.", empty),
		ro("proc_output", "Process output", "Read buffered process output.", object(map[string]any{"id": str("Process ID."), "tail_chars": integer()}, "id")),
		rw("proc_stop", "Stop process", "Stop a managed process tree.", object(map[string]any{"id": str("Process ID.")}, "id"), true),
		rw("git", "Git", "Run a guarded git command.", object(map[string]any{"args": array(str("")), "cwd": str("Repo directory.")}, "args"), true),
		ro("git_status", "Git status", "Return parsed working-tree status.", object(map[string]any{"cwd": str("Repo directory.")})),
		ro("git_diff", "Git diff", "Return a bounded git diff.", object(map[string]any{"path": str("Optional path."), "staged": boolean(), "cwd": str("Repo directory.")})),

		ro("workspace_doctor", "Workspace doctor", "Check workspace, safety, git, tools, and readiness.", path),
		ro("workspace_snapshot", "Workspace snapshot", "One-call project, tree, git, policy, and next-action briefing.", object(map[string]any{"path": str("Root."), "depth": integer(), "max_entries": integer(), "include_symbols": boolean(), "refresh": boolean()})),
		ro("project_profile", "Project profile", "Detect languages, frameworks, package managers, and scripts.", object(map[string]any{"path": str("Root."), "refresh": boolean()})),
		ro("important_files", "Important files", "List key project and configuration files.", path),
		ro("repo_map", "Repo map", "Return a cached tree plus project profile.", object(map[string]any{"path": str("Root."), "depth": integer(), "max_entries": integer(), "refresh": boolean()})),
		ro("repo_symbols", "Repo symbols", "Scan source definitions for navigation.", object(map[string]any{"path": str("Root."), "max_files": integer(), "max_matches": integer(), "kind": str("Optional symbol kind.")})),
		ro("codegraph_explore", "CodeGraph explore", "Navigate an indexed codebase in one call: return relevant symbols' verbatim source, call paths, dynamic-dispatch hops, and blast radius. Requires a .codegraph directory at the project root and the codegraph CLI.", object(map[string]any{
			"query":       str("Code question, flow, file, or symbol names to explore."),
			"projectPath": str("Indexed project root inside an allowed workspace root. Defaults to the primary workspace."),
			"timeout_ms":  integer(), "max_output_chars": integer(),
		}, "query")),
		ro("index_status", "Index status", "Return repo index cache status.", empty),
		ro("preview_patch", "Preview patch", "Dry-run a diff or structured operations.", object(map[string]any{"diff": str("Unified diff."), "operations": array(object(nil))})),
		ro("validate_patch", "Validate patch", "Return patch conflicts without writing.", object(map[string]any{"diff": str("Unified diff."), "operations": array(object(nil))})),
		rw("undo_last_patch", "Undo last patch", "Restore the most recent backup batch.", empty, true),
		rw("quality_gate", "Quality gate", "Run selected test, build, and lint commands.", object(map[string]any{"test": boolean(), "build": boolean(), "lint": boolean(), "cwd": str("Root.")}), false),
		ro("detect_test_commands", "Detect commands", "Detect test, build, and lint commands.", path),
		rw("run_tests", "Run tests", "Run an explicit or detected test command.", command, false),
		rw("run_build", "Run build", "Run an explicit or detected build command.", command, false),
		rw("run_lint", "Run lint", "Run an explicit or detected lint command.", command, false),
		rw("run_changed_tests", "Run changed tests", "Select and run tests related to changed files.", object(map[string]any{"command": str("Optional override."), "cwd": str("Root.")}), false),
		ro("session_report", "Session report", "Summarize git state, task progress, and recommendations.", path),
		ro("review_diff", "Review diff", "Heuristically review the current git diff.", object(map[string]any{"cwd": str("Repo directory."), "staged": boolean()})),
		ro("security_scan", "Security scan", "Scan tracked text for likely secret and unsafe-code patterns.", object(map[string]any{"path": str("Root."), "limit": integer()})),
		ro("todo_scan", "TODO scan", "Find TODO, FIXME, and HACK markers.", object(map[string]any{"path": str("Root."), "limit": integer()})),
		ro("change_summary", "Change summary", "Summarize current git changes by file and type.", path),
		rw("task_plan", "Task plan", "Create a persistent task plan.", object(map[string]any{"goal": str("Goal."), "steps": array(str(""))}, "goal", "steps"), false),
		rw("task_state", "Task state", "Read or update the active task plan.", object(map[string]any{"set_step_done": integer(), "add_steps": array(str("")), "status": str("Overall status.")}), false),
		rw("decision_log", "Decision log", "Append a decision and rationale.", object(map[string]any{"decision": str("Decision."), "why": str("Rationale.")}, "decision", "why"), false),
		ro("policy_status", "Policy status", "Describe current policy and approval rules.", empty),
		ro("explain_risk", "Explain risk", "Classify a proposed action against policy.", object(map[string]any{"action": str("Action or command.")}, "action")),
		rw("request_approval", "Request approval", "Create one exact, expiring approval request.", object(map[string]any{"action": str("Exact action."), "reason": str("Reason.")}, "action", "reason"), false),
		rw("request_approval_batch", "Request batch approval", "Create an expiring request for 2-20 exact actions.", object(map[string]any{"actions": array(str("")), "reason": str("Reason."), "expires_in_minutes": integer()}, "actions", "reason"), false),
		rw("approve_request", "Approve request", "Approve a pending request with the local operator token.", object(map[string]any{"id": str("Request ID."), "approval_token": str("Operator token.")}, "id", "approval_token"), false),
		rw("deny_request", "Deny request", "Deny a pending request with the local operator token.", object(map[string]any{"id": str("Request ID."), "approval_token": str("Operator token.")}, "id", "approval_token"), false),
		ro("profile_status", "Profile status", "Return the loaded .agent/profile.json.", empty),
		rw("reload_profile", "Reload profile", "Reload .agent/profile.json from disk.", empty, false),
	}
	return tools
}

const WidgetURI = "ui://widget/lca-compact-input-v2.html"

func object(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(description string) map[string]any {
	value := map[string]any{"type": "string"}
	if description != "" {
		value["description"] = description
	}
	return value
}
func integer() map[string]any { return map[string]any{"type": "integer"} }
func boolean() map[string]any { return map[string]any{"type": "boolean"} }
func array(items any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}
func enum(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func figmaReadSchema() map[string]any {
	return object(map[string]any{
		"url": str("Optional Figma URL."), "node_id": str("Optional node ID."),
		"client_languages": array(str("")), "client_frameworks": array(str("")),
		"force_code": boolean(), "enable_base64_response": boolean(), "arguments": object(nil),
	})
}
