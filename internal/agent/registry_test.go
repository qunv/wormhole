package agent

import "testing"

func TestToolRegistryContract(t *testing.T) {
	tools := Tools()
	if got, want := len(tools), 91; got != want {
		t.Fatalf("tool count = %d, want %d", got, want)
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool.Name == "" || tool.Description == "" || tool.Schema == nil {
			t.Fatalf("incomplete tool spec: %#v", tool)
		}
		if seen[tool.Name] {
			t.Fatalf("duplicate tool: %s", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Schema["type"] != "object" {
			t.Fatalf("%s schema is not an object", tool.Name)
		}
		groups := 0
		for _, group := range []map[string]bool{basicTools, fsTools, execTools, figmaTools, repoTools, workflowTools, memoryTools, databaseTools} {
			if group[tool.Name] {
				groups++
			}
		}
		if groups != 1 {
			t.Fatalf("%s belongs to %d dispatch groups, want 1", tool.Name, groups)
		}
	}
	for _, required := range []string{
		"workspace_snapshot", "read_many", "apply_patch", "run_commands", "review_diff",
		"request_approval_batch", "figma_get_design_context", "codegraph_explore", "lca_input",
		"memory_export", "memory_import", "db_list_connections", "db_describe", "db_query", "db_explain",
	} {
		if !seen[required] {
			t.Fatalf("missing contract tool: %s", required)
		}
	}
}
