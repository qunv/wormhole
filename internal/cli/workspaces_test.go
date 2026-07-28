package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/workspaceregistry"
)

func TestWorkspaceAddCreatesSharedDaemonEndpoint(t *testing.T) {
	configureWorkspaceTestPaths(t)
	root := t.TempDir()
	defaultConfig := config.Default()
	defaultConfig.Workspace = t.TempDir()
	defaultConfig.Port = 8789
	defaultConfig.TunnelID = "daemon-tunnel"
	defaultConfig.NoTunnel = false

	var output bytes.Buffer
	app := App{Stdout: &output, Stderr: &output, Stdin: strings.NewReader("")}
	extraRoot := t.TempDir()
	opts := options{
		Rest: []string{"add", "api", root}, Port: 9999, TunnelID: "ignored",
		Mode: "full", Policy: "full", ExtraRoots: []string{extraRoot}, NoTunnel: true, Profile: "ignored",
	}
	if err := app.workspaceAdd(defaultConfig, opts); err != nil {
		t.Fatal(err)
	}

	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry, exists := registry.Workspaces["api"]
	if !exists || !entry.Enabled {
		t.Fatalf("workspace was not registered and enabled: %#v", registry)
	}
	if entry.Workspace != root || entry.Port != defaultConfig.Port {
		t.Fatalf("unexpected registration: %#v", entry)
	}
	if !strings.Contains(output.String(), "/mcp/workspaces/api") {
		t.Fatalf("workspace endpoint missing from output: %s", output.String())
	}
	if !strings.Contains(output.String(), "--port 9999 ignored") || !strings.Contains(output.String(), "--tunnel-id ignored") {
		t.Fatalf("phase-one compatibility warning missing: %s", output.String())
	}

	override, err := config.ReadOverrideFile(entry.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, inherited := range []string{"workspace", "port", "host", "noTunnel", "tunnelId"} {
		if _, exists := override[inherited]; exists {
			t.Fatalf("daemon-owned field %q was duplicated in workspace override: %#v", inherited, override)
		}
	}
	if override["mode"] != "full" || override["policy"] != "full" {
		t.Fatalf("runtime options were not saved as overrides: %#v", override)
	}
	items, err := loadNamedWorkspaceConfigs(defaultConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("named configs = %d, want 1", len(items))
	}
	instanceConfig := items[0].Config
	if instanceConfig.Workspace != root || instanceConfig.Port != defaultConfig.Port {
		t.Fatalf("unexpected effective config: workspace=%q port=%d", instanceConfig.Workspace, instanceConfig.Port)
	}
	if !instanceConfig.NoTunnel || instanceConfig.TunnelID != "" {
		t.Fatalf("named workspace owns tunnel settings: noTunnel=%v tunnelID=%q", instanceConfig.NoTunnel, instanceConfig.TunnelID)
	}
	if instanceConfig.Mode != "full" || instanceConfig.Policy != "full" {
		t.Fatalf("runtime options were not applied: mode=%q policy=%q", instanceConfig.Mode, instanceConfig.Policy)
	}
	if len(instanceConfig.ExtraRoots) != 1 || instanceConfig.ExtraRoots[0] != extraRoot {
		t.Fatalf("extra roots were not applied: %#v", instanceConfig.ExtraRoots)
	}
	for _, warning := range []string{"--no-tunnel ignored", "--profile ignored"} {
		if !strings.Contains(output.String(), warning) {
			t.Fatalf("missing compatibility warning %q: %s", warning, output.String())
		}
	}
}

func TestWorkspaceStopDisablesEndpointWithoutStartingDaemon(t *testing.T) {
	configureWorkspaceTestPaths(t)
	defaultConfig := config.Default()
	defaultConfig.Workspace = t.TempDir()
	defaultConfig.Port = reserveAvailablePort(t)
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(defaultConfig, options{Rest: []string{"add", "api", t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceLifecycle(t.Context(), defaultConfig, "stop", "api", options{}); err != nil {
		t.Fatal(err)
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	if registry.Workspaces["api"].Enabled {
		t.Fatal("workspace stop did not disable the endpoint")
	}
}

func TestLoadNamedWorkspaceConfigsUsesRegistryIdentity(t *testing.T) {
	configureWorkspaceTestPaths(t)
	defaultConfig := config.Default()
	defaultConfig.Workspace = t.TempDir()
	defaultConfig.Port = 8789
	root := t.TempDir()
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(defaultConfig, options{Rest: []string{"add", "api", root}}); err != nil {
		t.Fatal(err)
	}

	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Workspaces["api"]
	override, err := config.ReadOverrideFile(entry.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	override["workspace"] = t.TempDir()
	override["port"] = 9999
	if err := config.SaveOverrideFile(entry.ConfigPath, defaultConfig, override); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_WORKSPACE", t.TempDir())
	t.Setenv("PORT", "7777")

	items, err := loadNamedWorkspaceConfigs(defaultConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("named configs = %d, want 1", len(items))
	}
	if items[0].Config.Workspace != root || items[0].Config.Port != defaultConfig.Port {
		t.Fatalf("registry identity was not authoritative: %#v", items[0])
	}
}

func TestNamedWorkspaceInheritsLaterGlobalChanges(t *testing.T) {
	configureWorkspaceTestPaths(t)
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.MCPServers["postgres"] = config.MCPServerConfig{Command: "uvx", StartupMode: config.MCPStartupModeEager}
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(cfg, options{Rest: []string{"add", "api", t.TempDir()}}); err != nil {
		t.Fatal(err)
	}

	cfg.MCPServers["postgres"] = config.MCPServerConfig{Command: "uvx", StartupMode: config.MCPStartupModeLazy}
	items, err := loadNamedWorkspaceConfigs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("named configs = %d, want 1", len(items))
	}
	server := items[0].Config.MCPServers["postgres"]
	if server.StartupMode != config.MCPStartupModeLazy {
		t.Fatalf("named workspace retained stale global MCP config: %#v", server)
	}
}

func TestDaemonConfigIDChangesWithWorkspaceRegistry(t *testing.T) {
	configureWorkspaceTestPaths(t)
	binary := filepath.Join(t.TempDir(), "codebridge")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	base, err := daemonConfigID(cfg, binary, []byte("widget"))
	if err != nil {
		t.Fatal(err)
	}
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(cfg, options{Rest: []string{"add", "api", t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	changed, err := daemonConfigID(cfg, binary, []byte("widget"))
	if err != nil {
		t.Fatal(err)
	}
	if changed == base {
		t.Fatal("daemon config ID did not change after workspace registration")
	}
}

func TestDaemonConfigIDChangesWithNamedWorkspaceSecret(t *testing.T) {
	configureWorkspaceTestPaths(t)
	binary := filepath.Join(t.TempDir(), "codebridge")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.Memory.SecretEnv = "DEFAULT_MEMORY_SECRET"
	t.Setenv("DEFAULT_MEMORY_SECRET", "stable")
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(cfg, options{Rest: []string{"add", "api", t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Workspaces["api"]
	override, err := config.ReadOverrideFile(entry.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	override["memory"] = map[string]any{"secretEnv": "API_MEMORY_SECRET"}
	if err := config.SaveOverrideFile(entry.ConfigPath, cfg, override); err != nil {
		t.Fatal(err)
	}

	t.Setenv("API_MEMORY_SECRET", "first")
	first, err := daemonConfigID(cfg, binary, []byte("widget"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("API_MEMORY_SECRET", "second")
	second, err := daemonConfigID(cfg, binary, []byte("widget"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("daemon config ID did not change with named workspace secret")
	}
}

func TestServeConfigIDUsesSupervisorValueWithoutRecomputing(t *testing.T) {
	configureWorkspaceTestPaths(t)
	t.Setenv("CODEBRIDGE_DAEMON_CONFIG_ID", "supervisor-config-id")
	if err := os.MkdirAll(filepath.Dir(workspaceregistry.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceregistry.Path(), []byte("invalid registry"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := serveConfigID(config.Default(), config.IdentityInputs{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "supervisor-config-id" {
		t.Fatalf("serve config ID = %q, want supervisor value", got)
	}
}

func TestDaemonConfigIDChangesWithRuntimeKeyFingerprint(t *testing.T) {
	configureWorkspaceTestPaths(t)
	binary := filepath.Join(t.TempDir(), "codebridge")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	first, err := daemonConfigIDWithInputs(cfg, config.NewIdentityInputs(binary, []byte("widget"), "first-runtime-key"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := daemonConfigIDWithInputs(cfg, config.NewIdentityInputs(binary, []byte("widget"), "second-runtime-key"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("daemon config ID did not change with Runtime API key")
	}
}

func TestStartupWaitTimeoutIncludesNamedWorkspaceDependencies(t *testing.T) {
	configureWorkspaceTestPaths(t)
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	primaryID := workspaceregistry.IDFromPath(cfg.Workspace)
	cfg.MCPServers["primary"] = config.MCPServerConfig{
		Transport: "streamable-http", URL: "http://127.0.0.1:9000/mcp",
		StartupTimeoutMS: 4_000, WorkspaceIDs: []string{primaryID},
	}
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(cfg, options{Rest: []string{"add", "api", t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Workspaces["api"]
	override, err := config.ReadOverrideFile(entry.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	override["memory"] = map[string]any{
		"enabled": true, "provider": "agentmemory", "required": true, "timeoutMs": 2_000,
	}
	override["mcpServers"] = map[string]any{
		"slow": map[string]any{"command": "mcp-slow", "startupTimeoutMs": 3_000},
	}
	if err := config.SaveOverrideFile(entry.ConfigPath, cfg, override); err != nil {
		t.Fatal(err)
	}

	if got, want := startupWaitTimeout(cfg), 30*time.Second; got != want {
		t.Fatalf("startupWaitTimeout() = %s, want %s", got, want)
	}
}

func TestTunnelProfileIncludesEnabledWorkspaceChannels(t *testing.T) {
	configureWorkspaceTestPaths(t)
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.Port = reserveAvailablePort(t)
	cfg.TunnelID = "tunnel"
	cfg.ProfileDir = t.TempDir()
	cfg.Profile = "multi"
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(cfg, options{Rest: []string{"add", "api", t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceAdd(cfg, options{Rest: []string{"add", "web", t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceLifecycle(t.Context(), cfg, "stop", "web", options{}); err != nil {
		t.Fatal(err)
	}

	path, err := writeTunnelProfile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"channel: main", "/mcp/session", "channel: fast", "/mcp/session/fast",
		"channel: workspace-api", "/mcp/workspaces/api", "channel: workspace-api-fast", "/mcp/workspaces/api/fast",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("profile missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "channel: session") {
		t.Fatalf("profile unexpectedly contains duplicate session channel: %s", text)
	}
	if strings.Contains(text, "workspace-web") {
		t.Fatalf("disabled workspace leaked into profile: %s", text)
	}
}

func TestWorkspaceCompactSupportsDryRunAndPersistsMinimalOverride(t *testing.T) {
	configureWorkspaceTestPaths(t)
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.AuthToken = strings.Repeat("t", 16)
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(cfg, options{Rest: []string{"add", "api", t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Workspaces["api"]
	if err := saveWorkspaceOverride(entry.ConfigPath, cfg, map[string]any{
		"mode": cfg.Mode, "policy": "strict", "extraRoots": []any{}, "host": "0.0.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app.Stdout = &output
	if err := app.workspaceCompact(cfg, "api", options{DryRun: true, JSON: true}); err != nil {
		t.Fatal(err)
	}
	beforeDryRun, err := config.ReadOverrideFile(entry.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := beforeDryRun["mode"]; !exists {
		t.Fatal("dry-run mutated the override")
	}
	if err := app.workspaceCompact(cfg, "api", options{}); err != nil {
		t.Fatal(err)
	}
	compacted, err := config.ReadOverrideFile(entry.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := compacted["mode"]; exists {
		t.Fatalf("equal mode snapshot remained: %#v", compacted)
	}
	if _, exists := compacted["host"]; exists {
		t.Fatalf("daemon-owned host remained: %#v", compacted)
	}
	if compacted["policy"] != "strict" || compacted["extraRoots"] == nil {
		t.Fatalf("meaningful overrides were lost: %#v", compacted)
	}
}

func TestWorkspaceCompactVersionsUnchangedLegacyOverride(t *testing.T) {
	configureWorkspaceTestPaths(t)
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(cfg, options{Rest: []string{"add", "api", t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Workspaces["api"]
	if err := os.WriteFile(entry.ConfigPath, []byte("{\n  \"extraRoots\": []\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceCompact(cfg, "api", options{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(entry.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"schemaVersion": 1`) {
		t.Fatalf("unchanged legacy override was not versioned: %s", raw)
	}
}

func TestWorkspaceRemoveForceDeletesConfigButKeepsState(t *testing.T) {
	configureWorkspaceTestPaths(t)
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(cfg, options{Rest: []string{"add", "api", t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Workspaces["api"]
	if err := os.MkdirAll(entry.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stateMarker := filepath.Join(entry.DataDir, "keep.txt")
	if err := os.WriteFile(stateMarker, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := app.workspaceRemove(cfg, "api", options{Force: true}); err != nil {
		t.Fatal(err)
	}
	registry, err = workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := registry.Workspaces["api"]; exists {
		t.Fatal("workspace registration still exists")
	}
	if _, err := os.Stat(entry.ConfigPath); !os.IsNotExist(err) {
		t.Fatalf("workspace config still exists: %v", err)
	}
	if raw, err := os.ReadFile(stateMarker); err != nil || string(raw) != "state" {
		t.Fatalf("workspace state was removed: raw=%q err=%v", raw, err)
	}
}

func TestWorkspaceRegistrationRollsBackOverrideWhenRegistrySaveFails(t *testing.T) {
	configureWorkspaceTestPaths(t)
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	originalSave := saveWorkspaceRegistry
	saveWorkspaceRegistry = func(workspaceregistry.Registry) error { return errors.New("registry unavailable") }
	t.Cleanup(func() { saveWorkspaceRegistry = originalSave })

	_, _, err := app.registerWorkspace(cfg, "api", t.TempDir(), options{}, false)
	if err == nil || !strings.Contains(err.Error(), "registry unavailable") {
		t.Fatalf("registry failure was not returned: %v", err)
	}
	if _, err := os.Stat(workspaceregistry.ConfigPath("api")); !os.IsNotExist(err) {
		t.Fatalf("new override remained after registry rollback: %v", err)
	}
}

func TestWorkspaceRemoveRestoresConfigWhenRegistrySaveFails(t *testing.T) {
	configureWorkspaceTestPaths(t)
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(cfg, options{Rest: []string{"add", "api", t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Workspaces["api"]
	before, err := os.ReadFile(entry.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}

	originalSave := saveWorkspaceRegistry
	saveWorkspaceRegistry = func(workspaceregistry.Registry) error { return errors.New("registry unavailable") }
	t.Cleanup(func() { saveWorkspaceRegistry = originalSave })
	if err := app.workspaceRemove(cfg, "api", options{Force: true}); err == nil {
		t.Fatal("expected registry save failure")
	}
	after, err := os.ReadFile(entry.ConfigPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("workspace config was not restored: before=%q after=%q err=%v", before, after, err)
	}
}

func TestWorkspaceLifecycleRejectsUnknownActionWithoutMutation(t *testing.T) {
	configureWorkspaceTestPaths(t)
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(cfg, options{Rest: []string{"add", "api", t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceLifecycle(t.Context(), cfg, "invalid", "api", options{}); err == nil {
		t.Fatal("unknown action was accepted")
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !registry.Workspaces["api"].Enabled {
		t.Fatal("unknown action mutated workspace state")
	}
}

func TestWorkspaceHealthMap(t *testing.T) {
	health := map[string]any{"workspaces": []any{
		map[string]any{"id": "default", "endpoint": "/mcp"},
		map[string]any{"id": "api", "endpoint": "/mcp/workspaces/api"},
	}}
	mapped := workspaceHealthMap(health)
	if len(mapped) != 2 || mapped["api"]["endpoint"] != "/mcp/workspaces/api" {
		raw, _ := json.Marshal(mapped)
		t.Fatalf("unexpected workspace health map: %s", raw)
	}
}

func configureWorkspaceTestPaths(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("CODEBRIDGE_HOME", base)
	switch runtime.GOOS {
	case "windows":
		t.Setenv("APPDATA", filepath.Join(base, "config"))
	case "darwin":
		t.Setenv("HOME", base)
	default:
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	}
	t.Setenv("CODEBRIDGE_DATA_DIR", filepath.Join(base, "data"))
	t.Setenv("CODEBRIDGE_CONFIG_PATH", filepath.Join(base, "default", "config.json"))
	t.Setenv("CODEBRIDGE_WORKSPACE_REGISTRY_PATH", filepath.Join(base, "registry", "workspaces.json"))
}

func reserveAvailablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
