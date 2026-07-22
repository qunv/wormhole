package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestCodebridgeHomeOverrideUnifiesPaths(t *testing.T) {
	base := filepath.Join(t.TempDir(), "custom-codebridge")
	t.Setenv("CODEBRIDGE_HOME", base)
	if got := AppHomeDir(); got != base {
		t.Fatalf("AppHomeDir() = %q, want %q", got, base)
	}
	if got := AppConfigDir(); got != base {
		t.Fatalf("AppConfigDir() = %q, want %q", got, base)
	}
	if got, want := AppDataDir(), filepath.Join(base, "state"); got != want {
		t.Fatalf("AppDataDir() = %q, want %q", got, want)
	}
	if got, want := ConfigPath(), filepath.Join(base, "config.json"); got != want {
		t.Fatalf("ConfigPath() = %q, want %q", got, want)
	}
	if got, want := DotEnvPath(), filepath.Join(base, ".env"); got != want {
		t.Fatalf("DotEnvPath() = %q, want %q", got, want)
	}
}

func TestMigrateLegacyLayoutCopiesWithoutOverwriting(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	configureLegacyLayoutTestEnvironment(t, home, base)

	legacyConfig := LegacyConfigDir()
	legacyData := LegacyDataDir()
	if err := os.MkdirAll(filepath.Join(legacyConfig, "workspaces", "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(legacyData, "instances", "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(legacyData, "workspaces", "0123456789abcdef"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(legacyConfig, "config.json"):                                "legacy-config",
		filepath.Join(legacyConfig, ".env"):                                       "SECRET=legacy\n",
		filepath.Join(legacyConfig, "workspaces", "api", "config.json"):           "workspace-config",
		filepath.Join(legacyData, "launcher.log"):                                 "legacy-log",
		filepath.Join(legacyData, "instances", "api", "audit.log"):                "legacy-audit",
		filepath.Join(legacyData, "workspaces", "0123456789abcdef", "notes.json"): "workspace-state",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(AppConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(), []byte("keep-new-config"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyLayout(); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, ConfigPath(), "keep-new-config")
	assertFileContent(t, DotEnvPath(), "SECRET=legacy\n")
	assertFileContent(t, filepath.Join(AppConfigDir(), "workspaces", "api", "config.json"), "workspace-config")
	assertFileContent(t, LogPath(), "legacy-log")
	assertFileContent(t, filepath.Join(AppDataDir(), "instances", "api", "audit.log"), "legacy-audit")
	assertFileContent(t, filepath.Join(AppDataDir(), "workspaces", "0123456789abcdef", "notes.json"), "workspace-state")
	assertMissing(t, filepath.Join(AppConfigDir(), "workspaces", "0123456789abcdef", "notes.json"))
	assertMissing(t, filepath.Join(AppDataDir(), "workspaces", "api", "config.json"))
	assertFileContent(t, filepath.Join(legacyConfig, "config.json"), "legacy-config")
}

func TestLoadFileMigratesLegacyDefaultAssetPaths(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	configureLegacyLayoutTestEnvironment(t, home, base)
	newHome := filepath.Join(base, "new-codebridge")
	t.Setenv("CODEBRIDGE_HOME", newHome)

	path := filepath.Join(base, "config.json")
	legacyProfileDir := filepath.Join(LegacyDataDir(), "profiles")
	legacyTunnelBin := filepath.Join(LegacyDataDir(), tunnelExecutable())
	raw := `{"profileDir":` + strconv.Quote(legacyProfileDir) + `,"tunnelBin":` + strconv.Quote(legacyTunnelBin) + `}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.ProfileDir, filepath.Join(newHome, "state", "profiles"); got != want {
		t.Fatalf("ProfileDir = %q, want %q", got, want)
	}
	if got, want := cfg.TunnelBin, filepath.Join(newHome, "state", tunnelExecutable()); got != want {
		t.Fatalf("TunnelBin = %q, want %q", got, want)
	}
}

func configureLegacyLayoutTestEnvironment(t *testing.T, home, base string) {
	t.Helper()
	for _, name := range []string{"CODEBRIDGE_HOME", "CODEBRIDGE_CONFIG_PATH", "CODEBRIDGE_DATA_DIR", "CODEBRIDGE_WORKSPACE_REGISTRY_PATH"} {
		t.Setenv(name, "")
	}
	switch runtime.GOOS {
	case "windows":
		t.Setenv("USERPROFILE", home)
		t.Setenv("APPDATA", filepath.Join(base, "legacy-config"))
		t.Setenv("LOCALAPPDATA", filepath.Join(base, "legacy-state"))
	default:
		t.Setenv("HOME", home)
		if runtime.GOOS != "darwin" {
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "legacy-config"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(base, "legacy-state"))
		}
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := string(raw); got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to be absent, got %v", path, err)
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

func TestLegacyIntegrationFieldsAreDroppedOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CODEBRIDGE_CONFIG_PATH", path)
	legacy := `{
  "workspace": ".",
  "database": {"enabled": true, "connections": {"db.legacy": {}}},
  "figmaDesktopMcpUrl": "http://127.0.0.1:3845/mcp",
  "figmaDesktopTimeoutMs": 30000,
  "mcpServers": {
    "postgres_prod": {
      "command": "uvx",
      "toolPrefix": "prod_db"
    }
  }
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("legacy config should load for migration: %v", err)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"\"database\"", "figmaDesktopMcpUrl", "figmaDesktopTimeoutMs", "toolPrefix", "prod_db"} {
		if strings.Contains(string(raw), removed) {
			t.Fatalf("legacy field %q persisted after save: %s", removed, raw)
		}
	}
	if !strings.Contains(string(raw), `"postgres_prod"`) {
		t.Fatalf("upstream MCP server was lost during migration: %s", raw)
	}
}
