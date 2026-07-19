// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"codebridge/internal/config"
)

type HealthResult struct {
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

type ConnectionSummary struct {
	Alias        string            `json:"alias"`
	Driver       string            `json:"driver"`
	Environment  string            `json:"environment"`
	Access       string            `json:"access"`
	Capabilities []string          `json:"capabilities"`
	Available    bool              `json:"available"`
	Error        string            `json:"error,omitempty"`
	Metrics      ConnectionMetrics `json:"metrics"`
}

type PoolMetrics struct {
	MaxOpen           int   `json:"max_open"`
	Open              int   `json:"open"`
	InUse             int   `json:"in_use"`
	Idle              int   `json:"idle"`
	WaitCount         int64 `json:"wait_count"`
	WaitMS            int64 `json:"wait_ms"`
	MaxIdleClosed     int64 `json:"max_idle_closed"`
	MaxIdleTimeClosed int64 `json:"max_idle_time_closed"`
	MaxLifetimeClosed int64 `json:"max_lifetime_closed"`
}

type ConnectionMetrics struct {
	QueryTotal           uint64      `json:"query_total"`
	ExplainTotal         uint64      `json:"explain_total"`
	DescribeTotal        uint64      `json:"describe_total"`
	MutationPreviewTotal uint64      `json:"mutation_preview_total"`
	MutationTotal        uint64      `json:"mutation_total"`
	AffectedRowsTotal    uint64      `json:"affected_rows_total"`
	FailedTotal          uint64      `json:"failed_total"`
	TimeoutTotal         uint64      `json:"timeout_total"`
	TruncatedTotal       uint64      `json:"truncated_total"`
	TotalDurationMS      uint64      `json:"total_duration_ms"`
	Pool                 PoolMetrics `json:"pool"`
}

type PoolStatsProvider interface {
	PoolMetrics() PoolMetrics
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

type MutationRequest struct {
	Operation       string
	Schema          string
	Table           string
	Values          map[string]any
	Where           map[string]any
	MaxAffectedRows int64
}

type MutationPreview struct {
	Operation         string   `json:"operation"`
	Schema            string   `json:"schema"`
	Table             string   `json:"table"`
	ValueColumns      []string `json:"value_columns,omitempty"`
	PredicateColumns  []string `json:"predicate_columns"`
	PrimaryKeyColumns []string `json:"primary_key_columns"`
	MaxAffectedRows   int64    `json:"max_affected_rows"`
}

type MutationResult struct {
	MutationPreview
	AffectedRows int64 `json:"affected_rows"`
	ElapsedMS    int64 `json:"elapsed_ms"`
}

type MutationConnection interface {
	SupportsMutation() bool
	PreviewMutation(context.Context, MutationRequest) (MutationPreview, error)
	Mutate(context.Context, MutationRequest) (MutationResult, error)
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
	Alias            string
	Config           config.DatabaseConnectionConfig
	Connection       Connection
	InitError        error
	semaphore        chan struct{}
	queries          atomic.Uint64
	explains         atomic.Uint64
	describes        atomic.Uint64
	mutationPreviews atomic.Uint64
	mutations        atomic.Uint64
	affectedRows     atomic.Uint64
	failed           atomic.Uint64
	timeouts         atomic.Uint64
	truncated        atomic.Uint64
	durationMS       atomic.Uint64
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

func (h *Handle) record(duration time.Duration, truncated bool, err error) {
	h.durationMS.Add(uint64(max(duration.Milliseconds(), 0)))
	if truncated {
		h.truncated.Add(1)
	}
	if err != nil {
		h.failed.Add(1)
		if err == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
			h.timeouts.Add(1)
		}
	}
}

func (h *Handle) metrics() ConnectionMetrics {
	metrics := ConnectionMetrics{
		QueryTotal: h.queries.Load(), ExplainTotal: h.explains.Load(), DescribeTotal: h.describes.Load(),
		MutationPreviewTotal: h.mutationPreviews.Load(), MutationTotal: h.mutations.Load(),
		AffectedRowsTotal: h.affectedRows.Load(), FailedTotal: h.failed.Load(),
		TimeoutTotal: h.timeouts.Load(), TruncatedTotal: h.truncated.Load(),
		TotalDurationMS: h.durationMS.Load(),
	}
	if provider, ok := h.Connection.(PoolStatsProvider); ok {
		metrics.Pool = provider.PoolMetrics()
	}
	return metrics
}

func timeoutContext(ctx context.Context, milliseconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Duration(milliseconds)*time.Millisecond)
}
