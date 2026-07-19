package sqlite

import (
	"strings"
	"testing"
)

func TestSQLiteDialectPrimitivesAndSafety(t *testing.T) {
	dialect := Dialect{}
	if got := dialect.Placeholder(3); got != "?" {
		t.Fatalf("placeholder = %q", got)
	}
	quoted, err := dialect.QuoteIdentifier(`users"archive`)
	if err != nil {
		t.Fatal(err)
	}
	if quoted != `"users""archive"` {
		t.Fatalf("quoted identifier = %q", quoted)
	}
	if got := dialect.ExplainSQL("SELECT 1"); !strings.HasPrefix(got, "EXPLAIN QUERY PLAN") {
		t.Fatalf("unexpected explain SQL: %q", got)
	}
	for _, statement := range []string{
		"SELECT load_extension('x')",
		"SELECT readfile('/etc/passwd')",
		"SELECT writefile('/tmp/x', data)",
		"SELECT * FROM temp.items",
		"SELECT * FROM sqlite_schema",
		"SELECT * FROM main.sqlite_master",
	} {
		if err := dialect.ValidateReadOnlySQL(statement); err == nil {
			t.Fatalf("unsafe SQLite statement was accepted: %s", statement)
		}
	}
	if err := dialect.ValidateReadOnlySQL("SELECT readfile FROM metrics"); err != nil {
		t.Fatalf("column sharing a forbidden function name was rejected: %v", err)
	}
}
