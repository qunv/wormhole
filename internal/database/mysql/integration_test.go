package mysql

import (
	"context"
	"database/sql"
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
	cfg.Access.DeniedTables = []string{parsed.DBName + ".secrets"}
	cfg.Access.MaskColumns = []string{"*.email"}
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
	result, err := connection.Query(ctx, database.QueryRequest{
		SQL: "SELECT id, email FROM users ORDER BY id", MaxRows: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 || !result.Truncated || result.Rows[0][1] != "[masked]" {
		t.Fatalf("unexpected bounded/masked query result: %#v", result)
	}
	if _, err := connection.Explain(ctx, database.QueryRequest{SQL: "SELECT id FROM users"}); err != nil {
		t.Fatalf("explain failed: %v", err)
	}
	described, err := connection.Describe(ctx, database.DescribeRequest{
		Schema: parsed.DBName, Table: "users", IncludeIndexes: true,
	})
	if err != nil {
		t.Fatalf("describe failed: %v", err)
	}
	if len(described.Tables) != 1 || len(described.Tables[0].Columns) < 2 || len(described.Tables[0].Indexes) == 0 {
		t.Fatalf("unexpected describe result: %#v", described)
	}

	direct, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	if _, err := direct.ExecContext(ctx, "UPDATE users SET email = email"); err == nil {
		t.Fatal("integration database role unexpectedly allowed UPDATE")
	}
}
