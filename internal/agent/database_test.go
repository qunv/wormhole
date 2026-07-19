package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestDatabaseAuditMetadataExcludesPayloads(t *testing.T) {
	metadata := databaseAuditMetadata("db_query", map[string]any{
		"alias": "db.prod", "sql": "SELECT secret FROM users", "params": []any{"private"},
	}, map[string]any{
		"connection_alias": "db.prod", "environment": "prod", "read_only": true,
		"query_hash": "sha256:abc", "elapsed_ms": int64(12), "row_count": 3,
		"truncated": false, "rows": []any{"private-row"}, "columns": []any{"secret"},
	})
	text := fmt.Sprint(metadata)
	for _, forbidden := range []string{"SELECT secret", "private", "private-row", "columns", "rows"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("audit metadata leaked %q in %s", forbidden, text)
		}
	}
	for _, expected := range []string{"db.prod", "prod", "sha256:abc", "row_count"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("audit metadata omitted %q in %s", expected, text)
		}
	}
}
