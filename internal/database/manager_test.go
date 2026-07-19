package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"codebridge/internal/config"
)

type managerTestConnection struct {
	validated string
	truncated bool
}

func (*managerTestConnection) Health(context.Context) HealthResult {
	return HealthResult{Available: true}
}
func (c *managerTestConnection) ValidateReadOnlySQL(statement string) error {
	c.validated = statement
	if strings.Contains(statement, "driver_blocked") {
		return errors.New("blocked by test dialect")
	}
	return nil
}
func (c *managerTestConnection) Query(context.Context, QueryRequest) (QueryResult, error) {
	return QueryResult{Rows: [][]any{{"ok"}}, RowCount: 1, Truncated: c.truncated}, nil
}
func (*managerTestConnection) PoolMetrics() PoolMetrics {
	return PoolMetrics{MaxOpen: 4, Open: 2, InUse: 1, Idle: 1}
}
func (*managerTestConnection) Explain(context.Context, QueryRequest) (QueryResult, error) {
	return QueryResult{Rows: [][]any{{"plan"}}, RowCount: 1}, nil
}
func (*managerTestConnection) Describe(context.Context, DescribeRequest) (DescribeResult, error) {
	return DescribeResult{}, nil
}
func (*managerTestConnection) SupportsMutation() bool { return true }
func (*managerTestConnection) PreviewMutation(_ context.Context, request MutationRequest) (MutationPreview, error) {
	return MutationPreview{
		Operation: request.Operation, Schema: request.Schema, Table: request.Table,
		PredicateColumns: []string{"id"}, PrimaryKeyColumns: []string{"id"},
		MaxAffectedRows: request.MaxAffectedRows,
	}, nil
}
func (*managerTestConnection) Mutate(_ context.Context, request MutationRequest) (MutationResult, error) {
	return MutationResult{
		MutationPreview: MutationPreview{
			Operation: request.Operation, Schema: request.Schema, Table: request.Table,
			PredicateColumns: []string{"id"}, PrimaryKeyColumns: []string{"id"},
			MaxAffectedRows: request.MaxAffectedRows,
		},
		AffectedRows: 1,
	}, nil
}
func (*managerTestConnection) Close() error { return nil }

func managerTestConfig() config.DatabaseConfig {
	return config.DatabaseConfig{
		Enabled: true,
		Connections: map[string]config.DatabaseConnectionConfig{
			"db.dev": {
				Driver: "test", Environment: "dev",
				CredentialRef: config.CredentialReference{Provider: "env", Name: "CODEBRIDGE_TEST_DATABASE_DSN"},
				Access:        config.DatabaseAccessConfig{Mode: "read-only"},
				Limits: config.DatabaseLimitsConfig{
					QueryTimeoutMS: 1000, MaxRows: 10, MaxResultBytes: 1024,
					MaxCellBytes: 128, MaxConcurrentQueries: 1,
				},
				Pool: config.DatabasePoolConfig{MaxOpen: 1, MaxIdle: 1, MaxLifetimeSeconds: 60},
			},
		},
	}
}

func TestManagerUsesExactAliasAndDriverValidation(t *testing.T) {
	t.Setenv("CODEBRIDGE_TEST_DATABASE_DSN", "test-credential")
	connection := &managerTestConnection{}
	manager, err := NewManager(managerTestConfig(), map[string]Constructor{
		"test": func(string, config.DatabaseConnectionConfig, string) (Connection, error) {
			return connection, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	result, summary, err := manager.Query(context.Background(), "db.dev", QueryRequest{SQL: "SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	if connection.validated != "SELECT 1" || summary.Alias != "db.dev" || result.RowCount != 1 {
		t.Fatalf("unexpected routing result: validated=%q summary=%#v result=%#v", connection.validated, summary, result)
	}

	if _, _, err := manager.Query(context.Background(), "db", QueryRequest{SQL: "SELECT 1"}); err == nil || !strings.Contains(err.Error(), "unknown database alias") {
		t.Fatalf("prefix alias unexpectedly resolved: %v", err)
	}

	if _, _, err := manager.Query(context.Background(), "db.dev", QueryRequest{SQL: "SELECT driver_blocked"}); err == nil || !strings.Contains(err.Error(), "driver rejected query") {
		t.Fatalf("driver-specific guard was not enforced: %v", err)
	}
	connection.truncated = true
	if _, _, err := manager.Query(context.Background(), "db.dev", QueryRequest{SQL: "SELECT 2"}); err != nil {
		t.Fatal(err)
	}
	listed := manager.List(context.Background(), false)
	if len(listed) != 1 {
		t.Fatalf("unexpected connection summaries: %#v", listed)
	}
	metrics := listed[0].Metrics
	if metrics.QueryTotal != 3 || metrics.FailedTotal != 1 || metrics.TruncatedTotal != 1 {
		t.Fatalf("unexpected operation metrics: %#v", metrics)
	}
	if metrics.Pool.MaxOpen != 4 || metrics.Pool.InUse != 1 {
		t.Fatalf("unexpected pool metrics: %#v", metrics.Pool)
	}
}
