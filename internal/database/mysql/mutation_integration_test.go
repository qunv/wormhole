package mysql

import (
	"context"
	"os"
	"testing"
	"time"

	"codebridge/internal/database"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestMySQLMutationIntegration(t *testing.T) {
	dsn := os.Getenv("CODEBRIDGE_TEST_MYSQL_WRITE_DSN")
	if dsn == "" {
		t.Skip("CODEBRIDGE_TEST_MYSQL_WRITE_DSN is not configured")
	}
	parsed, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg := mysqlTestConfig("dev")
	cfg.Access.Mode = "read-write"
	cfg.Access.AllowedSchemas = []string{parsed.DBName}
	connection, err := New("db.mysql_write_test", cfg, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	mutator, ok := connection.(database.MutationConnection)
	if !ok || !mutator.SupportsMutation() {
		t.Fatal("MySQL structured mutation support is unavailable")
	}
	request := database.MutationRequest{
		Operation: "update", Schema: parsed.DBName, Table: "users",
		Values: map[string]any{"status": "disabled"}, Where: map[string]any{"id": int64(1)},
		MaxAffectedRows: 1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	preview, err := mutator.PreviewMutation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.PrimaryKeyColumns) != 1 || preview.PrimaryKeyColumns[0] != "id" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	result, err := mutator.Mutate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.AffectedRows != 1 {
		t.Fatalf("affected rows = %d", result.AffectedRows)
	}
	queried, err := connection.Query(ctx, database.QueryRequest{SQL: "SELECT status FROM users WHERE id = ?", Params: []any{int64(1)}, MaxRows: 1})
	if err != nil {
		t.Fatal(err)
	}
	if queried.RowCount != 1 || stringValue(queried.Rows[0][0]) != "disabled" {
		t.Fatalf("mutation was not visible: %#v", queried)
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}
