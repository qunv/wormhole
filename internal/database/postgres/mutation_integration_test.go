package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/database"
)

func TestPostgresMutationIntegration(t *testing.T) {
	dsn := os.Getenv("CODEBRIDGE_TEST_POSTGRES_WRITE_DSN")
	if dsn == "" {
		t.Skip("CODEBRIDGE_TEST_POSTGRES_WRITE_DSN is not configured")
	}
	cfg := config.DatabaseConnectionConfig{
		Driver: "postgres", Environment: "dev",
		Access: config.DatabaseAccessConfig{Mode: "read-write", AllowedSchemas: []string{"app"}},
		Limits: config.DatabaseLimitsConfig{
			QueryTimeoutMS: 2000, MaxRows: 10, MaxResultBytes: 64 << 10,
			MaxCellBytes: 1024, MaxConcurrentQueries: 1,
		},
		Pool: config.DatabasePoolConfig{MaxOpen: 1, MaxIdle: 1, MaxLifetimeSeconds: 60},
	}
	connection, err := New("db.postgres_write_test", cfg, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	mutator, ok := connection.(database.MutationConnection)
	if !ok || !mutator.SupportsMutation() {
		t.Fatal("PostgreSQL structured mutation support is unavailable")
	}
	request := database.MutationRequest{
		Operation: "update", Schema: "app", Table: "users",
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
	queried, err := connection.Query(ctx, database.QueryRequest{SQL: "SELECT status FROM app.users WHERE id = $1", Params: []any{int64(1)}, MaxRows: 1})
	if err != nil {
		t.Fatal(err)
	}
	if queried.RowCount != 1 || queried.Rows[0][0] != "disabled" {
		t.Fatalf("mutation was not visible: %#v", queried)
	}
}
