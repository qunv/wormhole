package config

import (
	"strings"
	"testing"
)

func credentialTestConnection(provider, name string) DatabaseConnectionConfig {
	return DatabaseConnectionConfig{
		Driver: "postgres", Environment: "dev",
		CredentialRef: CredentialReference{Provider: provider, Name: name},
		Access:        DatabaseAccessConfig{Mode: "read-only"},
		Limits: DatabaseLimitsConfig{
			QueryTimeoutMS: 1000, MaxRows: 10, MaxResultBytes: 1024,
			MaxCellBytes: 128, MaxConcurrentQueries: 1,
		},
		Pool: DatabasePoolConfig{MaxOpen: 1, MaxIdle: 1, MaxLifetimeSeconds: 60},
	}
}

func TestDatabaseConfigAcceptsExternalCredentialProviderNames(t *testing.T) {
	cfg := Default()
	cfg.Database.Enabled = true
	cfg.Database.Connections = map[string]DatabaseConnectionConfig{
		"db.external": credentialTestConnection("vault", "secret/data/codebridge"),
	}
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("syntactically valid external credential provider was rejected: %v", err)
	}

	connection := cfg.Database.Connections["db.external"]
	connection.CredentialRef.Provider = "Vault/Prod"
	cfg.Database.Connections["db.external"] = connection
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "provider must match") {
		t.Fatalf("invalid credential provider name was not rejected: %v", err)
	}
}

func TestEnvironmentCredentialStillRequiresEnvironmentVariableName(t *testing.T) {
	cfg := Default()
	cfg.Database.Enabled = true
	cfg.Database.Connections = map[string]DatabaseConnectionConfig{
		"db.env": credentialTestConnection("env", "not/a/name"),
	}
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "environment variable name") {
		t.Fatalf("invalid env credential name was not rejected: %v", err)
	}
}
