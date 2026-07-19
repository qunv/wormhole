package mysql

import (
	"strings"
	"testing"
	"time"

	"codebridge/internal/config"
)

func mysqlTestConfig(environment string) config.DatabaseConnectionConfig {
	return config.DatabaseConnectionConfig{
		Driver: "mysql", Environment: environment,
		Access: config.DatabaseAccessConfig{Mode: "read-only", AllowedSchemas: []string{"app"}},
		Limits: config.DatabaseLimitsConfig{
			QueryTimeoutMS: 1500, MaxRows: 10, MaxResultBytes: 4096,
			MaxCellBytes: 256, MaxConcurrentQueries: 2,
		},
		Pool: config.DatabasePoolConfig{MaxOpen: 2, MaxIdle: 1, MaxLifetimeSeconds: 60},
	}
}

func TestParseAndHardenDSN(t *testing.T) {
	cfg, err := parseAndHardenDSN("user:password@tcp(127.0.0.1:3306)/app", mysqlTestConfig("dev"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ParseTime || cfg.Loc != time.UTC || cfg.DBName != "app" {
		t.Fatalf("unexpected hardened config: ParseTime=%v Loc=%v DBName=%q", cfg.ParseTime, cfg.Loc, cfg.DBName)
	}
}

func TestParseAndHardenDSNRejectsUnsafeOptions(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{"multi statements", "user:password@tcp(localhost:3306)/app?multiStatements=true", "multiStatements"},
		{"interpolated params", "user:password@tcp(localhost:3306)/app?interpolateParams=true", "interpolateParams"},
		{"all files", "user:password@tcp(localhost:3306)/app?allowAllFiles=true", "allowAllFiles"},
		{"cleartext password", "user:password@tcp(localhost:3306)/app?allowCleartextPasswords=true", "allowCleartextPasswords"},
		{"plaintext fallback", "user:password@tcp(localhost:3306)/app?allowFallbackToPlaintext=true", "plaintext"},
		{"old password", "user:password@tcp(localhost:3306)/app?allowOldPasswords=true", "allowOldPasswords"},
		{"skip verify", "user:password@tcp(localhost:3306)/app?tls=skip-verify", "TLS"},
		{"preferred TLS", "user:password@tcp(localhost:3306)/app?tls=preferred", "plaintext"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseAndHardenDSN(test.dsn, mysqlTestConfig("dev"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestParseAndHardenDSNEnforcesSchemaBoundary(t *testing.T) {
	for _, dsn := range []string{
		"user:password@tcp(localhost:3306)/",
		"user:password@tcp(localhost:3306)/other",
	} {
		if _, err := parseAndHardenDSN(dsn, mysqlTestConfig("dev")); err == nil || !strings.Contains(err.Error(), "allowedSchemas") {
			t.Fatalf("DSN %q was not rejected: %v", dsn, err)
		}
	}
}

func TestParseAndHardenDSNRequiresTLSForProductionTCP(t *testing.T) {
	if _, err := parseAndHardenDSN("user:password@tcp(db.example:3306)/app", mysqlTestConfig("production")); err == nil || !strings.Contains(err.Error(), "require verified TLS") {
		t.Fatalf("production TCP without TLS was not rejected: %v", err)
	}
	if _, err := parseAndHardenDSN("user:password@unix(/tmp/mysql.sock)/app", mysqlTestConfig("production")); err != nil {
		t.Fatalf("production Unix socket should not require TLS: %v", err)
	}
	if _, err := parseAndHardenDSN("user:password@tcp(db.example:3306)/app?tls=true", mysqlTestConfig("production")); err != nil {
		t.Fatalf("verified production TLS was rejected: %v", err)
	}
}
