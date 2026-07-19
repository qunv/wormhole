// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

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
	case "db_preview_mutation", "db_mutate":
		if err := required(args, "alias", "operation", "schema", "table", "where"); err != nil {
			return nil, err
		}
		request, err := databaseMutationRequest(args)
		if err != nil {
			return nil, err
		}
		alias := stringArg(args, "alias", "")
		if name == "db_preview_mutation" {
			preview, connection, err := r.Database.PreviewMutation(ctx, alias, request)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"connection_alias": connection.Alias, "environment": connection.Environment,
				"read_only": false, "preview": preview,
				"approval_action": databaseMutationApprovalAction(args),
			}, nil
		}
		result, connection, err := r.Database.Mutate(ctx, alias, request)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"connection_alias": connection.Alias, "environment": connection.Environment,
			"read_only": false, "operation": result.Operation, "schema": result.Schema,
			"table": result.Table, "affected_rows": result.AffectedRows,
			"max_affected_rows": result.MaxAffectedRows, "elapsed_ms": result.ElapsedMS,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported database tool: %s", name)
	}
}

func databaseMutationRequest(args map[string]any) (database.MutationRequest, error) {
	maxAffected := intArg(args, "max_affected_rows", 1)
	if maxAffected < 1 {
		return database.MutationRequest{}, fmt.Errorf("max_affected_rows must be greater than zero")
	}
	return database.MutationRequest{
		Operation: strings.ToLower(strings.TrimSpace(stringArg(args, "operation", ""))),
		Schema:    strings.TrimSpace(stringArg(args, "schema", "")),
		Table:     strings.TrimSpace(stringArg(args, "table", "")),
		Values:    objectArg(args, "values"), Where: objectArg(args, "where"),
		MaxAffectedRows: int64(maxAffected),
	}, nil
}

func databaseMutationApprovalAction(args map[string]any) string {
	request, err := databaseMutationRequest(args)
	if err != nil || stringArg(args, "alias", "") == "" || request.Operation == "" || request.Schema == "" || request.Table == "" || len(request.Where) == 0 {
		return ""
	}
	material, _ := json.Marshal(map[string]any{
		"alias": stringArg(args, "alias", ""), "operation": request.Operation,
		"schema": request.Schema, "table": request.Table, "values": request.Values,
		"where": request.Where, "maxAffectedRows": request.MaxAffectedRows,
	})
	sum := sha256.Sum256(material)
	return fmt.Sprintf("db_mutate:%s:%s:%s.%s:sha256:%s",
		stringArg(args, "alias", ""), request.Operation, request.Schema, request.Table,
		hex.EncodeToString(sum[:12]))
}
