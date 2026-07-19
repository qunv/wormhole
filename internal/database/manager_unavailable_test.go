package database

import (
	"context"
	"strings"
	"testing"

	"codebridge/internal/config"
)

func TestManagerListReturnsUnavailableOptionalConnectionWithoutPanic(t *testing.T) {
	cfg := managerTestConfig()
	connection := cfg.Connections["db.dev"]
	connection.Required = false
	connection.CredentialRef.Name = "CODEBRIDGE_MISSING_DATABASE_DSN"
	cfg.Connections["db.dev"] = connection
	manager, err := NewManager(cfg, map[string]Constructor{
		"test": func(string, config.DatabaseConnectionConfig, string) (Connection, error) {
			t.Fatal("constructor must not be called without a credential")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	results := manager.List(context.Background(), true)
	if len(results) != 1 || results[0].Available || !strings.Contains(results[0].Error, "provider") {
		t.Fatalf("unexpected unavailable summary: %#v", results)
	}
}
