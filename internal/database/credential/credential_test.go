package credential

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"codebridge/internal/config"
)

func TestResolveEnvironmentCredential(t *testing.T) {
	t.Setenv("CODEBRIDGE_TEST_CREDENTIAL", "  secret-value  ")
	value, err := Resolve(context.Background(), config.CredentialReference{
		Provider: "env", Name: "CODEBRIDGE_TEST_CREDENTIAL",
	})
	if err != nil {
		t.Fatal(err)
	}
	if value != "secret-value" {
		t.Fatalf("credential = %q", value)
	}
}

func TestResolveFileCredential(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	dir := filepath.Join(base, "codebridge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "database.secret")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := Resolve(context.Background(), config.CredentialReference{Provider: "file", Name: "database.secret"})
	if err != nil {
		t.Fatal(err)
	}
	if value != "file-secret" {
		t.Fatalf("credential = %q", value)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(context.Background(), config.CredentialReference{Provider: "file", Name: path}); err == nil || !strings.Contains(err.Error(), "writable") {
			t.Fatalf("unsafe credential file permissions were accepted: %v", err)
		}
	}
}

func TestRegisterExternalCredentialProvider(t *testing.T) {
	Register("testvault", func(context.Context, config.CredentialReference) (string, error) {
		return "vault-secret", nil
	})
	value, err := Resolve(context.Background(), config.CredentialReference{Provider: "testvault", Name: "database/path"})
	if err != nil {
		t.Fatal(err)
	}
	if value != "vault-secret" {
		t.Fatalf("credential = %q", value)
	}
}
