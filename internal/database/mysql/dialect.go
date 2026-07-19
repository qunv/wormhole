// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"codebridge/internal/config"
	"codebridge/internal/database"
	"codebridge/internal/database/sqlcore"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// Dialect contains only MySQL-specific SQL and session behavior.
type Dialect struct{}

var _ sqlcore.Dialect = Dialect{}

func (Dialect) Name() string { return "mysql" }

func (Dialect) Placeholder(int) string { return "?" }

func (Dialect) QuoteIdentifier(identifier string) (string, error) {
	if strings.TrimSpace(identifier) == "" || strings.ContainsRune(identifier, '\x00') {
		return "", fmt.Errorf("invalid MySQL identifier")
	}
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`", nil
}

var forbiddenReadFunctions = map[string]bool{
	"get_lock": true, "release_lock": true, "release_all_locks": true,
	"load_file": true, "sleep": true, "benchmark": true,
	"master_pos_wait": true, "source_pos_wait": true,
	"last_insert_id": true, "sys_exec": true, "sys_eval": true,
}

var forbiddenUserSchemas = map[string]bool{
	"mysql": true, "performance_schema": true, "sys": true,
}

func (Dialect) ValidateReadOnlySQL(statement string) error {
	for _, identifier := range database.SQLFunctionCalls(statement) {
		if forbiddenReadFunctions[identifier] {
			return fmt.Errorf("MySQL read-only query uses forbidden function %q", identifier)
		}
	}
	identifiers := database.SQLIdentifiers(statement)
	for index := 0; index+1 < len(identifiers); index++ {
		if identifiers[index+1] == "." && forbiddenUserSchemas[identifiers[index]] {
			return fmt.Errorf("MySQL read-only query cannot access system schema %q", identifiers[index])
		}
	}
	return nil
}

func (Dialect) ConfigureReadOnly(ctx context.Context, tx *sql.Tx, cfg config.DatabaseConnectionConfig) error {
	// go-sql-driver/mysql maps sql.TxOptions{ReadOnly:true} to
	// START TRANSACTION READ ONLY. This additional server-side cap supplements
	// the context deadline enforced by the manager.
	_, err := tx.ExecContext(ctx, fmt.Sprintf("SET SESSION MAX_EXECUTION_TIME = %d", cfg.Limits.QueryTimeoutMS))
	return err
}

func (Dialect) ExplainSQL(statement string) string {
	return "EXPLAIN FORMAT=JSON " + statement
}

func (d Dialect) Describe(ctx context.Context, queryer sqlcore.Queryer, cfg config.DatabaseConnectionConfig, request database.DescribeRequest) (database.DescribeResult, error) {
	query := `SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, ORDINAL_POSITION
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA NOT IN ('mysql', 'information_schema', 'performance_schema', 'sys')`
	var params []any
	query = d.appendNamedAllowedSchemas(query, &params, "TABLE_SCHEMA", cfg.Access.AllowedSchemas)
	if request.Schema != "" {
		params = append(params, request.Schema)
		query += " AND TABLE_SCHEMA = " + d.Placeholder(len(params))
	}
	if request.Table != "" {
		params = append(params, request.Table)
		query += " AND TABLE_NAME = " + d.Placeholder(len(params))
	}
	query += " ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION"
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
	var mysqlErr *mysqldriver.MySQLError
	if errors.As(err, &mysqlErr) {
		state := strings.TrimRight(string(mysqlErr.SQLState[:]), "\x00")
		if state != "" {
			return fmt.Errorf("MySQL error %d SQLSTATE %s: %s", mysqlErr.Number, state, mysqlErr.Message)
		}
		return fmt.Errorf("MySQL error %d: %s", mysqlErr.Number, mysqlErr.Message)
	}
	return err
}

type indexBuilder struct {
	name      string
	indexType string
	unique    bool
	columns   []string
}

func (d Dialect) attachIndexes(ctx context.Context, queryer sqlcore.Queryer, cfg config.DatabaseConnectionConfig, request database.DescribeRequest, tables map[string]*database.TableDescription) error {
	query := `SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE, SEQ_IN_INDEX,
       COLUMN_NAME, INDEX_TYPE, SUB_PART
FROM INFORMATION_SCHEMA.STATISTICS
WHERE TABLE_SCHEMA NOT IN ('mysql', 'information_schema', 'performance_schema', 'sys')`
	var params []any
	query = d.appendNamedAllowedSchemas(query, &params, "TABLE_SCHEMA", cfg.Access.AllowedSchemas)
	if request.Schema != "" {
		params = append(params, request.Schema)
		query += " AND TABLE_SCHEMA = " + d.Placeholder(len(params))
	}
	if request.Table != "" {
		params = append(params, request.Table)
		query += " AND TABLE_NAME = " + d.Placeholder(len(params))
	}
	query += " ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX"

	rows, err := queryer.QueryContext(ctx, query, params...)
	if err != nil {
		return err
	}
	defer rows.Close()

	builders := map[string]*indexBuilder{}
	order := []string{}
	for rows.Next() {
		var schema, table, name, indexType string
		var nonUnique, sequence int
		var column sql.NullString
		var subPart sql.NullInt64
		if err := rows.Scan(&schema, &table, &name, &nonUnique, &sequence, &column, &indexType, &subPart); err != nil {
			return err
		}
		tableKey := schema + "\x00" + table
		if tables[tableKey] == nil {
			continue
		}
		key := tableKey + "\x00" + name
		builder := builders[key]
		if builder == nil {
			builder = &indexBuilder{name: name, indexType: strings.ToUpper(indexType), unique: nonUnique == 0}
			builders[key] = builder
			order = append(order, key)
		}
		columnDefinition := "<expression>"
		if column.Valid && column.String != "" {
			quoted, quoteErr := d.QuoteIdentifier(column.String)
			if quoteErr != nil {
				return quoteErr
			}
			columnDefinition = quoted
			if subPart.Valid && subPart.Int64 > 0 {
				columnDefinition += "(" + strconv.FormatInt(subPart.Int64, 10) + ")"
			}
		}
		builder.columns = append(builder.columns, columnDefinition)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, key := range order {
		builder := builders[key]
		parts := strings.Split(key, "\x00")
		entry := tables[parts[0]+"\x00"+parts[1]]
		if entry == nil {
			continue
		}
		prefix := ""
		if builder.unique {
			prefix = "UNIQUE "
		}
		definition := prefix + builder.indexType + " (" + strings.Join(builder.columns, ", ") + ")"
		entry.Indexes = append(entry.Indexes, database.IndexDescription{Name: builder.name, Definition: definition})
	}
	return nil
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
