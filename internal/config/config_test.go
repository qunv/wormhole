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
	markerPath := filepath.Join(AppHomeDir(), legacyLayoutMigrationMarker)
	assertFileContent(t, markerPath, "completed\n")

	// A completed migration must not resurrect state that was intentionally
	// removed from the canonical tree while the rollback-safe legacy copy stays.
	migratedState := filepath.Join(AppDataDir(), "workspaces", "0123456789abcdef")
	if err := os.RemoveAll(migratedState); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyLayout(); err != nil {
		t.Fatal(err)
	}
	assertMissing(t, migratedState)
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

func TestAdminJSONParsingPreservesDefaultsForOmittedFields(t *testing.T) {
	cfg, err := ParseJSON([]byte(`{"workspace":"/tmp","mode":"safe","policy":"balanced","port":8789,"host":"127.0.0.1"}`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Audit || !cfg.AuditArgs {
		t.Fatalf("omitted audit defaults were lost: audit=%t auditArgs=%t", cfg.Audit, cfg.AuditArgs)
	}
	if cfg.MaxProcesses != Default().MaxProcesses || cfg.MaxConcurrentToolCalls != Default().MaxConcurrentToolCalls || cfg.Memory.TokenBudget != Default().Memory.TokenBudget {
		t.Fatalf("omitted resource defaults were lost: %#v", cfg)
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
	cfg.AuthToken = strings.Repeat("a", 16)
	cfg.ApprovalToken = strings.Repeat("p", 16)
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

func TestConfigIDIncludesEffectiveSecurityLimitsAndTunnelIdentity(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codebridge")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	widget := []byte("widget")
	base := Default()
	baseID := base.ConfigID(binary, widget)

	mutations := []struct {
		name   string
		mutate func(*Config)
	}{
		{"auth token", func(cfg *Config) { cfg.AuthToken = strings.Repeat("a", 16) }},
		{"approval token", func(cfg *Config) { cfg.ApprovalToken = strings.Repeat("p", 16) }},
		{"host", func(cfg *Config) { cfg.Host = "localhost" }},
		{"origins", func(cfg *Config) { cfg.AllowedOrigins = []string{"https://example.test"} }},
		{"audit arguments", func(cfg *Config) { cfg.AuditArgs = !cfg.AuditArgs }},
		{"body limit", func(cfg *Config) { cfg.MaxBodyBytes++ }},
		{"process limit", func(cfg *Config) { cfg.MaxProcesses++ }},
		{"tool concurrency limit", func(cfg *Config) { cfg.MaxConcurrentToolCalls++ }},
		{"tunnel id", func(cfg *Config) { cfg.TunnelID = "another-tunnel" }},
		{"profile directory", func(cfg *Config) { cfg.ProfileDir = filepath.Join(t.TempDir(), "profiles") }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.mutate(&cfg)
			if got := cfg.ConfigID(binary, widget); got == baseID {
				t.Fatalf("ConfigID did not change after %s changed", test.name)
			}
		})
	}

	first := base.ConfigIDWithInputs(NewIdentityInputs(binary, widget, strings.Repeat("r", 16)+"1"))
	second := base.ConfigIDWithInputs(NewIdentityInputs(binary, widget, strings.Repeat("r", 16)+"2"))
	if first == second {
		t.Fatal("ConfigID did not change when the tunnel Runtime API key changed")
	}
}

func TestConfigIDDoesNotEmbedRawSecrets(t *testing.T) {
	cfg := Default()
	cfg.AuthToken = strings.Repeat("a", 24)
	cfg.ApprovalToken = strings.Repeat("p", 24)
	runtimeSecret := strings.Repeat("r", 24)
	inputs := NewIdentityInputs("missing", []byte("widget"), runtimeSecret)
	identity := cfg.ConfigIDWithInputs(inputs)
	for _, secret := range []string{cfg.AuthToken, cfg.ApprovalToken, runtimeSecret} {
		if strings.Contains(identity, secret) {
			t.Fatalf("ConfigID leaked raw secret %q", secret)
		}
	}
}

func TestValidateBoundsConcurrentToolCalls(t *testing.T) {
	cfg := Default()
	cfg.MaxConcurrentToolCalls = 0
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "maxConcurrentToolCalls") {
		t.Fatalf("zero concurrency limit was accepted: %v", err)
	}
	cfg.MaxConcurrentToolCalls = 1025
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "must not exceed 1024") {
		t.Fatalf("oversized concurrency limit was accepted: %v", err)
	}
}

func TestValidateRejectsSecretsInsideMemoryOptions(t *testing.T) {
	cfg := Default()
	cfg.Memory.Enabled = true
	cfg.Memory.Provider = "agentmemory"
	cfg.Memory.Options = map[string]any{
		"transport": map[string]any{"apiKey": strings.Repeat("k", 24)},
	}
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "memory.secretEnv") {
		t.Fatalf("expected sensitive memory option to be rejected, got %v", err)
	}
	cfg.Memory.Options = map[string]any{"contextFallback": true, "contextPath": "/agentmemory/context"}
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("safe memory options rejected: %v", err)
	}
}

func TestLoadRejectsUnknownAndDuplicateConfigFields(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown top level":        `{"workspace":".","workspaceId":"api"}`,
		"unknown nested MCP field": `{"workspace":".","mcpServers":{"postgres":{"command":"uvx","workspaceId":["api"]}}}`,
		"duplicate top level":      `{"workspace":".","mode":"safe","mode":"full"}`,
		"duplicate nested":         `{"workspace":".","mcpServers":{"postgres":{"command":"uvx","startupMode":"lazy","startupMode":"eager"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFile(path); err == nil {
				t.Fatalf("invalid config was accepted: %s", raw)
			}
		})
	}
}

func TestEffectiveTunnelsSupportsLegacyAndNamedConfigurations(t *testing.T) {
	legacy := Default()
	legacy.TunnelID = "tunnel_legacy"
	items := legacy.EffectiveTunnels()
	if len(items) != 1 || items[0].Name != "default" || !items[0].Legacy || items[0].Config.Mode != "full" {
		t.Fatalf("legacy tunnel was not preserved: %#v", items)
	}

	disabled := false
	named := Default()
	named.TunnelID = "ignored-legacy"
	named.Tunnels = map[string]TunnelConfig{
		"full": {TunnelID: "tunnel_full", Mode: "full"},
		"fast": {TunnelID: "tunnel_fast", Mode: "fast", RuntimeKeyEnv: "FAST_KEY"},
		"off":  {Enabled: &disabled, TunnelID: "tunnel_off", Mode: "full"},
	}
	normalize(&named)
	all := named.EffectiveTunnels()
	if len(all) != 3 || all[0].Name != "fast" || all[1].Name != "full" || all[2].Name != "off" {
		t.Fatalf("named tunnels are not stable and sorted: %#v", all)
	}
	if all[0].Config.Profile != "codebridge-fast" || all[1].Config.RuntimeKeyEnv != "CONTROL_PLANE_API_KEY" {
		t.Fatalf("named tunnel defaults were not applied: %#v", all)
	}
	enabled := named.EnabledTunnels()
	if len(enabled) != 2 || enabled[0].Name != "fast" || enabled[1].Name != "full" {
		t.Fatalf("disabled tunnel was not filtered: %#v", enabled)
	}
}

func TestValidateRejectsDuplicateEnabledTunnelIdentityAndProfile(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"tunnel id": func(cfg *Config) {
			cfg.Tunnels["full"] = TunnelConfig{TunnelID: "same", Mode: "full", Profile: "full", RuntimeKeyEnv: "FULL_KEY"}
			cfg.Tunnels["fast"] = TunnelConfig{TunnelID: "same", Mode: "fast", Profile: "fast", RuntimeKeyEnv: "FAST_KEY"}
		},
		"profile": func(cfg *Config) {
			cfg.Tunnels["full"] = TunnelConfig{TunnelID: "full", Mode: "full", Profile: "same", RuntimeKeyEnv: "FULL_KEY"}
			cfg.Tunnels["fast"] = TunnelConfig{TunnelID: "fast", Mode: "fast", Profile: "same", RuntimeKeyEnv: "FAST_KEY"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			cfg.Tunnels = map[string]TunnelConfig{}
			mutate(&cfg)
			normalize(&cfg)
			if err := cfg.Validate(false); err == nil {
				t.Fatalf("duplicate %s was accepted", name)
			}
		})
	}
}

func TestTunnelValidationRejectsNormalizedNameCollisionsAndProfileEscapes(t *testing.T) {
	cfg := Default()
	cfg.Tunnels = map[string]TunnelConfig{
		"fast":   {TunnelID: "one", Mode: "fast", Profile: "fast"},
		" FAST ": {TunnelID: "two", Mode: "full", Profile: "full"},
	}
	if err := validateTunnelMapKeys(cfg.Tunnels); err == nil {
		t.Fatal("normalized tunnel name collision was accepted")
	}

	cfg = Default()
	cfg.Profile = "../outside"
	if err := cfg.Validate(false); err == nil {
		t.Fatal("legacy profile path escape was accepted")
	}
	cfg = Default()
	cfg.Tunnels = map[string]TunnelConfig{
		"fast": {TunnelID: "one", Mode: "fast", Profile: "../outside", RuntimeKeyEnv: "FAST_KEY"},
	}
	if err := cfg.Validate(false); err == nil {
		t.Fatal("named profile path escape was accepted")
	}
}

func TestValidateTreatsImplicitYamlProfileNamesAsDuplicates(t *testing.T) {
	cfg := Default()
	cfg.Tunnels = map[string]TunnelConfig{
		"fast": {TunnelID: "one", Mode: "fast", Profile: "same", RuntimeKeyEnv: "FAST_KEY"},
		"full": {TunnelID: "two", Mode: "full", Profile: "same.yaml", RuntimeKeyEnv: "FULL_KEY"},
	}
	if err := cfg.Validate(false); err == nil {
		t.Fatal("equivalent profile file names were accepted")
	}
}

func TestTunnelLogPathForConfinesInvalidStateNames(t *testing.T) {
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	path := TunnelLogPathFor("../../outside")
	if filepath.Dir(path) != AppDataDir() || strings.Contains(filepath.Base(path), "..") {
		t.Fatalf("invalid tunnel name escaped log directory: %s", path)
	}
	if got := TunnelLogPathFor("fast"); got != filepath.Join(AppDataDir(), "tunnel-fast.log") {
		t.Fatalf("named tunnel log path = %s", got)
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
