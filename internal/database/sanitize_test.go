package database

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizeErrorRedactsDatabaseCredentials(t *testing.T) {
	for _, message := range []string{
		"connect postgres://user:secret@db.example/app failed",
		"connect mysql://user:secret@db.example/app failed",
		"dial user:secret@tcp(db.example:3306)/app failed",
	} {
		got := SanitizeError(errors.New(message))
		if strings.Contains(got, "secret") {
			t.Fatalf("credential leaked from %q as %q", message, got)
		}
		if !strings.Contains(got, "[redacted]") {
			t.Fatalf("redaction marker missing from %q", got)
		}
	}
}
