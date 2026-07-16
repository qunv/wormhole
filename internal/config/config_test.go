package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAppConfigDirUsesLowercaseNameOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("XDG config directory is only used on Unix-like systems")
	}
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	want := filepath.Join(base, "codebridge")
	if got := AppConfigDir(); got != want {
		t.Fatalf("AppConfigDir() = %q, want %q", got, want)
	}
}

func TestSaveDoesNotPersistSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CODEBRIDGE_CONFIG_PATH", path)
	cfg := Default()
	cfg.Workspace = t.TempDir()
	cfg.AuthToken = "auth-secret"
	cfg.ApprovalToken = "approval-secret"
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "auth-secret") || strings.Contains(string(raw), "approval-secret") {
		t.Fatalf("secret persisted in config: %s", raw)
	}
}

func TestDotEnvMergePreservesUnrelatedLines(t *testing.T) {
	value := MergeDotEnv("A=1\n# keep\nCONTROL_PLANE_API_KEY=old\n", map[string]string{"CONTROL_PLANE_API_KEY": "new value"})
	if !strings.Contains(value, "A=1") || !strings.Contains(value, "# keep") || !strings.Contains(value, `CONTROL_PLANE_API_KEY="new value"`) {
		t.Fatalf("unexpected merge: %q", value)
	}
}
