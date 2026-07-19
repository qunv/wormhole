// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"time"

	"codebridge/internal/config"
)

type HealthResult struct {
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

type ConnectionSummary struct {
	Alias        string   `json:"alias"`
	Driver       string   `json:"driver"`
	Environment  string   `json:"environment"`
	Access       string   `json:"access"`
	Capabilities []string `json:"capabilities"`
	Available    bool     `json:"available"`
	Error        string   `json:"error,omitempty"`
}

type Column struct {
	Name         string `json:"name"`
	DatabaseType string `json:"database_type,omitempty"`
}

type QueryRequest struct {
	SQL     string
	Params  []any
	MaxRows int
}

type QueryResult struct {
	Columns   []Column `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowCount  int      `json:"row_count"`
	Truncated bool     `json:"truncated"`
	ElapsedMS int64    `json:"elapsed_ms"`
	QueryHash string   `json:"query_hash"`
}

type DescribeRequest struct {
	Schema         string
	Table          string
	IncludeIndexes bool
}

type IndexDescription struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

type ColumnDescription struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	Nullable bool   `json:"nullable"`
	Ordinal  int    `json:"ordinal"`
}

type TableDescription struct {
	Schema  string              `json:"schema"`
	Name    string              `json:"name"`
	Columns []ColumnDescription `json:"columns"`
	Indexes []IndexDescription  `json:"indexes,omitempty"`
}

type DescribeResult struct {
	Tables    []TableDescription `json:"tables"`
	Truncated bool               `json:"truncated"`
}

type Connection interface {
	Health(context.Context) HealthResult
	ValidateReadOnlySQL(string) error
	Query(context.Context, QueryRequest) (QueryResult, error)
	Explain(context.Context, QueryRequest) (QueryResult, error)
	Describe(context.Context, DescribeRequest) (DescribeResult, error)
	Close() error
}

type Constructor func(alias string, cfg config.DatabaseConnectionConfig, credential string) (Connection, error)

type Handle struct {
	Alias      string
	Config     config.DatabaseConnectionConfig
	Connection Connection
	InitError  error
	semaphore  chan struct{}
}

func newHandle(alias string, cfg config.DatabaseConnectionConfig, connection Connection, initErr error) *Handle {
	return &Handle{
		Alias: alias, Config: cfg, Connection: connection, InitError: initErr,
		semaphore: make(chan struct{}, cfg.Limits.MaxConcurrentQueries),
	}
}

func (h *Handle) acquire(ctx context.Context) error {
	select {
	case h.semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Handle) release() { <-h.semaphore }

func timeoutContext(ctx context.Context, milliseconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Duration(milliseconds)*time.Millisecond)
}
