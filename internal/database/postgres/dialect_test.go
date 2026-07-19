package postgres

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestDialectSQLPrimitives(t *testing.T) {
	dialect := Dialect{}
	if got := dialect.Placeholder(3); got != "$3" {
		t.Fatalf("Placeholder(3) = %q", got)
	}
	quoted, err := dialect.QuoteIdentifier("public\"data")
	if err != nil {
		t.Fatal(err)
	}
	if quoted != "\"public\"\"data\"" {
		t.Fatalf("quoted identifier = %q", quoted)
	}
	if got := dialect.ExplainSQL("SELECT 1"); !strings.HasPrefix(got, "EXPLAIN (FORMAT JSON") {
		t.Fatalf("unexpected explain SQL: %q", got)
	}
}

func TestDialectRejectsPostgresSideEffects(t *testing.T) {
	dialect := Dialect{}
	if err := dialect.ValidateReadOnlySQL("SELECT pg_advisory_lock(1)"); err == nil {
		t.Fatal("expected advisory lock to be rejected")
	}
	if err := dialect.ValidateReadOnlySQL("SELECT 'pg_advisory_lock'"); err != nil {
		t.Fatalf("function name inside a literal was rejected: %v", err)
	}
	if err := dialect.ValidateReadOnlySQL("SELECT pg_advisory_lock FROM metrics"); err != nil {
		t.Fatalf("column sharing a function name was rejected: %v", err)
	}
	for _, statement := range []string{
		"SELECT * FROM pg_catalog.pg_authid",
		"SELECT * FROM \"pg_catalog\".\"pg_authid\"",
		"SELECT * FROM information_schema.tables",
	} {
		if err := dialect.ValidateReadOnlySQL(statement); err == nil {
			t.Fatalf("system schema query was accepted: %s", statement)
		}
	}
}

func TestDialectNormalizesPostgresErrors(t *testing.T) {
	err := Dialect{}.NormalizeError(&pgconn.PgError{
		Code: "42501", Message: "permission denied", Detail: "sensitive detail",
	})
	if err == nil || !strings.Contains(err.Error(), "SQLSTATE 42501") || strings.Contains(err.Error(), "sensitive detail") {
		t.Fatalf("unexpected normalized error: %v", err)
	}
}
