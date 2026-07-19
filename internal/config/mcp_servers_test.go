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
		if server.EffectiveTransport() != "stdio" || server.ToolPrefix != "postgres" {
			t.Fatalf("command %q normalized to transport=%q prefix=%q", command, server.EffectiveTransport(), server.ToolPrefix)
		}
		if server.StartupTimeoutMS != DefaultMCPStartupTimeoutMS || server.CallTimeoutMS != DefaultMCPCallTimeoutMS || server.MaxTools != DefaultMCPMaxTools {
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

func TestDisabledMCPServerDoesNotRequireCommand(t *testing.T) {
	cfg := Default()
	cfg.MCPServers["optional"] = MCPServerConfig{Enabled: enabled(false)}
	normalize(&cfg)
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("disabled MCP server rejected: %v", err)
	}
}
