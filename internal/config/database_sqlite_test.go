package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteDatabaseConfigRequiresFileRoot(t *testing.T) {
	cfg := Default()
	cfg.Database.Enabled = true
	cfg.Database.Connections = map[string]DatabaseConnectionConfig{
		"db.sqlite": {
			Driver: "sqlite", Environment: "dev",
			CredentialRef: CredentialReference{Provider: "env", Name: "CODEBRIDGE_SQLITE_PATH"},
			Access:        DatabaseAccessConfig{Mode: "read-only", AllowedSchemas: []string{"main"}},
			Limits: DatabaseLimitsConfig{
				QueryTimeoutMS: 1000, MaxRows: 10, MaxResultBytes: 1024,
				MaxCellBytes: 128, MaxConcurrentQueries: 1,
			},
			Pool: DatabasePoolConfig{MaxOpen: 1, MaxIdle: 1, MaxLifetimeSeconds: 60},
		},
	}
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "fileRoot") {
		t.Fatalf("missing SQLite fileRoot was not rejected: %v", err)
	}
	connection := cfg.Database.Connections["db.sqlite"]
	connection.FileRoot = t.TempDir()
	cfg.Database.Connections["db.sqlite"] = connection
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("valid SQLite config was rejected: %v", err)
	}

	connection.Driver = "postgres"
	cfg.Database.Connections["db.sqlite"] = connection
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "only supported for sqlite") {
		t.Fatalf("non-SQLite fileRoot was not rejected: %v", err)
	}
}

func TestNormalizeSQLiteFileRoot(t *testing.T) {
	cfg := Default()
	cfg.Database.Connections = map[string]DatabaseConnectionConfig{
		"db.sqlite": {Driver: "sqlite", FileRoot: "."},
	}
	normalize(&cfg)
	if !filepath.IsAbs(cfg.Database.Connections["db.sqlite"].FileRoot) {
		t.Fatalf("SQLite fileRoot was not normalized: %q", cfg.Database.Connections["db.sqlite"].FileRoot)
	}
}
