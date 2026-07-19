package mysql

import (
	"context"
	"os"
	"testing"
	"time"

	"codebridge/internal/database"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestMySQLIntegration(t *testing.T) {
	dsn := os.Getenv("CODEBRIDGE_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("CODEBRIDGE_TEST_MYSQL_DSN is not configured")
	}
	parsed, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse integration DSN: %v", err)
	}
	if parsed.DBName == "" {
		t.Fatal("integration DSN must select a database")
	}
	cfg := mysqlTestConfig("dev")
	cfg.Access.AllowedSchemas = []string{parsed.DBName}
	connection, err := New("db.mysql_test", cfg, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if health := connection.Health(ctx); !health.Available {
		t.Fatalf("MySQL health check failed: %s", health.Error)
	}
	result, err := connection.Query(ctx, database.QueryRequest{SQL: "SELECT 1 AS value", MaxRows: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 {
		t.Fatalf("row count = %d, want 1", result.RowCount)
	}
	if _, err := connection.Explain(ctx, database.QueryRequest{SQL: "SELECT 1"}); err != nil {
		t.Fatalf("explain failed: %v", err)
	}
	if _, err := connection.Describe(ctx, database.DescribeRequest{Schema: parsed.DBName}); err != nil {
		t.Fatalf("describe failed: %v", err)
	}
}
