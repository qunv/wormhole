// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"codebridge/internal/config"
	"codebridge/internal/database"
	"codebridge/internal/database/sqlcore"

	"github.com/jackc/pgx/v5/pgconn"
)

// Dialect contains only PostgreSQL-specific SQL and session behavior.
type Dialect struct{}

var _ sqlcore.Dialect = Dialect{}

func (Dialect) Name() string { return "postgres" }

func (Dialect) Placeholder(position int) string { return fmt.Sprintf("$%d", position) }

func (Dialect) QuoteIdentifier(identifier string) (string, error) {
	if strings.TrimSpace(identifier) == "" || strings.ContainsRune(identifier, '\x00') {
		return "", fmt.Errorf("invalid PostgreSQL identifier")
	}
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`, nil
}

var forbiddenReadFunctions = map[string]bool{
	"nextval": true, "setval": true,
	"pg_advisory_lock": true, "pg_advisory_lock_shared": true,
	"pg_try_advisory_lock": true, "pg_try_advisory_lock_shared": true,
	"pg_cancel_backend": true, "pg_terminate_backend": true,
	"pg_reload_conf": true, "pg_rotate_logfile": true, "pg_notify": true,
	"pg_logical_emit_message": true, "set_config": true,
	"lo_import": true, "lo_export": true, "lo_unlink": true,
	"pg_read_file": true, "pg_read_binary_file": true,
	"pg_ls_dir": true, "pg_stat_file": true,
	"dblink": true, "dblink_connect": true, "dblink_connect_u": true,
	"dblink_exec": true, "dblink_open": true, "dblink_fetch": true,
}

func (Dialect) ValidateReadOnlySQL(statement string) error {
	for _, identifier := range database.SQLFunctionCalls(statement) {
		if forbiddenReadFunctions[identifier] {
			return fmt.Errorf("PostgreSQL read-only query uses forbidden function %q", identifier)
		}
	}
	return nil
}

func (d Dialect) ConfigureReadOnly(ctx context.Context, tx *sql.Tx, cfg config.DatabaseConnectionConfig) error {
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", cfg.Limits.QueryTimeoutMS)); err != nil {
		return err
	}
	if len(cfg.Access.AllowedSchemas) == 0 {
		return nil
	}
	quoted := make([]string, 0, len(cfg.Access.AllowedSchemas))
	for _, schema := range cfg.Access.AllowedSchemas {
		value, err := d.QuoteIdentifier(schema)
		if err != nil {
			return err
		}
		quoted = append(quoted, value)
	}
	_, err := tx.ExecContext(ctx, "SET LOCAL search_path TO "+strings.Join(quoted, ", "))
	return err
}

func (Dialect) ExplainSQL(statement string) string {
	return "EXPLAIN (FORMAT JSON, COSTS TRUE, VERBOSE FALSE, BUFFERS FALSE) " + statement
}

func (d Dialect) Describe(ctx context.Context, queryer sqlcore.Queryer, cfg config.DatabaseConnectionConfig, request database.DescribeRequest) (database.DescribeResult, error) {
	query := `SELECT table_schema, table_name, column_name, data_type, is_nullable, ordinal_position
FROM information_schema.columns
WHERE table_schema NOT IN ('pg_catalog', 'information_schema')`
	var params []any
	query = d.appendNamedAllowedSchemas(query, &params, "table_schema", cfg.Access.AllowedSchemas)
	if request.Schema != "" {
		params = append(params, request.Schema)
		query += " AND table_schema = " + d.Placeholder(len(params))
	}
	if request.Table != "" {
		params = append(params, request.Table)
		query += " AND table_name = " + d.Placeholder(len(params))
	}
	query += " ORDER BY table_schema, table_name, ordinal_position"
	params = append(params, cfg.Limits.MaxRows+1)
	query += " LIMIT " + d.Placeholder(len(params))

	rows, err := queryer.QueryContext(ctx, query, params...)
	if err != nil {
		return database.DescribeResult{}, err
	}
	defer rows.Close()

	result := database.DescribeResult{}
	tables := map[string]*database.TableDescription{}
	order := []string{}
	columnCount := 0
	for rows.Next() {
		columnCount++
		if columnCount > cfg.Limits.MaxRows {
			result.Truncated = true
			break
		}
		var schema, table, column, dataType, nullable string
		var ordinal int
		if err := rows.Scan(&schema, &table, &column, &dataType, &nullable, &ordinal); err != nil {
			return result, err
		}
		if deniedTable(schema, table, cfg.Access.DeniedTables) {
			continue
		}
		key := schema + "\x00" + table
		entry := tables[key]
		if entry == nil {
			entry = &database.TableDescription{Schema: schema, Name: table}
			tables[key] = entry
			order = append(order, key)
		}
		entry.Columns = append(entry.Columns, database.ColumnDescription{
			Name: column, DataType: dataType, Nullable: nullable == "YES", Ordinal: ordinal,
		})
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if request.IncludeIndexes && len(tables) > 0 {
		if err := d.attachIndexes(ctx, queryer, cfg, request, tables); err != nil {
			return result, err
		}
	}
	for _, key := range order {
		result.Tables = append(result.Tables, *tables[key])
	}
	return result, nil
}

func (Dialect) NormalizeError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("PostgreSQL SQLSTATE %s: %s", pgErr.Code, pgErr.Message)
	}
	return err
}

func (d Dialect) attachIndexes(ctx context.Context, queryer sqlcore.Queryer, cfg config.DatabaseConnectionConfig, request database.DescribeRequest, tables map[string]*database.TableDescription) error {
	query := `SELECT schemaname, tablename, indexname, indexdef
FROM pg_indexes
WHERE schemaname NOT IN ('pg_catalog', 'information_schema')`
	var params []any
	query = d.appendNamedAllowedSchemas(query, &params, "schemaname", cfg.Access.AllowedSchemas)
	if request.Schema != "" {
		params = append(params, request.Schema)
		query += " AND schemaname = " + d.Placeholder(len(params))
	}
	if request.Table != "" {
		params = append(params, request.Table)
		query += " AND tablename = " + d.Placeholder(len(params))
	}
	query += " ORDER BY schemaname, tablename, indexname"
	rows, err := queryer.QueryContext(ctx, query, params...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table, name, definition string
		if err := rows.Scan(&schema, &table, &name, &definition); err != nil {
			return err
		}
		if entry := tables[schema+"\x00"+table]; entry != nil {
			entry.Indexes = append(entry.Indexes, database.IndexDescription{Name: name, Definition: definition})
		}
	}
	return rows.Err()
}

func (d Dialect) appendNamedAllowedSchemas(query string, params *[]any, column string, schemas []string) string {
	if len(schemas) == 0 {
		return query
	}
	placeholders := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		*params = append(*params, schema)
		placeholders = append(placeholders, d.Placeholder(len(*params)))
	}
	return query + " AND " + column + " IN (" + strings.Join(placeholders, ", ") + ")"
}

func deniedTable(schema, table string, denied []string) bool {
	qualified := strings.ToLower(schema + "." + table)
	table = strings.ToLower(table)
	for _, candidate := range denied {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == table || candidate == qualified {
			return true
		}
	}
	return false
}
