// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

type ToolSpec struct {
	Name        string
	Title       string
	Description string
	ReadOnly    bool
	Destructive bool
	OpenWorld   bool
	Schema      map[string]any
	Meta        map[string]any
}

// Tools preserves the package-level catalog for compatibility. Runtime.Tools is
// the authoritative catalog after module registration.
func Tools() []ToolSpec {
	groups := [][]ToolSpec{
		basicToolSpecs(), filesystemToolSpecs(), repoToolSpecs(), workflowToolSpecs(),
		figmaToolSpecs(), memoryToolSpecs(), databaseToolSpecs(), executionToolSpecs(),
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
