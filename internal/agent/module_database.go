// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import "context"

type databaseModule struct {
	builtInModulePolicy
	runtime *Runtime
}

func newDatabaseModule(runtime *Runtime) ToolModule { return &databaseModule{runtime: runtime} }
func (*databaseModule) Name() string                { return "database" }
func (*databaseModule) Specs() []ToolSpec           { return databaseToolSpecs() }
func (m *databaseModule) ToolPolicy(tool string, args map[string]any) ToolCallPolicy {
	policy := m.builtInModulePolicy.ToolPolicy(tool, args)
	if tool == "db_mutate" && policy.ApprovalAction != "" {
		policy.AlwaysRequireApproval = true
	}
	return policy
}
func (m *databaseModule) Handle(ctx context.Context, _ CallIdentity, tool string, args map[string]any) (any, error) {
	return m.runtime.handleDatabase(ctx, tool, args)
}
func (m *databaseModule) Health(ctx context.Context) any {
	connections := m.runtime.Database.List(ctx, true)
	available := 0
	for _, connection := range connections {
		if connection.Available {
			available++
		}
	}
	return map[string]any{
		"module": "database", "enabled": m.runtime.Config.Database.Enabled,
		"tools": len(databaseToolSpecs()), "available": available == len(connections),
		"available_connections": available, "connections": connections,
	}
}
func (m *databaseModule) Close() error {
	if m.runtime.Database != nil {
		m.runtime.Database.Close()
		m.runtime.Database = nil
	}
	return nil
}

func databaseToolSpecs() []ToolSpec {
	return []ToolSpec{
		{
			Name: "db_list_connections", Title: "List database connections",
			Description: "List configured database aliases and safe capability metadata. Credentials and endpoints are never returned.",
			ReadOnly:    true, OpenWorld: true,
			Schema: object(map[string]any{"check_health": boolean()}),
		},
		{
			Name: "db_describe", Title: "Describe database schema",
			Description: "Describe allowed SQL schemas, tables, columns, and optional indexes through an exact connection alias.",
			ReadOnly:    true, OpenWorld: true,
			Schema: object(map[string]any{
				"alias": str("Exact configured database alias."), "schema": str("Optional schema filter."),
				"table": str("Optional table filter."), "include_indexes": boolean(),
			}, "alias"),
		},
		{
			Name: "db_query", Title: "Query database",
			Description: "Run one bounded, parameterized, read-only SELECT or WITH query through an exact connection alias.",
			ReadOnly:    true, OpenWorld: true,
			Schema: object(map[string]any{
				"alias": str("Exact configured database alias."), "sql": str("One read-only SQL statement."),
				"params": array(map[string]any{}), "max_rows": integer(),
			}, "alias", "sql"),
		},
		{
			Name: "db_explain", Title: "Explain database query",
			Description: "Run the selected SQL driver's non-executing EXPLAIN form for one bounded, parameterized, read-only query.",
			ReadOnly:    true, OpenWorld: true,
			Schema: object(map[string]any{
				"alias": str("Exact configured database alias."), "sql": str("One read-only SQL statement."),
				"params": array(map[string]any{}),
			}, "alias", "sql"),
		},
		{
			Name: "db_preview_mutation", Title: "Preview database mutation",
			Description: "Validate a structured non-production update or delete, including primary-key and affected-row safeguards, without changing data. Returns the exact approval action required for execution.",
			ReadOnly:    true, OpenWorld: true,
			Schema: object(map[string]any{
				"alias":     str("Exact configured non-production database alias."),
				"operation": enum("update", "delete"), "schema": str("Exact schema name."), "table": str("Exact table name."),
				"values": object(nil), "where": object(nil), "max_affected_rows": integer(),
			}, "alias", "operation", "schema", "table", "where", "max_affected_rows"),
		},
		{
			Name: "db_mutate", Title: "Execute database mutation",
			Description: "Execute one exact approval-gated structured update or delete on a non-production read-write alias. Raw SQL, DDL, procedures, and unbounded predicates are not accepted.",
			ReadOnly:    false, OpenWorld: true, Destructive: true,
			Schema: object(map[string]any{
				"alias":     str("Exact configured non-production database alias."),
				"operation": enum("update", "delete"), "schema": str("Exact schema name."), "table": str("Exact table name."),
				"values": object(nil), "where": object(nil), "max_affected_rows": integer(),
			}, "alias", "operation", "schema", "table", "where", "max_affected_rows"),
		},
	}
}
