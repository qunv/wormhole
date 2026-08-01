// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codebridge/internal/config"
)

func TestNamedTunnelProfilesExposeSelectedModeAsMain(t *testing.T) {
	cfg := config.Default()
	cfg.ProfileDir = t.TempDir()
	cfg.Port = 9123
	cfg.TunnelID = ""
	cfg.Tunnels = map[string]config.TunnelConfig{
		"fast": {
			TunnelID: "tunnel_fast", Mode: "fast", Profile: "codebridge-fast",
			RuntimeKeyEnv: "FAST_KEY",
		},
		"full": {
			TunnelID: "tunnel_full", Mode: "full", Profile: "codebridge-full",
			RuntimeKeyEnv: "FULL_KEY",
		},
		"review": {
			TunnelID: "tunnel_review", Mode: "full", ToolProfile: "review", Profile: "codebridge-review",
			RuntimeKeyEnv: "REVIEW_KEY",
		},
	}
	paths, err := writeTunnelProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("profile count = %d, want 3", len(paths))
	}
	profiles := map[string]string{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		profiles[filepath.Base(path)] = string(raw)
	}
	fast := profiles["codebridge-fast.yaml"]
	full := profiles["codebridge-full.yaml"]
	review := profiles["codebridge-review.yaml"]
	if !strings.Contains(fast, "channel: main") || !strings.Contains(fast, "/mcp/session/fast") || strings.Contains(fast, "channel: fast") {
		t.Fatalf("fast profile did not expose only fast as main: %s", fast)
	}
	if !strings.Contains(full, "channel: main") || !strings.Contains(full, `9123/mcp/session"`) || strings.Contains(full, "/mcp/session/fast") {
		t.Fatalf("full profile did not expose full as main: %s", full)
	}
	if !strings.Contains(review, "/mcp/session/profiles/review") || strings.Contains(review, "/mcp/session/fast") {
		t.Fatalf("custom profile did not expose its stable session endpoint: %s", review)
	}
	if !strings.Contains(fast, `tunnel_id: "tunnel_fast"`) || !strings.Contains(full, `tunnel_id: "tunnel_full"`) || !strings.Contains(review, `tunnel_id: "tunnel_review"`) {
		t.Fatalf("profile tunnel IDs are incorrect: fast=%s full=%s review=%s", fast, full, review)
	}
}

func TestLegacyTunnelProfileKeepsLogicalChannels(t *testing.T) {
	configureWorkspaceTestPaths(t)
	cfg := config.Default()
	cfg.ProfileDir = t.TempDir()
	cfg.TunnelID = "tunnel_legacy"
	path, err := writeTunnelProfile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "channel: main") || !strings.Contains(text, "channel: fast") {
		t.Fatalf("legacy logical channels were not preserved: %s", text)
	}
}

func TestMigrateTunnelProcessStateAndRoundTripNamedProcesses(t *testing.T) {
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	state := processState{TunnelPID: 22, TunnelIdentity: "legacy"}
	migrateTunnelProcessState(&state)
	if state.TunnelPID != 0 || state.TunnelIdentity != "" || state.Tunnels["default"].PID != 22 {
		t.Fatalf("legacy tunnel state was not migrated: %#v", state)
	}
	state.Tunnels["fast"] = tunnelProcessState{PID: 33, Identity: "fast"}
	state.UpdatedAt = time.Now().UTC()
	if err := writeState(state); err != nil {
		t.Fatal(err)
	}
	got := readState()
	if got.Tunnels["default"].Identity != "legacy" || got.Tunnels["fast"].Identity != "fast" {
		t.Fatalf("named tunnel state was not preserved: %#v", got)
	}
}

func TestStopAllTunnelsPreflightsOwnershipBeforeMutation(t *testing.T) {
	identity, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	state := processState{Tunnels: map[string]tunnelProcessState{
		"dead":    {PID: 99999999, Identity: "stale"},
		"unknown": {PID: os.Getpid(), Identity: identity + "-other"},
	}}
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.stopAllTunnels(&state, false); err == nil {
		t.Fatal("unowned live tunnel was not rejected")
	}
	if len(state.Tunnels) != 2 {
		t.Fatalf("preflight failure partially mutated tunnel state: %#v", state.Tunnels)
	}
}

func TestRuntimeKeyIdentityMaterialIncludesEveryEnabledTunnel(t *testing.T) {
	cfg := config.Default()
	cfg.TunnelID = ""
	cfg.Tunnels = map[string]config.TunnelConfig{
		"fast": {TunnelID: "fast", Mode: "fast", Profile: "fast", RuntimeKeyEnv: "FAST_KEY"},
		"full": {TunnelID: "full", Mode: "full", Profile: "full", RuntimeKeyEnv: "FULL_KEY"},
	}
	t.Setenv("FAST_KEY", "fast-secret")
	t.Setenv("FULL_KEY", "full-secret")
	first := runtimeKeyIdentityMaterial(cfg, "")
	t.Setenv("FULL_KEY", "changed")
	second := runtimeKeyIdentityMaterial(cfg, "")
	if first == second || strings.Contains(first, "FAST_KEY\x00\x00") {
		t.Fatalf("runtime key identity did not include enabled tunnel values")
	}
}

func TestChildLogPathSeparatesNamedTunnels(t *testing.T) {
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	if got := childLogPath("tunnel-fast"); got != config.TunnelLogPathFor("fast") {
		t.Fatalf("fast tunnel log path = %s", got)
	}
	if got := childLogPath("tunnel-full"); got != config.TunnelLogPathFor("full") {
		t.Fatalf("full tunnel log path = %s", got)
	}
	if !validChildLabel("tunnel-fast") || validChildLabel("tunnel-../bad") {
		t.Fatal("named tunnel child label validation is incorrect")
	}
}

func TestKeyCommandSupportsTunnelSpecificEnvironment(t *testing.T) {
	t.Setenv("CODEBRIDGE_HOME", t.TempDir())
	var output bytes.Buffer
	app := App{Stdout: &output, Stderr: &output, Stdin: strings.NewReader("")}
	if err := app.keyCommand(options{Rest: []string{"set", "secret"}, RuntimeEnv: "FAST_KEY"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(config.DotEnvPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "FAST_KEY=secret") {
		t.Fatalf("custom tunnel key was not stored: %s", raw)
	}
	if err := app.keyCommand(options{Rest: []string{"delete"}, RuntimeEnv: "FAST_KEY"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config.DotEnvPath()); !os.IsNotExist(err) {
		t.Fatalf("dotenv should be removed after deleting its only key: %v", err)
	}
}
