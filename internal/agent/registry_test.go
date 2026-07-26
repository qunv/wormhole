package agent

import "testing"

func TestToolRegistryContract(t *testing.T) {
	groups := map[string][]ToolSpec{
		"basic":      basicToolSpecs(),
		"filesystem": filesystemToolSpecs(),
		"execution":  executionToolSpecs(),
		"repo":       repoToolSpecs(),
		"workflow":   workflowToolSpecs(),
		"memory":     memoryToolSpecs(),
	}
	seen := map[string]string{}
	for group, tools := range groups {
		if len(tools) == 0 {
			t.Fatalf("module %s has no tools", group)
		}
		for _, tool := range tools {
			if tool.Name == "" || tool.Description == "" || tool.Schema == nil {
				t.Fatalf("incomplete tool spec in %s: %#v", group, tool)
			}
			if owner := seen[tool.Name]; owner != "" {
				t.Fatalf("duplicate tool %s in modules %s and %s", tool.Name, owner, group)
			}
			seen[tool.Name] = group
			if tool.Schema["type"] != "object" {
				t.Fatalf("%s schema is not an object", tool.Name)
			}
		}
	}
	if got, want := len(seen), 76; got != want {
		t.Fatalf("tool count = %d, want %d", got, want)
	}
	if got := len(Tools()); got != len(seen) {
		t.Fatalf("compatibility catalog count = %d, want %d", got, len(seen))
	}
	for _, required := range []string{
		"workspace_snapshot", "task_context", "read_many", "apply_patch", "run_commands", "review_diff",
		"request_approval_batch", "codegraph_explore", "cb_input",
		"memory_export", "memory_import",
	} {
		if seen[required] == "" {
			t.Fatalf("missing contract tool: %s", required)
		}
	}
	for _, removed := range []string{
		"list_skills", "read_skill", "create_skill", "delete_skill",
		"db_list_connections", "db_describe", "db_query", "db_explain", "db_preview_mutation", "db_mutate",
		"figma_status", "figma_list_tools", "figma_call_tool", "figma_get_design_context", "figma_get_screenshot",
	} {
		if owner := seen[removed]; owner != "" {
			t.Fatalf("removed built-in tool %s remains owned by %s", removed, owner)
		}
	}
}

func TestStructuredArraySchemasExposeItemFields(t *testing.T) {
	tools := map[string]ToolSpec{}
	for _, tool := range Tools() {
		tools[tool.Name] = tool
	}
	assertArrayItemProperties(t, tools["read_many"].Schema, "requests", "path", "start_line", "line_count", "max_chars")
	for _, name := range []string{"apply_patch", "preview_patch", "validate_patch"} {
		assertArrayItemProperties(t, tools[name].Schema, "operations", "op", "path", "content", "rename_to", "recursive", "edits")
	}
	assertArrayItemProperties(t, tools["memory_import"].Schema, "memories", "id", "provider_id", "kind", "content", "summary", "metadata")
}

func assertArrayItemProperties(t *testing.T, schema map[string]any, field string, names ...string) {
	t.Helper()
	properties, _ := schema["properties"].(map[string]any)
	arraySchema, _ := properties[field].(map[string]any)
	itemSchema, _ := arraySchema["items"].(map[string]any)
	itemProperties, _ := itemSchema["properties"].(map[string]any)
	for _, name := range names {
		if itemProperties[name] == nil {
			t.Fatalf("schema field %s item is missing property %s: %#v", field, name, itemSchema)
		}
	}
}
