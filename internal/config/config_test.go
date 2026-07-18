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

func TestValidateRequiresAuthForNonLoopbackHost(t *testing.T) {
	cfg := Default()
	cfg.Host = "0.0.0.0"
	if err := cfg.Validate(false); err == nil {
		t.Fatal("expected non-loopback host without auth to fail")
	}
	cfg.AuthToken = "secret"
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("authenticated non-loopback host rejected: %v", err)
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

func TestRemoveDotEnvKeysPreservesCommentsAndOtherValues(t *testing.T) {
	value := RemoveDotEnvKeys(
		"A=1\n# keep\nCODEBRIDGE_MEMORY_ENABLED=true\nCODEBRIDGE_MEMORY_PROVIDER=agentmemory\nCODEBRIDGE_MEMORY_SECRET=secret\n",
		"CODEBRIDGE_MEMORY_ENABLED", "CODEBRIDGE_MEMORY_PROVIDER",
	)
	if strings.Contains(value, "CODEBRIDGE_MEMORY_ENABLED") || strings.Contains(value, "CODEBRIDGE_MEMORY_PROVIDER") {
		t.Fatalf("memory config keys were not removed: %q", value)
	}
	for _, want := range []string{"A=1", "# keep", "CODEBRIDGE_MEMORY_SECRET=secret"} {
		if !strings.Contains(value, want) {
			t.Fatalf("removed unrelated value %q from %q", want, value)
		}
	}
}

func TestConfigIDIncludesMemorySettingsAndSecretFingerprint(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codebridge")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Memory.Enabled = true
	cfg.Memory.Provider = "agentmemory"
	base := cfg.ConfigID(binary, []byte("widget"))

	cfg.Memory.AgentID = "another-agent"
	if got := cfg.ConfigID(binary, []byte("widget")); got == base {
		t.Fatal("ConfigID did not change when memory agent ID changed")
	}
	cfg.Memory.AgentID = "chatgpt-codebridge"

	t.Setenv(cfg.Memory.SecretEnv, "first-secret")
	first := cfg.ConfigID(binary, []byte("widget"))
	t.Setenv(cfg.Memory.SecretEnv, "second-secret")
	second := cfg.ConfigID(binary, []byte("widget"))
	if first == second {
		t.Fatal("ConfigID did not change when memory secret changed")
	}
}

func TestValidateRejectsSecretsInsideMemoryOptions(t *testing.T) {
	cfg := Default()
	cfg.Memory.Enabled = true
	cfg.Memory.Provider = "agentmemory"
	cfg.Memory.Options = map[string]any{
		"transport": map[string]any{"apiKey": "must-not-be-persisted"},
	}
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "memory.secretEnv") {
		t.Fatalf("expected sensitive memory option to be rejected, got %v", err)
	}
	cfg.Memory.Options = map[string]any{"contextFallback": true, "contextPath": "/agentmemory/context"}
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("safe memory options rejected: %v", err)
	}
}
