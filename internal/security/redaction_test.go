package security

import (
	"fmt"
	"strings"
	"testing"
)

func TestDatabaseArgumentsAreRedacted(t *testing.T) {
	redacted := RedactDeep(map[string]any{
		"alias":  "db.prod",
		"sql":    "SELECT secret FROM users WHERE id = $1",
		"params": []any{"private-id"},
		"rows":   []any{map[string]any{"secret": "private-value"}},
		"dsn":    "postgres://user:password@database/prod",
	}, 0)
	text := fmt.Sprint(redacted)
	for _, secret := range []string{"SELECT secret", "private-id", "private-value", "postgres://"} {
		if strings.Contains(text, secret) {
			t.Fatalf("database audit argument leaked %q in %s", secret, text)
		}
	}
	if !strings.Contains(text, "db.prod") {
		t.Fatalf("safe alias metadata was removed: %s", text)
	}
}
