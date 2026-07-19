package postgres

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/database"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("CODEBRIDGE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CODEBRIDGE_TEST_POSTGRES_DSN is not configured")
	}
	cfg := config.DatabaseConnectionConfig{
		Driver: "postgres", Environment: "dev",
		Access: config.DatabaseAccessConfig{
			Mode: "read-only", AllowedSchemas: []string{"app"},
			DeniedTables: []string{"app.secrets"}, MaskColumns: []string{"*.email"},
		},
		Limits: config.DatabaseLimitsConfig{
			QueryTimeoutMS: 2000, MaxRows: 10, MaxResultBytes: 64 << 10,
			MaxCellBytes: 1024, MaxConcurrentQueries: 2,
		},
		Pool: config.DatabasePoolConfig{MaxOpen: 2, MaxIdle: 1, MaxLifetimeSeconds: 60},
	}
	connection, err := New("db.postgres_test", cfg, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if health := connection.Health(ctx); !health.Available {
		t.Fatalf("PostgreSQL health check failed: %s", health.Error)
	}
	result, err := connection.Query(ctx, database.QueryRequest{
		SQL: "SELECT id, email FROM app.users ORDER BY id", MaxRows: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 || !result.Truncated || result.Rows[0][1] != "[masked]" {
		t.Fatalf("unexpected bounded/masked query result: %#v", result)
	}
	if _, err := connection.Explain(ctx, database.QueryRequest{SQL: "SELECT id FROM app.users"}); err != nil {
		t.Fatalf("explain failed: %v", err)
	}
	described, err := connection.Describe(ctx, database.DescribeRequest{Schema: "app", Table: "users", IncludeIndexes: true})
	if err != nil {
		t.Fatalf("describe failed: %v", err)
	}
	if len(described.Tables) != 1 || len(described.Tables[0].Columns) < 2 || len(described.Tables[0].Indexes) == 0 {
		t.Fatalf("unexpected describe result: %#v", described)
	}

	direct, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	if _, err := direct.ExecContext(ctx, "UPDATE app.users SET email = email"); err == nil {
		t.Fatal("integration database role unexpectedly allowed UPDATE")
	}
}
