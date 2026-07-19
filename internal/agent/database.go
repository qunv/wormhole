// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"fmt"

	"codebridge/internal/database"
)

func (r *Runtime) handleDatabase(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "db_list_connections":
		return map[string]any{
			"enabled":     r.Config.Database.Enabled,
			"connections": r.Database.List(ctx, boolArg(args, "check_health", false)),
		}, nil
	case "db_describe":
		if err := required(args, "alias"); err != nil {
			return nil, err
		}
		result, connection, err := r.Database.Describe(ctx, stringArg(args, "alias", ""), database.DescribeRequest{
			Schema: stringArg(args, "schema", ""), Table: stringArg(args, "table", ""),
			IncludeIndexes: boolArg(args, "include_indexes", false),
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"connection_alias": connection.Alias, "environment": connection.Environment,
			"read_only": connection.Access == "read-only", "tables": result.Tables,
			"truncated": result.Truncated,
		}, nil
	case "db_query", "db_explain":
		if err := required(args, "alias", "sql"); err != nil {
			return nil, err
		}
		request := database.QueryRequest{
			SQL: stringArg(args, "sql", ""), Params: arrayArg(args, "params"),
			MaxRows: intArg(args, "max_rows", 0),
		}
		var result database.QueryResult
		var connection database.ConnectionSummary
		var err error
		if name == "db_explain" {
			result, connection, err = r.Database.Explain(ctx, stringArg(args, "alias", ""), request)
		} else {
			result, connection, err = r.Database.Query(ctx, stringArg(args, "alias", ""), request)
		}
		if err != nil {
			return nil, err
		}
		response := map[string]any{
			"connection_alias": connection.Alias, "environment": connection.Environment,
			"read_only": connection.Access == "read-only", "columns": result.Columns,
			"rows": result.Rows, "row_count": result.RowCount, "truncated": result.Truncated,
			"elapsed_ms": result.ElapsedMS, "query_hash": result.QueryHash,
		}
		if name == "db_explain" {
			response["kind"] = "explain"
		}
		return response, nil
	default:
		return nil, fmt.Errorf("unsupported database tool: %s", name)
	}
}
