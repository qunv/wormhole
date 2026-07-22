// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

const (
	DefaultMCPStartupTimeoutMS  = 15_000
	DefaultMCPCallTimeoutMS     = 30_000
	DefaultMCPHealthTimeoutMS   = 3_000
	DefaultMCPHealthCacheMS     = 5_000
	DefaultMCPFailureCooldownMS = 1_000
	DefaultMCPMaxConcurrency    = 8
	DefaultMCPMaxTools          = 200
)

type MCPServerConfig struct {
	Enabled *bool `json:"enabled,omitempty"`

	Transport string   `json:"transport,omitempty"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	CWD       string   `json:"cwd,omitempty"`

	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	HeaderRefs  map[string]string `json:"headerRefs,omitempty"`
	AllowRemote bool              `json:"allowRemote,omitempty"`

	Env        map[string]string `json:"env,omitempty"`
	EnvRefs    map[string]string `json:"envRefs,omitempty"`
	InheritEnv []string          `json:"inheritEnv,omitempty"`

	AllowedTools []string `json:"allowedTools,omitempty"`
	DeniedTools  []string `json:"deniedTools,omitempty"`

	Required          bool `json:"required,omitempty"`
	StartupTimeoutMS  int  `json:"startupTimeoutMs,omitempty"`
	CallTimeoutMS     int  `json:"callTimeoutMs,omitempty"`
	HealthTimeoutMS   int  `json:"healthTimeoutMs,omitempty"`
	HealthCacheMS     int  `json:"healthCacheMs,omitempty"`
	FailureCooldownMS int  `json:"failureCooldownMs,omitempty"`
	MaxConcurrency    int  `json:"maxConcurrency,omitempty"`
	MaxTools          int  `json:"maxTools,omitempty"`

	Policy MCPServerPolicyConfig `json:"policy,omitempty"`
}

type MCPServerPolicyConfig struct {
	TrustAnnotations   bool     `json:"trustAnnotations,omitempty"`
	Default            string   `json:"default,omitempty"`
	ReadOnlyTools      []string `json:"readOnlyTools,omitempty"`
	ApprovalTools      []string `json:"approvalTools,omitempty"`
	AlwaysApproveTools []string `json:"alwaysApproveTools,omitempty"`
}

var mcpServerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,23}$`)

func (c MCPServerConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c MCPServerConfig) EffectiveTransport() string {
	if c.Transport != "" {
		return c.Transport
	}
	if c.Command != "" {
		return "stdio"
	}
	if c.URL != "" {
		return "streamable-http"
	}
	return ""
}

func normalizeMCPServers(c *Config) {
	if c.MCPServers == nil {
		c.MCPServers = map[string]MCPServerConfig{}
	}
	for name, server := range c.MCPServers {
		server.Transport = strings.ToLower(strings.TrimSpace(server.Transport))
		server.Command = strings.TrimSpace(server.Command)
		server.CWD = strings.TrimSpace(server.CWD)
		server.URL = strings.TrimSpace(server.URL)
		if server.StartupTimeoutMS == 0 {
			server.StartupTimeoutMS = DefaultMCPStartupTimeoutMS
		}
		if server.CallTimeoutMS == 0 {
			server.CallTimeoutMS = DefaultMCPCallTimeoutMS
		}
		if server.HealthTimeoutMS == 0 {
			server.HealthTimeoutMS = DefaultMCPHealthTimeoutMS
		}
		if server.HealthCacheMS == 0 {
			server.HealthCacheMS = DefaultMCPHealthCacheMS
		}
		if server.FailureCooldownMS == 0 {
			server.FailureCooldownMS = DefaultMCPFailureCooldownMS
		}
		if server.MaxConcurrency == 0 {
			server.MaxConcurrency = DefaultMCPMaxConcurrency
		}
		if server.MaxTools == 0 {
			server.MaxTools = DefaultMCPMaxTools
		}
		server.Policy.Default = strings.ToLower(strings.TrimSpace(server.Policy.Default))
		if server.Policy.Default == "" {
			server.Policy.Default = "approval"
		}
		server.AllowedTools = cleanStringList(server.AllowedTools)
		server.DeniedTools = cleanStringList(server.DeniedTools)
		server.Policy.ReadOnlyTools = cleanStringList(server.Policy.ReadOnlyTools)
		server.Policy.ApprovalTools = cleanStringList(server.Policy.ApprovalTools)
		server.Policy.AlwaysApproveTools = cleanStringList(server.Policy.AlwaysApproveTools)
		server.InheritEnv = cleanStringList(server.InheritEnv)
		c.MCPServers[name] = server
	}
}

func validateMCPServers(c Config) error {
	for name, server := range c.MCPServers {
		if !mcpServerNamePattern.MatchString(name) {
			return fmt.Errorf("mcpServers name %q must match %s", name, mcpServerNamePattern.String())
		}
		if !server.IsEnabled() {
			continue
		}
		transport := server.EffectiveTransport()
		switch transport {
		case "stdio":
			if server.Command == "" {
				return fmt.Errorf("mcpServers.%s.command is required for stdio transport", name)
			}
			if server.URL != "" || len(server.Headers) > 0 || len(server.HeaderRefs) > 0 || server.AllowRemote {
				return fmt.Errorf("mcpServers.%s.url, headers, headerRefs, and allowRemote are not supported for stdio transport", name)
			}
		case "streamable-http":
			if server.URL == "" {
				return fmt.Errorf("mcpServers.%s.url is required for streamable-http transport", name)
			}
			if server.Command != "" || len(server.Args) > 0 || server.CWD != "" || len(server.Env) > 0 || len(server.EnvRefs) > 0 || len(server.InheritEnv) > 0 {
				return fmt.Errorf("mcpServers.%s.command, args, cwd, env, envRefs, and inheritEnv are not supported for streamable-http transport", name)
			}
			parsed, err := url.Parse(server.URL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
				return fmt.Errorf("mcpServers.%s.url must be an absolute http or https URL", name)
			}
			if parsed.User != nil {
				return fmt.Errorf("mcpServers.%s.url must not contain credentials", name)
			}
		default:
			return fmt.Errorf("mcpServers.%s.transport must be stdio or streamable-http", name)
		}
		if err := validateMCPServerValues(name, server); err != nil {
			return err
		}
		for _, limit := range []struct {
			name  string
			value int
			max   int
		}{
			{"startupTimeoutMs", server.StartupTimeoutMS, 10 * 60_000},
			{"callTimeoutMs", server.CallTimeoutMS, 60 * 60_000},
			{"healthTimeoutMs", server.HealthTimeoutMS, 60_000},
			{"healthCacheMs", server.HealthCacheMS, 60_000},
			{"failureCooldownMs", server.FailureCooldownMS, 60_000},
			{"maxConcurrency", server.MaxConcurrency, 128},
			{"maxTools", server.MaxTools, 1_000},
		} {
			if limit.value <= 0 || limit.value > limit.max {
				return fmt.Errorf("mcpServers.%s.%s must be between 1 and %d", name, limit.name, limit.max)
			}
		}
		for key := range server.Env {
			if !envNamePattern.MatchString(key) {
				return fmt.Errorf("mcpServers.%s.env key %q must be an environment variable name", name, key)
			}
			if sensitiveConfigKey(key) {
				return fmt.Errorf("mcpServers.%s.env.%s must use envRefs instead of storing a sensitive value in config.json", name, key)
			}
		}
		for target, source := range server.EnvRefs {
			if !envNamePattern.MatchString(target) || !envNamePattern.MatchString(source) {
				return fmt.Errorf("mcpServers.%s.envRefs must map environment variable names to environment variable names", name)
			}
		}
		for _, inherited := range server.InheritEnv {
			if !envNamePattern.MatchString(inherited) {
				return fmt.Errorf("mcpServers.%s.inheritEnv value %q is invalid", name, inherited)
			}
		}
		for header := range server.Headers {
			if !validHTTPHeaderName(header) {
				return fmt.Errorf("mcpServers.%s.headers key %q is invalid", name, header)
			}
			if sensitiveConfigKey(header) {
				return fmt.Errorf("mcpServers.%s.headers.%s must use headerRefs instead of storing a sensitive value in config.json", name, header)
			}
		}
		for header, source := range server.HeaderRefs {
			if !validHTTPHeaderName(header) || !envNamePattern.MatchString(source) {
				return fmt.Errorf("mcpServers.%s.headerRefs must map valid HTTP header names to environment variable names", name)
			}
		}
		if err := validateMCPToolLists(name, server); err != nil {
			return err
		}
	}
	return nil
}

func validateMCPServerValues(name string, server MCPServerConfig) error {
	if len(server.Command) > 4_096 || strings.IndexByte(server.Command, 0) >= 0 {
		return fmt.Errorf("mcpServers.%s.command is too long or contains a NUL byte", name)
	}
	if len(server.Args) > 256 {
		return fmt.Errorf("mcpServers.%s.args is limited to 256 entries", name)
	}
	for index, value := range server.Args {
		if len(value) > 32<<10 || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("mcpServers.%s.args[%d] is too long or contains a NUL byte", name, index)
		}
	}
	if len(server.CWD) > 4_096 || strings.IndexByte(server.CWD, 0) >= 0 {
		return fmt.Errorf("mcpServers.%s.cwd is too long or contains a NUL byte", name)
	}
	if len(server.URL) > 8_192 || strings.ContainsAny(server.URL, "\r\n\x00") {
		return fmt.Errorf("mcpServers.%s.url is too long or contains an invalid control byte", name)
	}
	if len(server.Env)+len(server.EnvRefs)+len(server.InheritEnv) > 256 {
		return fmt.Errorf("mcpServers.%s environment configuration is limited to 256 entries", name)
	}
	for key, value := range server.Env {
		if len(value) > 64<<10 || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("mcpServers.%s.env.%s is too long or contains a NUL byte", name, key)
		}
		if _, exists := server.EnvRefs[key]; exists {
			return fmt.Errorf("mcpServers.%s environment variable %q appears in both env and envRefs", name, key)
		}
	}
	for _, inherited := range server.InheritEnv {
		if sensitiveConfigKey(inherited) {
			return fmt.Errorf("mcpServers.%s.inheritEnv value %q is sensitive; pass it explicitly through envRefs", name, inherited)
		}
	}
	if len(server.Headers)+len(server.HeaderRefs) > 128 {
		return fmt.Errorf("mcpServers.%s HTTP header configuration is limited to 128 entries", name)
	}
	headerOwners := map[string]string{}
	for header, value := range server.Headers {
		if len(value) > 64<<10 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("mcpServers.%s.headers.%s is too long or contains an invalid control byte", name, header)
		}
		canonical := strings.ToLower(header)
		if previous := headerOwners[canonical]; previous != "" {
			return fmt.Errorf("mcpServers.%s HTTP header %q conflicts with %s", name, header, previous)
		}
		headerOwners[canonical] = "headers." + header
	}
	for header := range server.HeaderRefs {
		canonical := strings.ToLower(header)
		if previous := headerOwners[canonical]; previous != "" {
			return fmt.Errorf("mcpServers.%s HTTP header %q conflicts with %s", name, header, previous)
		}
		headerOwners[canonical] = "headerRefs." + header
	}
	for _, values := range [][]string{
		server.AllowedTools, server.DeniedTools, server.Policy.ReadOnlyTools,
		server.Policy.ApprovalTools, server.Policy.AlwaysApproveTools,
	} {
		if len(values) > 1_000 {
			return fmt.Errorf("mcpServers.%s tool lists are limited to 1000 entries", name)
		}
		for _, value := range values {
			if len(value) > 512 || strings.IndexByte(value, 0) >= 0 {
				return fmt.Errorf("mcpServers.%s contains an invalid or oversized tool name", name)
			}
		}
	}
	return nil
}

func validateMCPToolLists(name string, server MCPServerConfig) error {
	lists := map[string][]string{
		"allowedTools":              server.AllowedTools,
		"deniedTools":               server.DeniedTools,
		"policy.readOnlyTools":      server.Policy.ReadOnlyTools,
		"policy.approvalTools":      server.Policy.ApprovalTools,
		"policy.alwaysApproveTools": server.Policy.AlwaysApproveTools,
	}
	membership := map[string]string{}
	for listName, values := range lists {
		seen := map[string]bool{}
		for _, value := range values {
			if value == "" {
				return fmt.Errorf("mcpServers.%s.%s must not contain empty tool names", name, listName)
			}
			if seen[value] {
				return fmt.Errorf("mcpServers.%s.%s contains duplicate tool %q", name, listName, value)
			}
			seen[value] = true
			if strings.HasPrefix(listName, "policy.") {
				if previous := membership[value]; previous != "" {
					return fmt.Errorf("mcpServers.%s tool %q appears in both %s and %s", name, value, previous, listName)
				}
				membership[value] = listName
			}
		}
	}
	for _, value := range server.DeniedTools {
		if containsString(server.AllowedTools, value) {
			return fmt.Errorf("mcpServers.%s tool %q appears in both allowedTools and deniedTools", name, value)
		}
	}
	switch server.Policy.Default {
	case "read-only", "approval", "always-approval", "deny":
		return nil
	default:
		return fmt.Errorf("mcpServers.%s.policy.default must be read-only, approval, always-approval, or deny", name)
	}
}

func MCPServerSecretFingerprints(servers map[string]MCPServerConfig) map[string]map[string]string {
	result := map[string]map[string]string{}
	for name, server := range servers {
		if !server.IsEnabled() {
			continue
		}
		values := map[string]string{}
		for target, source := range server.EnvRefs {
			if value := os.Getenv(source); value != "" {
				sum := sha256.Sum256([]byte(value))
				values["env:"+target] = hex.EncodeToString(sum[:8])
			}
		}
		for header, source := range server.HeaderRefs {
			if value := os.Getenv(source); value != "" {
				sum := sha256.Sum256([]byte(value))
				values["header:"+strings.ToLower(header)] = hex.EncodeToString(sum[:8])
			}
		}
		if len(values) > 0 {
			result[name] = values
		}
	}
	return result
}

func sensitiveConfigKey(value string) bool {
	normalized := strings.ToLower(value)
	normalized = strings.NewReplacer("_", "", "-", "", ".", "").Replace(normalized)
	for _, marker := range []string{
		"secret", "password", "passwd", "token", "apikey", "authorization", "auth",
		"credential", "cookie", "session", "databaseuri", "databaseurl", "dsn",
		"connectionstring", "privatekey",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func validHTTPHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			continue
		}
		switch char {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		}
		return false
	}
	return true
}

func cleanStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func SortedMCPServerNames(servers map[string]MCPServerConfig) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
