package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codebridge/internal/config"
)

func TestDatabaseCLIAddListAndRemove(t *testing.T) {
	configDir := t.TempDir()
	dataDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("CODEBRIDGE_CONFIG_PATH", configPath)
	t.Setenv("CODEBRIDGE_DATA_DIR", dataDir)

	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		app := App{Name: "codebridge", Version: "test", Tier: "pro", Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}
		err := app.Run(context.Background(), args)
		return stdout.String() + stderr.String(), err
	}

	output, err := run(
		"database", "add", "db.test",
		"--driver", "postgres",
		"--environment", "dev",
		"--credential-env", "CODEBRIDGE_DB_TEST_DSN",
		"--allowed-schemas", "public,app",
		"--denied-tables", "app.secrets",
		"--mask-columns", "*.password,*.token",
		"--no-prompt",
	)
	if err != nil {
		t.Fatalf("database add failed: %v\n%s", err, output)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	connection, ok := loaded.Database.Connections["db.test"]
	if !ok || connection.Driver != "postgres" || connection.CredentialRef.Name != "CODEBRIDGE_DB_TEST_DSN" {
		t.Fatalf("unexpected saved connection: %#v", connection)
	}
	if len(connection.Access.AllowedSchemas) != 2 || connection.Access.DeniedTables[0] != "app.secrets" {
		t.Fatalf("unexpected access policy: %#v", connection.Access)
	}

	output, err = run("database", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"alias": "db.test"`) || strings.Contains(output, "postgres://") {
		t.Fatalf("unexpected safe list output: %s", output)
	}

	if err := os.MkdirAll(filepath.Dir(config.DotEnvPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.DotEnvPath(), []byte("KEEP=value\nCODEBRIDGE_DB_TEST_DSN=super-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err = run("database", "remove", "db.test")
	if err != nil {
		t.Fatalf("database remove failed: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(config.DotEnvPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "CODEBRIDGE_DB_TEST_DSN") || !strings.Contains(string(raw), "KEEP=value") {
		t.Fatalf("database credential cleanup changed wrong values: %s", raw)
	}
	loaded, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Database.Enabled || len(loaded.Database.Connections) != 0 {
		t.Fatalf("database config was not disabled after last removal: %#v", loaded.Database)
	}
}

func TestDatabaseCLIRejectsDuplicateWithoutForce(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("CODEBRIDGE_CONFIG_PATH", filepath.Join(configDir, "config.json"))
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	app := App{Name: "codebridge", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	args := []string{"database", "add", "db.test", "--driver", "postgres", "--credential-env", "CODEBRIDGE_DB_TEST_DSN", "--no-prompt"}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate alias was not rejected: %v", err)
	}
}

func TestCredentialEnvForAlias(t *testing.T) {
	if got, want := credentialEnvForAlias("db.mysql-dev"), "CODEBRIDGE_DB_DB_MYSQL_DEV_DSN"; got != want {
		t.Fatalf("credential env = %q, want %q", got, want)
	}
}
