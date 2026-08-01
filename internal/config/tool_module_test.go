package config

import (
	"strings"
	"testing"
)

func TestToolExposureAcceptsExternalModuleNames(t *testing.T) {
	cfg := Default()
	cfg.Tools.AllowedGroups = []string{"database", "kubernetes", "cloud_aws"}
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("valid external module names rejected: %v", err)
	}
}

func TestToolExposureRejectsInvalidModuleNames(t *testing.T) {
	cfg := Default()
	cfg.Tools.AllowedGroups = []string{"custom/module"}
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "valid module name") {
		t.Fatalf("invalid module name error = %v", err)
	}
}

func TestCustomToolProfilesNormalizeAndValidate(t *testing.T) {
	cfg := Default()
	cfg.ToolProfiles = map[string]ToolProfileConfig{
		" Review ": {
			Name: " Code Review ", AllowedGroups: []string{"Repo", "repo"},
			AllowedTools: []string{"read_file", "read_file"}, DeniedTools: []string{"git"},
			OutputMode: "STRUCTURED", CompactDefaults: true,
		},
	}
	prepared, err := Prepare(cfg)
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := prepared.ToolProfiles["review"]
	if !ok || profile.Name != "Code Review" || profile.OutputMode != "structured" || len(profile.AllowedGroups) != 1 || profile.AllowedGroups[0] != "repo" || len(profile.AllowedTools) != 1 {
		t.Fatalf("custom profile was not normalized: %#v", prepared.ToolProfiles)
	}
	cfg = Default()
	cfg.ToolProfiles = map[string]ToolProfileConfig{"fast": {OutputMode: "both"}}
	if _, err := Prepare(cfg); err == nil {
		t.Fatal("reserved custom profile ID was accepted")
	}
	cfg = Default()
	cfg.ToolProfiles = map[string]ToolProfileConfig{"review": {OutputMode: "invalid"}}
	if _, err := Prepare(cfg); err == nil || !strings.Contains(err.Error(), "outputMode") {
		t.Fatalf("invalid profile output mode error = %v", err)
	}
}

func TestTunnelToolProfileReferencesCustomProfile(t *testing.T) {
	cfg := Default()
	cfg.ToolProfiles = map[string]ToolProfileConfig{"review": {OutputMode: "both"}}
	cfg.Tunnels = map[string]TunnelConfig{
		"review": {TunnelID: "tunnel_review", Mode: "full", ToolProfile: "review", Profile: "codebridge-review"},
	}
	prepared, err := Prepare(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.Tunnels["review"].EffectiveToolProfile(); got != "review" {
		t.Fatalf("effective tunnel tool profile = %q", got)
	}
	cfg.Tunnels["review"] = TunnelConfig{TunnelID: "tunnel_review", Mode: "full", ToolProfile: "missing"}
	if _, err := Prepare(cfg); err == nil || !strings.Contains(err.Error(), "unknown tool profile") {
		t.Fatalf("unknown tunnel tool profile error = %v", err)
	}
}

func TestToolExposureAcceptsExactToolsAndRejectsEmptyNames(t *testing.T) {
	cfg := Default()
	cfg.Tools.AllowedTools = []string{"read_file", "apply_patch"}
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("valid exact tool names rejected: %v", err)
	}
	cfg.Tools.AllowedTools = []string{"read_file", " "}
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "allowedTools") {
		t.Fatalf("empty exact tool name error = %v", err)
	}
}
