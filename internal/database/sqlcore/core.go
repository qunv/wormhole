// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sqlcore contains the database/sql execution path shared by SQL
// database drivers. Dialects own only database-specific SQL and session rules.
package sqlcore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/database"
)

// Queryer is the subset required by dialect introspection implementations.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Dialect isolates behavior that database/sql cannot standardize across
// PostgreSQL, MySQL, SQLite, and future SQL engines.
type Dialect interface {
	Name() string
	Placeholder(position int) string
	QuoteIdentifier(identifier string) (string, error)
	ValidateReadOnlySQL(statement string) error
	ConfigureReadOnly(context.Context, *sql.Tx, config.DatabaseConnectionConfig) error
	ExplainSQL(statement string) string
	Describe(context.Context, Queryer, config.DatabaseConnectionConfig, database.DescribeRequest) (database.DescribeResult, error)
	NormalizeError(error) error
}

// Client implements database.Connection using the common database/sql path.
type Client struct {
	db      *sql.DB
	config  config.DatabaseConnectionConfig
	dialect Dialect
}

var _ database.Connection = (*Client)(nil)

// Open opens a registered database/sql driver and applies CodeBridge pool
// limits. Driver packages remain responsible for registration.
func Open(driverName, credential string, cfg config.DatabaseConnectionConfig, dialect Dialect) (*Client, error) {
	if strings.TrimSpace(driverName) == "" {
		return nil, fmt.Errorf("database/sql driver name is required")
	}
	db, err := sql.Open(driverName, credential)
	if err != nil {
		return nil, err
	}
	client, err := NewWithDB(db, cfg, dialect)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return client, nil
}

// NewWithDB supports explicit connectors, tests, and drivers that need custom
// TLS, tracing, or connection hooks before entering the common SQL core.
func NewWithDB(db *sql.DB, cfg config.DatabaseConnectionConfig, dialect Dialect) (*Client, error) {
	if db == nil {
		return nil, fmt.Errorf("database/sql handle is required")
	}
	if dialect == nil {
		return nil, fmt.Errorf("SQL dialect is required")
	}
	db.SetMaxOpenConns(cfg.Pool.MaxOpen)
	db.SetMaxIdleConns(cfg.Pool.MaxIdle)
	db.SetConnMaxLifetime(time.Duration(cfg.Pool.MaxLifetimeSeconds) * time.Second)
	return &Client{db: db, config: cfg, dialect: dialect}, nil
}

func (c *Client) Health(ctx context.Context) database.HealthResult {
	if err := c.db.PingContext(ctx); err != nil {
		return database.HealthResult{Available: false, Error: database.SanitizeError(c.normalizeError(err))}
	}
	return database.HealthResult{Available: true}
}

func (c *Client) ValidateReadOnlySQL(statement string) error {
	return c.dialect.ValidateReadOnlySQL(statement)
}

func (c *Client) Query(ctx context.Context, request database.QueryRequest) (database.QueryResult, error) {
	return c.execute(ctx, request.SQL, request.Params, request.MaxRows)
}

func (c *Client) Explain(ctx context.Context, request database.QueryRequest) (database.QueryResult, error) {
	return c.execute(ctx, c.dialect.ExplainSQL(request.SQL), request.Params, 1)
}

func (c *Client) Describe(ctx context.Context, request database.DescribeRequest) (database.DescribeResult, error) {
	tx, err := c.beginReadOnly(ctx)
	if err != nil {
		return database.DescribeResult{}, err
	}
	defer tx.Rollback()
	result, err := c.dialect.Describe(ctx, tx, c.config, request)
	return result, c.normalizeError(err)
}

func (c *Client) Close() error { return c.db.Close() }

func (c *Client) beginReadOnly(ctx context.Context) (*sql.Tx, error) {
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, c.normalizeError(err)
	}
	if err := c.dialect.ConfigureReadOnly(ctx, tx, c.config); err != nil {
		_ = tx.Rollback()
		return nil, c.normalizeError(err)
	}
	return tx, nil
}

func (c *Client) execute(ctx context.Context, statement string, params []any, maxRows int) (database.QueryResult, error) {
	if maxRows <= 0 || maxRows > c.config.Limits.MaxRows {
		maxRows = c.config.Limits.MaxRows
	}
	started := time.Now()
	tx, err := c.beginReadOnly(ctx)
	if err != nil {
		return database.QueryResult{}, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, statement, params...)
	if err != nil {
		return database.QueryResult{}, c.normalizeError(err)
	}
	defer rows.Close()

	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return database.QueryResult{}, c.normalizeError(err)
	}
	result := database.QueryResult{Columns: make([]database.Column, len(columnTypes))}
	for index, column := range columnTypes {
		result.Columns[index] = database.Column{Name: column.Name(), DatabaseType: column.DatabaseTypeName()}
	}

	bytesUsed := 0
	for rows.Next() {
		if len(result.Rows) >= maxRows {
			result.Truncated = true
			break
		}
		values := make([]any, len(columnTypes))
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return result, c.normalizeError(err)
		}
		for index := range values {
			if shouldMaskColumn(columnTypes[index].Name(), c.config.Access.MaskColumns) {
				values[index] = "[masked]"
				continue
			}
			var truncated bool
			values[index], truncated = normalizeValue(values[index], c.config.Limits.MaxCellBytes)
			result.Truncated = result.Truncated || truncated
		}
		raw, _ := json.Marshal(values)
		if bytesUsed+len(raw) > c.config.Limits.MaxResultBytes {
			result.Truncated = true
			break
		}
		bytesUsed += len(raw)
		result.Rows = append(result.Rows, values)
	}
	if err := rows.Err(); err != nil {
		return result, c.normalizeError(err)
	}
	result.RowCount = len(result.Rows)
	result.ElapsedMS = time.Since(started).Milliseconds()
	return result, nil
}

func (c *Client) normalizeError(err error) error {
	if err == nil {
		return nil
	}
	if normalized := c.dialect.NormalizeError(err); normalized != nil {
		return normalized
	}
	return err
}

func shouldMaskColumn(name string, patterns []string) bool {
	name = strings.ToLower(name)
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == name || strings.HasSuffix(pattern, "."+name) || pattern == "*."+name {
			return true
		}
	}
	return false
}

func normalizeValue(value any, maxBytes int) (any, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano), false
	case []byte:
		copyValue := append([]byte(nil), typed...)
		truncated := false
		if len(copyValue) > maxBytes {
			copyValue = copyValue[:maxBytes]
			truncated = true
		}
		if json.Valid(copyValue) {
			var decoded any
			if json.Unmarshal(copyValue, &decoded) == nil {
				return decoded, truncated
			}
		}
		return "base64:" + base64.StdEncoding.EncodeToString(copyValue), truncated
	case string:
		if len(typed) > maxBytes {
			return typed[:maxBytes] + "…", true
		}
		return typed, false
	default:
		return typed, false
	}
}
