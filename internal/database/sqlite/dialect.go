// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"codebridge/internal/config"
	"codebridge/internal/database"
	"codebridge/internal/database/sqlcore"

	modernsqlite "modernc.org/sqlite"
)

// Dialect contains only SQLite-specific SQL and introspection behavior.
type Dialect struct{}

var _ sqlcore.Dialect = Dialect{}

func (Dialect) Name() string { return "sqlite" }

func (Dialect) Placeholder(int) string { return "?" }

func (Dialect) QuoteIdentifier(identifier string) (string, error) {
	if strings.TrimSpace(identifier) == "" || strings.ContainsRune(identifier, '\x00') {
		return "", fmt.Errorf("invalid SQLite identifier")
	}
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`, nil
}

var forbiddenSQLiteFunctions = map[string]bool{
	"load_extension":    true,
	"readfile":          true,
	"writefile":         true,
	"edit":              true,
	"fts3_tokenizer":    true,
	"sqlite_log":        true,
	"last_insert_rowid": true,
	"changes":           true,
	"total_changes":     true,
}

var forbiddenSQLiteSchemas = map[string]bool{
	"temp": true,
}

func (Dialect) ValidateReadOnlySQL(statement string) error {
	for _, call := range database.SQLFunctionCalls(statement) {
		if forbiddenSQLiteFunctions[call] {
			return fmt.Errorf("SQLite read-only query uses forbidden function %q", call)
		}
	}
	identifiers := database.SQLIdentifiers(statement)
	for index, identifier := range identifiers {
		switch identifier {
		case "sqlite_schema", "sqlite_master", "sqlite_temp_schema", "sqlite_temp_master":
			return fmt.Errorf("SQLite read-only query cannot access internal schema table %q", identifier)
		}
		if index+1 < len(identifiers) && identifiers[index+1] == "." && forbiddenSQLiteSchemas[identifier] {
			return fmt.Errorf("SQLite read-only query cannot access schema %q", identifier)
		}
	}
	return nil
}

func (Dialect) ConfigureReadOnly(ctx context.Context, tx *sql.Tx, _ config.DatabaseConnectionConfig) error {
	for _, statement := range []string{
		"PRAGMA query_only = ON",
		"PRAGMA trusted_schema = OFF",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	var queryOnly int
	if err := tx.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		return err
	}
	if queryOnly != 1 {
		return fmt.Errorf("SQLite query_only could not be enforced")
	}
	return nil
}

func (Dialect) ExplainSQL(statement string) string {
	return "EXPLAIN QUERY PLAN " + statement
}

func (d Dialect) Describe(ctx context.Context, queryer sqlcore.Queryer, cfg config.DatabaseConnectionConfig, request database.DescribeRequest) (database.DescribeResult, error) {
	if request.Schema != "" && !strings.EqualFold(request.Schema, "main") {
		return database.DescribeResult{}, fmt.Errorf("SQLite supports only the main schema")
	}
	query := `SELECT name
FROM main.sqlite_schema
WHERE type IN ('table', 'view')
  AND name NOT LIKE 'sqlite_%'`
	var params []any
	if request.Table != "" {
		query += " AND name = ?"
		params = append(params, request.Table)
	}
	query += " ORDER BY name LIMIT ?"
	params = append(params, cfg.Limits.MaxRows+1)
	rows, err := queryer.QueryContext(ctx, query, params...)
	if err != nil {
		return database.DescribeResult{}, err
	}
	defer rows.Close()

	result := database.DescribeResult{}
	var tableNames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return result, err
		}
		if len(tableNames) >= cfg.Limits.MaxRows {
			result.Truncated = true
			break
		}
		if deniedTable("main", name, cfg.Access.DeniedTables) {
			continue
		}
		tableNames = append(tableNames, name)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}

	for _, name := range tableNames {
		table, err := d.describeTable(ctx, queryer, name, request.IncludeIndexes)
		if err != nil {
			return result, err
		}
		result.Tables = append(result.Tables, table)
	}
	return result, nil
}

func (d Dialect) describeTable(ctx context.Context, queryer sqlcore.Queryer, tableName string, includeIndexes bool) (database.TableDescription, error) {
	quotedTable, err := d.QuoteIdentifier(tableName)
	if err != nil {
		return database.TableDescription{}, err
	}
	rows, err := queryer.QueryContext(ctx, "PRAGMA main.table_xinfo("+quotedTable+")")
	if err != nil {
		return database.TableDescription{}, err
	}
	table := database.TableDescription{Schema: "main", Name: tableName}
	for rows.Next() {
		var cid, notNull, primaryKey, hidden int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey, &hidden); err != nil {
			rows.Close()
			return table, err
		}
		table.Columns = append(table.Columns, database.ColumnDescription{
			Name: name, DataType: dataType, Nullable: notNull == 0, Ordinal: cid + 1,
		})
	}
	if err := rows.Close(); err != nil {
		return table, err
	}
	if !includeIndexes {
		return table, nil
	}
	indexes, err := d.describeIndexes(ctx, queryer, tableName)
	if err != nil {
		return table, err
	}
	table.Indexes = indexes
	return table, nil
}

func (d Dialect) describeIndexes(ctx context.Context, queryer sqlcore.Queryer, tableName string) ([]database.IndexDescription, error) {
	quotedTable, err := d.QuoteIdentifier(tableName)
	if err != nil {
		return nil, err
	}
	rows, err := queryer.QueryContext(ctx, "PRAGMA main.index_list("+quotedTable+")")
	if err != nil {
		return nil, err
	}
	type indexEntry struct {
		name    string
		unique  bool
		origin  string
		partial bool
	}
	var entries []indexEntry
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, indexEntry{name: name, unique: unique == 1, origin: origin, partial: partial == 1})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	indexes := make([]database.IndexDescription, 0, len(entries))
	for _, entry := range entries {
		quotedIndex, err := d.QuoteIdentifier(entry.name)
		if err != nil {
			return nil, err
		}
		columnRows, err := queryer.QueryContext(ctx, "PRAGMA main.index_xinfo("+quotedIndex+")")
		if err != nil {
			return nil, err
		}
		var columns []string
		for columnRows.Next() {
			var seqNo, cid, desc, key int
			var name, collation sql.NullString
			if err := columnRows.Scan(&seqNo, &cid, &name, &desc, &collation, &key); err != nil {
				columnRows.Close()
				return nil, err
			}
			if key == 0 {
				continue
			}
			column := "<expression>"
			if name.Valid && name.String != "" {
				column, err = d.QuoteIdentifier(name.String)
				if err != nil {
					columnRows.Close()
					return nil, err
				}
			}
			if desc == 1 {
				column += " DESC"
			}
			columns = append(columns, column)
		}
		if err := columnRows.Close(); err != nil {
			return nil, err
		}
		prefix := ""
		if entry.unique {
			prefix = "UNIQUE "
		}
		definition := prefix + "INDEX (" + strings.Join(columns, ", ") + ")"
		if entry.origin != "c" {
			definition += " origin=" + entry.origin
		}
		if entry.partial {
			definition += " partial"
		}
		indexes = append(indexes, database.IndexDescription{Name: entry.name, Definition: definition})
	}
	return indexes, nil
}

func (Dialect) NormalizeError(err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *modernsqlite.Error
	if errors.As(err, &sqliteErr) {
		return fmt.Errorf("SQLite error %d: %s", sqliteErr.Code(), sqliteErr.Error())
	}
	return err
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
