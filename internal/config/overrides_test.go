// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOverrideFileInheritsAndMergesNestedObjects(t *testing.T) {
	base := Default()
	base.Mode = "full"
	base.Memory.Enabled = true
	base.Memory.Provider = "agentmemory"
	base.Memory.AgentID = "global-agent"
	base.MCPServers["postgres"] = MCPServerConfig{
		Transport: "stdio", Command: "uvx", Args: []string{"postgres-mcp"}, StartupMode: MCPStartupModeLazy,
		EnvRefs: map[string]string{"DATABASE_URI": "GLOBAL_DATABASE_URI"},
		Policy:  MCPServerPolicyConfig{Default: "approval", ReadOnlyTools: []string{"list_schemas"}},
	}
	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{
  "policy": "strict",
  "memory": {"agentId": "workspace-agent"},
  "mcpServers": {
    "postgres": {
      "envRefs": {"DATABASE_URI": "WORKSPACE_DATABASE_URI"},
      "policy": {"default": "deny"}
    }
  }
}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadOverrideFile(path, base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "full" || cfg.Policy != "strict" {
		t.Fatalf("unexpected scalar inheritance: mode=%q policy=%q", cfg.Mode, cfg.Policy)
	}
	if !cfg.Memory.Enabled || cfg.Memory.Provider != "agentmemory" || cfg.Memory.AgentID != "workspace-agent" {
		t.Fatalf("memory override did not merge: %#v", cfg.Memory)
	}
	server := cfg.MCPServers["postgres"]
	if server.Command != "uvx" || server.StartupMode != MCPStartupModeLazy {
		t.Fatalf("server transport fields were not inherited: %#v", server)
	}
	if server.EnvRefs["DATABASE_URI"] != "WORKSPACE_DATABASE_URI" || server.Policy.Default != "deny" {
		t.Fatalf("server nested override was not applied: %#v", server)
	}
	if len(server.Policy.ReadOnlyTools) != 1 || server.Policy.ReadOnlyTools[0] != "list_schemas" {
		t.Fatalf("omitted array was not inherited: %#v", server.Policy.ReadOnlyTools)
	}
}

func TestApplyOverrideReplacesArraysAndSupportsFalse(t *testing.T) {
	base := Default()
	base.Audit = true
	base.ExtraRoots = []string{"/one", "/two"}
	base.Tools.AllowedGroups = []string{"repo"}
	cfg, err := ApplyOverride(base, map[string]any{
		"audit":      false,
		"extraRoots": []any{},
		"tools": map[string]any{
			"allowedGroups": []any{"filesystem", "workflow"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Audit {
		t.Fatal("false boolean override was ignored")
	}
	if len(cfg.ExtraRoots) != 0 {
		t.Fatalf("empty array did not replace inherited roots: %#v", cfg.ExtraRoots)
	}
	if got := strings.Join(cfg.Tools.AllowedGroups, ","); got != "filesystem,workflow" {
		t.Fatalf("array replacement = %q", got)
	}
}

func TestApplyOverrideDisablesInheritedMCPServer(t *testing.T) {
	base := Default()
	base.MCPServers["postgres"] = MCPServerConfig{Command: "uvx", StartupMode: MCPStartupModeLazy}
	cfg, err := ApplyOverride(base, map[string]any{
		"mcpServers": map[string]any{
			"postgres": map[string]any{"enabled": false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := cfg.MCPServers["postgres"]
	if server.IsEnabled() {
		t.Fatalf("inherited MCP server was not disabled: %#v", server)
	}
	if server.Command != "uvx" || server.StartupMode != MCPStartupModeLazy {
		t.Fatalf("disabling server discarded inherited settings: %#v", server)
	}
}

func TestApplyOverrideNullRemovesInheritedMCPEntry(t *testing.T) {
	base := Default()
	base.MCPServers["keep"] = MCPServerConfig{Command: "keep"}
	base.MCPServers["remove"] = MCPServerConfig{Command: "remove"}
	cfg, err := ApplyOverride(base, map[string]any{
		"mcpServers": map[string]any{"remove": nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := cfg.MCPServers["remove"]; exists {
		t.Fatalf("null did not remove inherited server: %#v", cfg.MCPServers)
	}
	if cfg.MCPServers["keep"].Command != "keep" {
		t.Fatalf("unrelated inherited server changed: %#v", cfg.MCPServers)
	}
}

func TestLoadOverrideFileMissingUsesBase(t *testing.T) {
	base := Default()
	base.Mode = "full"
	base.MCPServers["postgres"] = MCPServerConfig{Command: "uvx"}
	cfg, err := LoadOverrideFile(filepath.Join(t.TempDir(), "missing.json"), base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "full" || cfg.MCPServers["postgres"].Command != "uvx" {
		t.Fatalf("missing override did not inherit base: %#v", cfg)
	}
}

func TestSaveOverrideFileStripsTokensAndValidatesEffectiveConfig(t *testing.T) {
	base := Default()
	path := filepath.Join(t.TempDir(), "config.json")
	override := map[string]any{
		"authToken": strings.Repeat("a", 16), "approvalToken": strings.Repeat("p", 16), "mode": "full",
	}
	if err := SaveOverrideFile(path, base, override); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("secret persisted in override: %s", raw)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["mode"] != "full" {
		t.Fatalf("override was not persisted: %#v", stored)
	}
	if err := SaveOverrideFile(path, base, map[string]any{"mode": "invalid"}); err == nil {
		t.Fatal("invalid effective config was persisted")
	}
}

func TestSaveOverrideFileWritesAndReadStripsSchemaVersion(t *testing.T) {
	base := Default()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SaveOverrideFile(path, base, map[string]any{"mode": "full"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"schemaVersion": 1`) {
		t.Fatalf("workspace override schema version missing: %s", raw)
	}
	override, err := ReadOverrideFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if override["mode"] != "full" {
		t.Fatalf("override content changed: %#v", override)
	}
	if _, exists := override["schemaVersion"]; exists {
		t.Fatalf("schema metadata leaked into effective override: %#v", override)
	}
}

func TestCompactOverrideRemovesSnapshotValuesButPreservesExtraRoots(t *testing.T) {
	base := Default()
	base.Mode = "safe"
	base.Policy = "balanced"
	compacted, err := CompactOverride(base, map[string]any{
		"mode": "safe", "policy": "strict", "extraRoots": []any{},
		"memory": map[string]any{"provider": base.Memory.Provider, "agentId": "workspace-agent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := compacted["mode"]; exists {
		t.Fatalf("equal snapshot value was not removed: %#v", compacted)
	}
	if compacted["policy"] != "strict" || compacted["extraRoots"] == nil {
		t.Fatalf("meaningful workspace overrides were lost: %#v", compacted)
	}
	memory := compacted["memory"].(map[string]any)
	if _, exists := memory["provider"]; exists || memory["agentId"] != "workspace-agent" {
		t.Fatalf("nested compaction mismatch: %#v", memory)
	}
}

func TestReadOverrideFileRejectsUnsupportedSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadOverrideFile(path); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported schema version was accepted: %v", err)
	}
}

func TestReadOverrideFileRejectsNonObjectTrailingUnknownAndDuplicateJSON(t *testing.T) {
	for _, raw := range []string{
		`[]`, `{} {}`,
		`{"workspaceId":"api"}`,
		`{"mcpServers":{"postgres":{"command":"uvx","workspaceId":["api"]}}}`,
		`{"startupMode":"lazy","startupMode":"eager"}`,
		`{"mcpServers":{"postgres":{"command":"uvx","startupMode":"lazy","startupMode":"eager"}}}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadOverrideFile(path); err == nil {
			t.Fatalf("invalid override %q was accepted", raw)
		}
	}
}
