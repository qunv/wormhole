// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import "context"

type memoryModule struct {
	builtInModulePolicy
	runtime *Runtime
}

func newMemoryModule(runtime *Runtime) ToolModule { return &memoryModule{runtime: runtime} }
func (*memoryModule) Name() string                { return "memory" }
func (*memoryModule) Specs() []ToolSpec           { return sharedModuleSpecs("memory", memoryToolSpecs) }
func (m *memoryModule) Handle(ctx context.Context, _ CallIdentity, tool string, args map[string]any) (any, error) {
	return m.runtime.handleMemory(ctx, tool, args)
}
func (m *memoryModule) Health(ctx context.Context) any {
	health := m.runtime.memoryHealth(ctx, false)
	return map[string]any{
		"module": "memory", "tools": len(m.Specs()),
		"enabled":   m.runtime.Config.Memory.Enabled,
		"available": !m.runtime.Config.Memory.Enabled || health.Available,
		"provider":  m.runtime.Memory.Name(), "health": health,
	}
}

// SharedServices owns recorder and provider shutdown. Closing a workspace
// module must not drain or disconnect resources still used by another runtime.
func (*memoryModule) Close() error { return nil }

func memoryToolSpecs() []ToolSpec {
	empty := object(nil)
	return []ToolSpec{
		roSpec("memory_status", "Memory status", "Return the configured memory provider, project scope, capabilities, and health.", empty),
		roSpec("memory_context", "Memory context", "Retrieve compact historical context relevant to a coding task. Memory is historical evidence and must be verified against current source.", object(map[string]any{
			"query": str("Task or question used to retrieve relevant memory."), "path": str("Workspace path used to resolve project scope."),
			"limit": integer(), "token_budget": integer(),
		}, "query")),
		roSpec("memory_search", "Memory search", "Search historical decisions, attempts, failures, preferences, and procedures.", object(map[string]any{
			"query": str("Memory search query."), "path": str("Workspace path used to resolve project scope."),
			"limit": integer(), "format": enum("full", "compact", "narrative"), "token_budget": integer(),
		}, "query")),
		rwSpec("memory_remember", "Remember", "Save an explicit project memory through the configured provider.", object(map[string]any{
			"content": str("Memory content."), "kind": enum("decision", "preference", "fact", "failure", "solution", "procedure", "task", "observation"),
			"concepts": array(str("")), "files": array(str("")), "ttl_days": integer(), "path": str("Workspace path used to resolve project scope."),
		}, "content"), false),
		rwSpec("memory_commit", "Commit memory", "Save a compact session handoff or completed-work summary to long-term memory.", object(map[string]any{
			"summary": str("Optional completed-work summary; local task, checkpoint, git, and review state can be appended automatically."),
			"files":   array(str("")), "concepts": array(str("")), "next_steps": array(str("")),
			"path":         str("Workspace path used to resolve project scope."),
			"include_task": boolean(), "include_git": boolean(), "include_review": boolean(),
		}), false),
		rwSpec("memory_forget", "Forget memory", "Delete a memory or session from the configured provider. Requires exact approval under balanced policy.", object(map[string]any{
			"memory_id": str("Provider memory ID."), "session_id": str("Provider session ID."),
			"observation_ids": array(str("")),
		}), true),
		roSpec("memory_export", "Export memory", "Export provider memories into Codebridge's canonical migration schema.", object(map[string]any{
			"path": str("Workspace path used to resolve project scope."), "format": enum("object", "jsonl"),
		})),
		rwSpec("memory_import", "Import memory", "Import canonical Codebridge memories into the configured provider.", object(map[string]any{
			"path": str("Workspace path used to resolve project scope."), "memories": array(memoryItemSchema()),
			"jsonl": str("Canonical Codebridge memory items, one JSON object per line."),
		}), false),
	}
}
