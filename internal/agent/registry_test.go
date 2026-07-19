package agent

import "testing"

func TestToolRegistryContract(t *testing.T) {
	groups := map[string][]ToolSpec{
		"basic":      basicToolSpecs(),
		"filesystem": filesystemToolSpecs(),
		"execution":  executionToolSpecs(),
		"figma":      figmaToolSpecs(),
		"repo":       repoToolSpecs(),
		"workflow":   workflowToolSpecs(),
		"memory":     memoryToolSpecs(),
		"database":   databaseToolSpecs(),
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
	if got, want := len(seen), 93; got != want {
		t.Fatalf("tool count = %d, want %d", got, want)
	}
	if got := len(Tools()); got != len(seen) {
		t.Fatalf("compatibility catalog count = %d, want %d", got, len(seen))
	}
	for _, required := range []string{
		"workspace_snapshot", "read_many", "apply_patch", "run_commands", "review_diff",
		"request_approval_batch", "figma_get_design_context", "codegraph_explore", "lca_input",
		"memory_export", "memory_import", "db_list_connections", "db_describe", "db_query", "db_explain",
		"db_preview_mutation", "db_mutate",
	} {
		if seen[required] == "" {
			t.Fatalf("missing contract tool: %s", required)
		}
	}
}
