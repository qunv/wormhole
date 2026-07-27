package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func enabled(value bool) *bool { return &value }

func validStdioServer(command string) MCPServerConfig {
	return MCPServerConfig{
		Command: command,
		Policy: MCPServerPolicyConfig{
			ReadOnlyTools: []string{"list_tables"},
		},
	}
}

func TestMCPServersAcceptGenericCommandsAndApplyDefaults(t *testing.T) {
	for _, command := range []string{"npx", "uvx", "docker", "/opt/tools/custom-mcp"} {
		cfg := Default()
		cfg.MCPServers["postgres"] = validStdioServer(command)
		normalize(&cfg)
		server := cfg.MCPServers["postgres"]
		if server.EffectiveTransport() != "stdio" {
			t.Fatalf("command %q normalized to transport=%q", command, server.EffectiveTransport())
		}
		if server.StartupTimeoutMS != DefaultMCPStartupTimeoutMS ||
			server.CallTimeoutMS != DefaultMCPCallTimeoutMS ||
			server.HealthCacheMS != DefaultMCPHealthCacheMS ||
			server.FailureCooldownMS != DefaultMCPFailureCooldownMS ||
			server.MaxConcurrency != DefaultMCPMaxConcurrency ||
			server.MaxTools != DefaultMCPMaxTools {
			t.Fatalf("command %q did not receive defaults: %#v", command, server)
		}
		if err := cfg.Validate(false); err != nil {
			t.Fatalf("generic command %q rejected: %v", command, err)
		}
	}
}

func TestMCPServersRejectPersistedSecretsAndSensitiveInheritance(t *testing.T) {
	cfg := Default()
	server := validStdioServer("uvx")
	server.Env = map[string]string{"DATABASE_URI": "postgresql://user:password@localhost/db"}
	cfg.MCPServers["postgres"] = server
	normalize(&cfg)
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "envRefs") {
		t.Fatalf("persisted MCP secret was not rejected: %v", err)
	}

	server.Env = nil
	server.EnvRefs = map[string]string{"DATABASE_URI": "POSTGRES_MCP_DATABASE_URI"}
	server.InheritEnv = []string{"MCP_AUTH_TOKEN"}
	cfg.MCPServers["postgres"] = server
	normalize(&cfg)
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "envRefs") {
		t.Fatalf("sensitive inherited environment was not rejected: %v", err)
	}

	server.InheritEnv = []string{"UV_CACHE_DIR"}
	cfg.MCPServers["postgres"] = server
	normalize(&cfg)
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("secret reference configuration rejected: %v", err)
	}
}

func TestMCPServersValidateTransportSpecificFields(t *testing.T) {
	cfg := Default()
	server := validStdioServer("npx")
	server.URL = "http://127.0.0.1:9000/mcp"
	cfg.MCPServers["filesystem"] = server
	normalize(&cfg)
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "not supported for stdio") {
		t.Fatalf("stdio URL was not rejected: %v", err)
	}

	cfg = Default()
	cfg.MCPServers["remote"] = MCPServerConfig{
		Transport: "streamable-http", URL: "http://127.0.0.1:9000/mcp", CWD: ".",
	}
	normalize(&cfg)
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "not supported for streamable-http") {
		t.Fatalf("HTTP cwd was not rejected: %v", err)
	}
}

func TestMCPServersRejectPolicyAndHeaderCollisions(t *testing.T) {
	cfg := Default()
	server := validStdioServer("uvx")
	server.Policy.ReadOnlyTools = []string{"query"}
	server.Policy.AlwaysApproveTools = []string{"query"}
	cfg.MCPServers["postgres"] = server
	normalize(&cfg)
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "appears in both") {
		t.Fatalf("policy collision was not rejected: %v", err)
	}

	cfg = Default()
	cfg.MCPServers["remote"] = MCPServerConfig{
		Transport: "streamable-http", URL: "http://127.0.0.1:9000/mcp",
		Headers:    map[string]string{"X-Tenant": "demo"},
		HeaderRefs: map[string]string{"x-tenant": "TENANT_HEADER"},
	}
	normalize(&cfg)
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("case-insensitive header collision was not rejected: %v", err)
	}
}

func TestMCPServerConfigIDIncludesSecretFingerprints(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codebridge")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.MCPServers["postgres"] = MCPServerConfig{
		Command: "uvx", EnvRefs: map[string]string{"DATABASE_URI": "POSTGRES_MCP_DATABASE_URI"},
	}
	normalize(&cfg)
	t.Setenv("POSTGRES_MCP_DATABASE_URI", "postgresql://first")
	first := cfg.ConfigID(binary, []byte("widget"))
	t.Setenv("POSTGRES_MCP_DATABASE_URI", "postgresql://second")
	second := cfg.ConfigID(binary, []byte("widget"))
	if first == second {
		t.Fatal("ConfigID did not change when an upstream MCP secret changed")
	}
}

func TestMCPServersBoundConcurrencyCacheAndCooldown(t *testing.T) {
	for field, configure := range map[string]func(*MCPServerConfig){
		"healthCacheMs":     func(server *MCPServerConfig) { server.HealthCacheMS = 60_001 },
		"failureCooldownMs": func(server *MCPServerConfig) { server.FailureCooldownMS = 60_001 },
		"maxConcurrency":    func(server *MCPServerConfig) { server.MaxConcurrency = 129 },
	} {
		cfg := Default()
		server := validStdioServer("uvx")
		configure(&server)
		cfg.MCPServers["bounded"] = server
		normalize(&cfg)
		if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("invalid %s was not rejected: %v", field, err)
		}
	}
}

func TestMCPServerStartupModeNormalizeValidateAndRequireEager(t *testing.T) {
	cfg := Default()
	server := validStdioServer("uvx")
	server.StartupMode = " LAZY "
	cfg.MCPServers["deferred"] = server
	normalize(&cfg)

	normalized := cfg.MCPServers["deferred"]
	if normalized.StartupMode != MCPStartupModeLazy || normalized.EffectiveStartupMode() != MCPStartupModeLazy {
		t.Fatalf("startup mode normalized to %q effective=%q", normalized.StartupMode, normalized.EffectiveStartupMode())
	}
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("valid startup mode rejected: %v", err)
	}

	normalized.Required = true
	if got := normalized.EffectiveStartupMode(); got != MCPStartupModeEager {
		t.Fatalf("required server effective startup mode = %q, want eager", got)
	}

	server.StartupMode = "later"
	server.Required = true
	cfg.MCPServers["deferred"] = server
	normalize(&cfg)
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "startupMode") {
		t.Fatalf("invalid startup mode was not rejected: %v", err)
	}
}

func TestMCPServerWorkspaceIDsNormalizeValidateAndApply(t *testing.T) {
	cfg := Default()
	server := validStdioServer("uvx")
	server.WorkspaceIDs = []string{" API ", "remotee"}
	cfg.MCPServers["scoped"] = server
	normalize(&cfg)

	normalized := cfg.MCPServers["scoped"]
	if got := strings.Join(normalized.WorkspaceIDs, ","); got != "api,remotee" {
		t.Fatalf("workspace IDs normalized to %q", got)
	}
	if !normalized.AppliesToWorkspace("API") || normalized.AppliesToWorkspace("other") {
		t.Fatalf("unexpected workspace scope behavior: %#v", normalized.WorkspaceIDs)
	}
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("valid workspace scope rejected: %v", err)
	}

	server.WorkspaceIDs = []string{"bad/id"}
	cfg.MCPServers["scoped"] = server
	normalize(&cfg)
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "workspaceIds") {
		t.Fatalf("invalid workspace ID was not rejected: %v", err)
	}

	server.WorkspaceIDs = []string{"*"}
	cfg.MCPServers["scoped"] = server
	normalize(&cfg)
	if scoped := cfg.MCPServers["scoped"]; !scoped.AppliesToWorkspace("anything") {
		t.Fatal("workspace wildcard did not preserve global scope")
	}
}

func TestDisabledMCPServerDoesNotRequireCommand(t *testing.T) {
	cfg := Default()
	cfg.MCPServers["optional"] = MCPServerConfig{Enabled: enabled(false)}
	normalize(&cfg)
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("disabled MCP server rejected: %v", err)
	}
}
