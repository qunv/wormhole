package workspaceregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"codebridge/internal/config"
)

func TestLoadMigratesPhaseOneRegistry(t *testing.T) {
	configureRegistryTestPaths(t)
	configPath := ConfigPath("api")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"version": 1,
		"workspaces": map[string]any{
			"api": map[string]any{
				"id": "api", "workspace": t.TempDir(),
				"configPath": configPath, "dataDir": DataDir("api"), "port": 8790,
			},
		},
	}
	raw, _ := json.Marshal(legacy)
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if registry.Version != CurrentVersion || !registry.Workspaces["api"].Enabled {
		t.Fatalf("legacy registry was not migrated: %#v", registry)
	}
}

func TestFingerprintChangesWithConfig(t *testing.T) {
	configureRegistryTestPaths(t)
	entry := Registration{
		ID: "api", Workspace: t.TempDir(), ConfigPath: ConfigPath("api"), DataDir: DataDir("api"), Enabled: true,
	}
	if err := os.MkdirAll(filepath.Dir(entry.ConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.ConfigPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(Registry{Workspaces: map[string]Registration{"api": entry}}); err != nil {
		t.Fatal(err)
	}
	first, err := Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.ConfigPath, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("fingerprint did not change with workspace config")
	}
}

func TestLoadMigratesLegacyDefaultPathsToCodebridgeLayout(t *testing.T) {
	base := t.TempDir()
	newHome := filepath.Join(base, "new-home")
	t.Setenv("CODEBRIDGE_HOME", newHome)
	t.Setenv("CODEBRIDGE_WORKSPACE_REGISTRY_PATH", filepath.Join(base, "registry.json"))
	switch runtime.GOOS {
	case "windows":
		t.Setenv("USERPROFILE", filepath.Join(base, "home"))
		t.Setenv("APPDATA", filepath.Join(base, "legacy-config"))
		t.Setenv("LOCALAPPDATA", filepath.Join(base, "legacy-state"))
	default:
		t.Setenv("HOME", filepath.Join(base, "home"))
		if runtime.GOOS != "darwin" {
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "legacy-config"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(base, "legacy-state"))
		}
	}

	legacy := Registry{Version: 2, Workspaces: map[string]Registration{
		"api": {
			ID: "api", Workspace: t.TempDir(), Enabled: true,
			ConfigPath: filepath.Join(config.LegacyConfigDir(), "workspaces", "api", "config.json"),
			DataDir:    filepath.Join(config.LegacyDataDir(), "instances", "api"),
		},
	}}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(Path(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Workspaces["api"]
	if entry.ConfigPath != ConfigPath("api") || entry.DataDir != DataDir("api") {
		t.Fatalf("legacy paths were not migrated: %#v", entry)
	}
	if registry.Version != CurrentVersion {
		t.Fatalf("registry version = %d, want %d", registry.Version, CurrentVersion)
	}
}

func TestValidateID(t *testing.T) {
	for _, valid := range []string{"api", "loyalty-api", "web_2"} {
		if err := ValidateID(valid); err != nil {
			t.Fatalf("valid id %q rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "default", "API space", "../api", "-api"} {
		if err := ValidateID(invalid); err == nil {
			t.Fatalf("invalid id %q accepted", invalid)
		}
	}
}

func TestSaveRejectsSharedConfigOrDataPaths(t *testing.T) {
	configureRegistryTestPaths(t)
	base := Registration{Workspace: t.TempDir(), Enabled: true}
	one := base
	one.ID, one.ConfigPath, one.DataDir = "one", ConfigPath("one"), DataDir("one")
	two := base
	two.ID, two.ConfigPath, two.DataDir = "two", ConfigPath("two"), one.DataDir
	if err := Save(Registry{Workspaces: map[string]Registration{"one": one, "two": two}}); err == nil {
		t.Fatal("shared data directory was accepted")
	}

	two.DataDir = DataDir("two")
	two.ConfigPath = one.ConfigPath
	if err := Save(Registry{Workspaces: map[string]Registration{"one": one, "two": two}}); err == nil {
		t.Fatal("shared config path was accepted")
	}
}

func configureRegistryTestPaths(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("APPDATA", filepath.Join(base, "config"))
	case "darwin":
		t.Setenv("HOME", base)
	default:
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	}
	t.Setenv("CODEBRIDGE_DATA_DIR", filepath.Join(base, "data"))
	t.Setenv("CODEBRIDGE_WORKSPACE_REGISTRY_PATH", filepath.Join(base, "registry", "workspaces.json"))
}
