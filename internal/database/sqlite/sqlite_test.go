package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"codebridge/internal/config"
	"codebridge/internal/database"

	_ "modernc.org/sqlite"
)

func createSQLiteFixture(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "fixture.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, secret TEXT)`,
		`CREATE INDEX idx_users_email ON users(email)`,
		`CREATE TABLE secrets (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO users(id, email, secret) VALUES (1, 'one@example.com', 'private'), (2, 'two@example.com', 'private')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture statement failed: %v", err)
		}
	}
	return path
}

func sqliteConfig(root string) config.DatabaseConnectionConfig {
	return config.DatabaseConnectionConfig{
		Driver: "sqlite", Environment: "dev", FileRoot: root,
		Access: config.DatabaseAccessConfig{
			Mode: "read-only", AllowedSchemas: []string{"main"},
			DeniedTables: []string{"main.secrets"}, MaskColumns: []string{"*.secret"},
		},
		Limits: config.DatabaseLimitsConfig{
			QueryTimeoutMS: 1000, MaxRows: 10, MaxResultBytes: 64 << 10,
			MaxCellBytes: 1024, MaxConcurrentQueries: 2,
		},
		Pool: config.DatabasePoolConfig{MaxOpen: 2, MaxIdle: 1, MaxLifetimeSeconds: 60},
	}
}

func TestSQLiteReadOnlyConnectionAndDescribe(t *testing.T) {
	root := t.TempDir()
	path := createSQLiteFixture(t, root)
	connection, err := New("db.sqlite", sqliteConfig(root), path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	ctx := context.Background()
	if health := connection.Health(ctx); !health.Available {
		t.Fatalf("health failed: %s", health.Error)
	}
	result, err := connection.Query(ctx, database.QueryRequest{
		SQL: "SELECT id, secret FROM users ORDER BY id", MaxRows: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 || !result.Truncated || result.Rows[0][1] != "[masked]" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := connection.Explain(ctx, database.QueryRequest{SQL: "SELECT id FROM users"}); err != nil {
		t.Fatalf("explain failed: %v", err)
	}
	described, err := connection.Describe(ctx, database.DescribeRequest{Schema: "main", Table: "users", IncludeIndexes: true})
	if err != nil {
		t.Fatalf("describe failed: %v", err)
	}
	if len(described.Tables) != 1 || len(described.Tables[0].Columns) != 3 || len(described.Tables[0].Indexes) == 0 {
		t.Fatalf("unexpected describe result: %#v", described)
	}
	if _, err := connection.Query(ctx, database.QueryRequest{SQL: "UPDATE users SET email = 'changed'", MaxRows: 1}); err == nil {
		t.Fatal("SQLite connection unexpectedly allowed UPDATE")
	}
}

func TestSQLitePathConfinement(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsidePath := createSQLiteFixture(t, outside)
	if _, err := New("db.sqlite", sqliteConfig(root), outsidePath); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("outside SQLite path was not rejected: %v", err)
	}
	if _, err := New("db.sqlite", sqliteConfig(root), ":memory:"); err == nil {
		t.Fatal("memory SQLite database was not rejected")
	}
	if _, err := New("db.sqlite", sqliteConfig(root), "file:fixture.db?mode=rw"); err == nil {
		t.Fatal("SQLite URI credential was not rejected")
	}

	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "escape.db")
		if err := os.Symlink(outsidePath, link); err != nil {
			t.Fatal(err)
		}
		if _, err := New("db.sqlite", sqliteConfig(root), link); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("SQLite symlink escape was not rejected: %v", err)
		}
	}
}

func TestSQLiteDSNIsReadOnlyAndHardened(t *testing.T) {
	dsn := sqliteReadOnlyDSN(filepath.Join(t.TempDir(), "file.db"), 2500)
	for _, value := range []string{"mode=ro", "query_only%281%29", "trusted_schema%280%29", "busy_timeout%282500%29"} {
		if !strings.Contains(dsn, value) {
			t.Fatalf("SQLite DSN %q does not contain %q", dsn, value)
		}
	}
}
