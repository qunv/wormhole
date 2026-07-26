// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

type ToolOutputMode string

const (
	ToolOutputBoth       ToolOutputMode = "both"
	ToolOutputStructured ToolOutputMode = "structured"
	ToolOutputText       ToolOutputMode = "text"
)

type ToolSpec struct {
	Name        string
	Title       string
	Description string
	ReadOnly    bool
	Destructive bool
	OpenWorld   bool
	OutputMode  ToolOutputMode
	Schema      map[string]any
	Meta        map[string]any
}

// Tools preserves the package-level catalog for compatibility. Runtime.Tools is
// the authoritative catalog after module registration.
func Tools() []ToolSpec {
	groups := [][]ToolSpec{
		basicToolSpecs(), filesystemToolSpecs(), repoToolSpecs(), workflowToolSpecs(),
		memoryToolSpecs(), executionToolSpecs(),
	}
	var tools []ToolSpec
	for _, group := range groups {
		tools = append(tools, group...)
	}
	return tools
}

func roSpec(name, title, description string, schema map[string]any) ToolSpec {
	return ToolSpec{Name: name, Title: title, Description: description, ReadOnly: true, Schema: schema}
}

func structuredROSpec(name, title, description string, schema map[string]any) ToolSpec {
	return ToolSpec{
		Name: name, Title: title, Description: description,
		ReadOnly: true, OutputMode: ToolOutputStructured, Schema: schema,
	}
}

func rwSpec(name, title, description string, schema map[string]any, destructive bool) ToolSpec {
	return ToolSpec{Name: name, Title: title, Description: description, Schema: schema, Destructive: destructive}
}

func commandSchema() map[string]any {
	return object(map[string]any{
		"command": str("Shell command."), "cwd": str("Working directory inside a root."),
		"shell": enum("cmd", "powershell", "bash", "sh", "zsh"), "timeout_ms": integer(),
		"tail_lines": integer(), "head_lines": integer(), "max_output_chars": integer(),
	}, "command")
}

func readRequestSchema() map[string]any {
	return object(map[string]any{
		"path": str("File path."), "start_line": integer(),
		"line_count": integer(), "max_chars": integer(),
	}, "path")
}

func patchEditSchema() map[string]any {
	return object(map[string]any{
		"old_text": str("Exact text to replace."), "new_text": str("Replacement text."),
		"replace_all": boolean(),
	}, "old_text")
}

func patchOperationSchema() map[string]any {
	return object(map[string]any{
		"op": enum("create", "update", "delete", "rename"), "path": str("Target path."),
		"content": str("Content for create or update."), "rename_to": str("Destination path for rename."),
		"recursive": boolean(), "edits": array(patchEditSchema()),
	}, "op", "path")
}

func memoryItemSchema() map[string]any {
	return object(map[string]any{
		"id": str("Canonical memory ID."), "provider_id": str("Provider memory ID."),
		"kind": str("Memory kind."), "content": str("Memory content."),
		"summary": str("Memory summary."), "score": number(),
		"project": str("Project scope."), "agent_id": str("Agent ID."),
		"concepts": array(str("")), "files": array(str("")),
		"created_at": str("Creation timestamp."), "metadata": freeObject(),
	})
}

const WidgetURI = "ui://widget/cb-compact-input-v2.html"

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
func number() map[string]any  { return map[string]any{"type": "number"} }
func boolean() map[string]any { return map[string]any{"type": "boolean"} }
func freeObject() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": true}
}
func array(items any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}
func enum(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}
