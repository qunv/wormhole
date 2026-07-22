// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import "context"

type basicModule struct {
	builtInModulePolicy
	runtime *Runtime
}

func newBasicModule(runtime *Runtime) ToolModule { return &basicModule{runtime: runtime} }
func (*basicModule) Name() string                { return "basic" }
func (*basicModule) Specs() []ToolSpec           { return sharedModuleSpecs("basic", basicToolSpecs) }
func (m *basicModule) Handle(ctx context.Context, _ CallIdentity, tool string, args map[string]any) (any, error) {
	return m.runtime.handleBasic(ctx, tool, args)
}
func (m *basicModule) Health(context.Context) any { return healthyModule("basic", len(m.Specs())) }
func (*basicModule) Close() error                 { return nil }

func basicToolSpecs() []ToolSpec {
	empty := object(nil)
	return []ToolSpec{
		roSpec("ping", "Ping", "Check whether Codebridge is reachable.", object(map[string]any{"message": str("Optional echo message.")})),
		roSpec("workspace_info", "Workspace info", "Return roots, mode, policy, limits, and safety rules.", empty),
		roSpec("lca", "Codebridge status", "Short alias for workspace_info.", empty),
		roSpec("runtime_metrics", "Runtime metrics", "Return bounded tool-call counts, latency, audit, and recent-call diagnostics without arguments, results, sessions, or error text.", object(map[string]any{"include_tools": boolean(), "recent_limit": integer()})),
		rwSpec("save_note", "Save note", "Save a local workspace note.", object(map[string]any{"title": str("Note title."), "body": str("Note body.")}, "title", "body"), false),
		roSpec("list_notes", "List notes", "List saved workspace notes.", object(map[string]any{"limit": integer()})),
		rwSpec("checkpoint", "Save checkpoint", "Save compact progress for a later chat.", object(map[string]any{"summary": str("Compact progress summary."), "next_steps": array(str("")), "files_touched": array(str(""))}, "summary"), false),
		roSpec("resume", "Resume checkpoint", "Load the latest saved checkpoint.", empty),
		roSpec("workspace_search", "Workspace @ search", "Autocomplete files, folders, and symbols.", object(map[string]any{"query": str("Picker query."), "path": str("Search root."), "include": array(str("")), "limit": integer()})),
		roSpec("slash_commands", "Slash commands", "Autocomplete workflow and mode shortcuts.", object(map[string]any{"query": str("Slash query."), "include": array(str("")), "limit": integer()})),
		roSpec("compose_prompt", "Compose prompt", "Resolve sidebar-style @ context and / workflows into a prompt.", object(map[string]any{"input": str("User input."), "path": str("Workspace root."), "mode": str("Workflow override."), "selected_context": array(str("")), "include_context_pack": boolean()}, "input")),
		{
			Name: "cb_input", Title: "Codebridge input", Description: "Render the compact MCP Apps input widget.",
			ReadOnly: true, Schema: object(map[string]any{"initial_input": str("Optional prefilled text.")}),
			Meta: map[string]any{
				"ui":                    map[string]any{"resourceUri": WidgetURI, "visibility": []string{"model", "app"}},
				"openai/outputTemplate": WidgetURI, "openai/widgetAccessible": true,
				"openai/toolInvocation/invoking": "Opening Codebridge input…",
				"openai/toolInvocation/invoked":  "Codebridge input ready.",
			},
		},
		roSpec("policy_status", "Policy status", "Describe current policy and approval rules.", empty),
		roSpec("explain_risk", "Explain risk", "Classify a proposed action against policy.", object(map[string]any{"action": str("Action or command.")}, "action")),
		rwSpec("request_approval", "Request approval", "Create one exact, expiring approval request.", object(map[string]any{"action": str("Exact action."), "reason": str("Reason.")}, "action", "reason"), false),
		rwSpec("request_approval_batch", "Request batch approval", "Create an expiring request for 2-20 exact actions.", object(map[string]any{"actions": array(str("")), "reason": str("Reason."), "expires_in_minutes": integer()}, "actions", "reason"), false),
		rwSpec("approve_request", "Approve request", "Approve a pending request with the local operator token.", object(map[string]any{"id": str("Request ID."), "approval_token": str("Operator token.")}, "id", "approval_token"), false),
		rwSpec("deny_request", "Deny request", "Deny a pending request with the local operator token.", object(map[string]any{"id": str("Request ID."), "approval_token": str("Operator token.")}, "id", "approval_token"), false),
		roSpec("profile_status", "Profile status", "Return the loaded .agent/profile.json.", empty),
		rwSpec("reload_profile", "Reload profile", "Reload .agent/profile.json from disk.", empty, false),
	}
}
