// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import "context"

type repoModule struct {
	builtInModulePolicy
	runtime *Runtime
}

func newRepoModule(runtime *Runtime) ToolModule { return &repoModule{runtime: runtime} }
func (*repoModule) Name() string                { return "repo" }
func (*repoModule) Specs() []ToolSpec           { return sharedModuleSpecs("repo", repoToolSpecs) }
func (m *repoModule) Handle(ctx context.Context, _ CallIdentity, tool string, args map[string]any) (any, error) {
	return m.runtime.handleRepo(ctx, tool, args)
}
func (m *repoModule) Health(context.Context) any {
	return map[string]any{
		"module": "repo", "available": true, "tools": len(m.Specs()),
		"ripgrep": m.runtime.Workspace.RGBin != "",
	}
}
func (*repoModule) Close() error { return nil }

func repoToolSpecs() []ToolSpec {
	path := object(map[string]any{"path": str("Path inside an allowed workspace root.")})
	command := commandSchema()
	return []ToolSpec{
		roSpec("workspace_doctor", "Workspace doctor", "Check workspace, safety, git, tools, and readiness.", path),
		structuredROSpec("workspace_snapshot", "Workspace snapshot", "Compact project, tree, git, policy, and next-action briefing with an explicit detail and token budget.", object(map[string]any{
			"path": str("Root."), "depth": integer(), "max_entries": integer(),
			"include_symbols": boolean(), "include_memory": boolean(), "refresh": boolean(),
			"detail_level": enum("compact", "normal", "full"), "token_budget": integer(),
		})),
		structuredROSpec("task_context", "Task context", "Build a bounded coding context in one call from repository inventory, CodeGraph, targeted search and reads, git, and optional memory.", object(map[string]any{
			"query": str("Coding task or code question."), "path": str("Root."),
			"detail_level": enum("compact", "normal", "full"), "token_budget": integer(),
			"depth": integer(), "max_entries": integer(), "search_limit": integer(), "max_read_files": integer(),
			"include_codegraph": boolean(), "include_git": boolean(), "include_memory": boolean(),
			"refresh": boolean(), "timeout_ms": integer(),
		}, "query")),
		roSpec("project_profile", "Project profile", "Detect languages, frameworks, package managers, and scripts.", object(map[string]any{"path": str("Root."), "refresh": boolean()})),
		roSpec("important_files", "Important files", "List key project and configuration files.", path),
		structuredROSpec("repo_map", "Repo map", "Return a cached tree plus project profile.", object(map[string]any{"path": str("Root."), "depth": integer(), "max_entries": integer(), "refresh": boolean()})),
		structuredROSpec("repo_symbols", "Repo symbols", "Scan source definitions for navigation.", object(map[string]any{"path": str("Root."), "max_files": integer(), "max_matches": integer(), "kind": str("Optional symbol kind.")})),
		roSpec("codegraph_explore", "CodeGraph explore", "Navigate an indexed codebase in one call: return relevant symbols' verbatim source, call paths, dynamic-dispatch hops, and blast radius. Requires a .codegraph directory at the project root and the codegraph CLI.", object(map[string]any{
			"query":       str("Code question, flow, file, or symbol names to explore."),
			"projectPath": str("Indexed project root inside an allowed workspace root. Defaults to the primary workspace."),
			"timeout_ms":  integer(), "max_output_chars": integer(),
			"detail_level": enum("compact", "normal", "full"), "token_budget": integer(),
		}, "query")),
		roSpec("index_status", "Index status", "Return repo index cache status.", object(nil)),
		rwSpec("quality_gate", "Quality gate", "Run selected test, build, and lint commands.", object(map[string]any{"test": boolean(), "build": boolean(), "lint": boolean(), "cwd": str("Root.")}), false),
		roSpec("detect_test_commands", "Detect commands", "Detect test, build, and lint commands.", path),
		rwSpec("run_tests", "Run tests", "Run the detected test command. Explicit overrides require mode=full and exact approval under balanced policy.", command, false),
		rwSpec("run_build", "Run build", "Run the detected build command. Explicit overrides require mode=full and exact approval under balanced policy.", command, false),
		rwSpec("run_lint", "Run lint", "Run the detected lint command. Explicit overrides require mode=full and exact approval under balanced policy.", command, false),
		rwSpec("run_changed_tests", "Run changed tests", "Select tests related to changed files. Explicit overrides require mode=full and exact approval under balanced policy.", object(map[string]any{"command": str("Optional override."), "cwd": str("Root.")}), false),
		roSpec("session_report", "Session report", "Summarize git state, task progress, and recommendations.", path),
		roSpec("review_diff", "Review diff", "Heuristically review the current git diff.", object(map[string]any{"cwd": str("Repo directory."), "staged": boolean()})),
		roSpec("security_scan", "Security scan", "Scan tracked text for likely secret and unsafe-code patterns.", object(map[string]any{"path": str("Root."), "limit": integer()})),
		roSpec("todo_scan", "TODO scan", "Find TODO, FIXME, and HACK markers.", object(map[string]any{"path": str("Root."), "limit": integer()})),
		roSpec("change_summary", "Change summary", "Summarize current git changes by file and type.", path),
	}
}
