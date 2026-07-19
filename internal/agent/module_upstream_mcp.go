// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"codebridge/internal/config"
	"codebridge/internal/upstreammcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxUpstreamToolNameBytes        = 512
	maxUpstreamToolTitleBytes       = 512
	maxUpstreamToolDescriptionBytes = 16 << 10
	maxUpstreamToolSchemaBytes      = 256 << 10
	maxUpstreamTotalSchemaBytes     = 4 << 20
)

func (r *Runtime) registerConfiguredUpstreamMCP(ctx context.Context, reporter StartupReporter) error {
	for _, serverName := range config.SortedMCPServerNames(r.Config.MCPServers) {
		serverConfig := r.Config.MCPServers[serverName]
		if !serverConfig.IsEnabled() {
			continue
		}
		startedAt := time.Now()
		reportStartup(reporter, "mcp", fmt.Sprintf(
			"connecting %s transport=%s required=%t timeout=%s",
			serverName,
			serverConfig.EffectiveTransport(),
			serverConfig.Required,
			time.Duration(serverConfig.StartupTimeoutMS)*time.Millisecond,
		))
		module, err := newUpstreamMCPModule(ctx, r, serverName, serverConfig)
		if err != nil {
			if serverConfig.Required {
				reportStartup(reporter, "mcp", fmt.Sprintf("%s failed after %s: %s", serverName, time.Since(startedAt).Round(time.Millisecond), err))
				return err
			}
			warning := fmt.Sprintf("optional upstream MCP server %q was skipped: %s", serverName, err)
			r.addStartupWarning(warning)
			reportStartup(reporter, "warning", warning)
			continue
		}
		if err := r.RegisterModule(module); err != nil {
			_ = module.Close()
			return fmt.Errorf("register upstream MCP server %q: %w", serverName, err)
		}
		reportStartup(reporter, "mcp", fmt.Sprintf(
			"connected %s tools=%d in %s",
			serverName,
			len(module.Specs()),
			time.Since(startedAt).Round(time.Millisecond),
		))
	}
	return nil
}

type upstreamMCPModule struct {
	name         string
	serverName   string
	client       *upstreammcp.Client
	specs        []ToolSpec
	upstream     map[string]string
	readOnly     map[string]bool
	policy       map[string]string
	declaredArgs map[string]map[string]bool
}

func newUpstreamMCPModule(ctx context.Context, runtime *Runtime, serverName string, cfg config.MCPServerConfig) (ToolModule, error) {
	cwd := runtime.Workspace.Primary
	if cfg.EffectiveTransport() == "stdio" {
		path := cfg.CWD
		if path == "" {
			path = "."
		}
		resolved, err := runtime.Workspace.Resolve(path)
		if err != nil {
			return nil, fmt.Errorf("resolve mcpServers.%s.cwd: %w", serverName, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("stat mcpServers.%s.cwd: %w", serverName, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("mcpServers.%s.cwd must be a directory", serverName)
		}
		cwd = resolved
	}
	client, err := upstreammcp.New(ctx, serverName, cfg, runtime.Version, cwd)
	if err != nil {
		return nil, fmt.Errorf("connect upstream MCP server %q: %w", serverName, err)
	}
	module := &upstreamMCPModule{
		name: "mcp_" + serverName, serverName: serverName, client: client,
		upstream: map[string]string{}, readOnly: map[string]bool{}, policy: map[string]string{},
		declaredArgs: map[string]map[string]bool{},
	}
	if err := module.buildSpecs(cfg, client.Tools()); err != nil {
		_ = client.Close()
		return nil, err
	}
	return module, nil
}

func (m *upstreamMCPModule) Name() string      { return m.name }
func (m *upstreamMCPModule) Specs() []ToolSpec { return append([]ToolSpec(nil), m.specs...) }

func (m *upstreamMCPModule) Handle(ctx context.Context, _ CallIdentity, tool string, args map[string]any) (any, error) {
	upstreamTool, ok := m.upstream[tool]
	if !ok {
		return nil, fmt.Errorf("unsupported upstream MCP tool: %s", tool)
	}
	return m.client.Call(ctx, upstreamTool, args, m.readOnly[tool])
}

func (m *upstreamMCPModule) Health(ctx context.Context) any {
	status := m.client.Health(ctx)
	status["module"] = m.name
	status["registered_tools"] = len(m.specs)
	return status
}

func (m *upstreamMCPModule) Close() error { return m.client.Close() }

func (m *upstreamMCPModule) ToolPolicy(tool string, args map[string]any) ToolCallPolicy {
	mode := m.policy[tool]
	if mode == "read-only" || mode == "" {
		return ToolCallPolicy{}
	}
	action := genericToolApprovalAction(tool, args)
	return ToolCallPolicy{
		ApprovalAction: action, RequiresApproval: true,
		AlwaysRequireApproval: mode == "always-approval",
	}
}

func (m *upstreamMCPModule) AuditArguments(tool string, args map[string]any) any {
	declared := m.declaredArgs[tool]
	keys := make([]string, 0, min(len(args), 128))
	undeclared := 0
	for key := range args {
		if !declared[key] {
			undeclared++
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	truncated := false
	if len(keys) > 128 {
		keys = keys[:128]
		truncated = true
	}
	return map[string]any{
		"argument_keys": keys, "argument_count": len(args),
		"undeclared_argument_count": undeclared, "argument_keys_truncated": truncated,
	}
}

func (m *upstreamMCPModule) AuditMetadata(tool string, _ map[string]any, value any) map[string]any {
	metadata := map[string]any{
		"server": m.serverName, "module": m.name, "transport": m.client.Transport(),
		"upstream_tool": m.upstream[tool], "read_only": m.readOnly[tool], "policy": m.policy[tool],
	}
	if result, ok := value.(*mcp.CallToolResult); ok {
		metadata["upstream_is_error"] = result.IsError
		metadata["content_items"] = len(result.Content)
		metadata["structured_content"] = result.StructuredContent != nil
	}
	return metadata
}

func (*upstreamMCPModule) AuditError(error) string {
	return "upstream MCP call failed; inspect module health for connection metadata"
}

func (*upstreamMCPModule) CaptureObservation(string) bool { return false }

func (m *upstreamMCPModule) buildSpecs(cfg config.MCPServerConfig, tools []*mcp.Tool) error {
	if m.upstream == nil {
		m.upstream = map[string]string{}
	}
	if m.readOnly == nil {
		m.readOnly = map[string]bool{}
	}
	if m.policy == nil {
		m.policy = map[string]string{}
	}
	if m.declaredArgs == nil {
		m.declaredArgs = map[string]map[string]bool{}
	}
	seenPublic := map[string]string{}
	totalSchemaBytes := 0
	for _, tool := range tools {
		if len(tool.Name) > maxUpstreamToolNameBytes {
			return fmt.Errorf("upstream MCP server %q returned a tool name larger than %d bytes", m.serverName, maxUpstreamToolNameBytes)
		}
		if !toolAllowed(cfg, tool.Name) {
			continue
		}
		mode := upstreamToolPolicy(cfg, tool)
		if mode == "deny" {
			continue
		}
		publicName := upstreamPublicToolName(m.serverName, tool.Name)
		if previous := seenPublic[publicName]; previous != "" {
			return fmt.Errorf("upstream MCP server %q tools %q and %q normalize to the same public name %q", m.serverName, previous, tool.Name, publicName)
		}
		seenPublic[publicName] = tool.Name
		schema, schemaBytes, err := upstreamInputSchema(tool.InputSchema)
		if err != nil {
			return fmt.Errorf("upstream MCP server %q tool %q input schema: %w", m.serverName, tool.Name, err)
		}
		totalSchemaBytes += schemaBytes
		if totalSchemaBytes > maxUpstreamTotalSchemaBytes {
			return fmt.Errorf("upstream MCP server %q tool schemas exceed %d bytes in total", m.serverName, maxUpstreamTotalSchemaBytes)
		}
		title := strings.TrimSpace(tool.Title)
		if title == "" && tool.Annotations != nil {
			title = strings.TrimSpace(tool.Annotations.Title)
		}
		if title == "" {
			title = tool.Name
		}
		title = truncateUTF8(title, maxUpstreamToolTitleBytes)
		description := strings.TrimSpace(tool.Description)
		if description == "" {
			description = fmt.Sprintf("Call upstream MCP tool %q from server %q.", tool.Name, m.serverName)
		} else {
			description = fmt.Sprintf("Upstream MCP server %q: %s", m.serverName, description)
		}
		description = truncateUTF8(description, maxUpstreamToolDescriptionBytes)
		readOnly := mode == "read-only"
		destructive := !readOnly
		openWorld := true
		if cfg.Policy.TrustAnnotations && tool.Annotations != nil {
			if readOnly {
				destructive = false
			} else if tool.Annotations.DestructiveHint != nil {
				destructive = *tool.Annotations.DestructiveHint
			}
			if tool.Annotations.OpenWorldHint != nil {
				openWorld = *tool.Annotations.OpenWorldHint
			}
		}
		m.specs = append(m.specs, ToolSpec{
			Name: publicName, Title: title, Description: description,
			ReadOnly: readOnly, Destructive: destructive, OpenWorld: openWorld, Schema: schema,
		})
		declaredArgs := map[string]bool{}
		if properties, ok := schema["properties"].(map[string]any); ok {
			for key := range properties {
				declaredArgs[key] = true
			}
		}
		m.declaredArgs[publicName] = declaredArgs
		m.upstream[publicName] = tool.Name
		m.readOnly[publicName] = readOnly
		m.policy[publicName] = mode
	}
	if len(m.specs) == 0 {
		return fmt.Errorf("upstream MCP server %q exposes no tools after filtering and policy", m.serverName)
	}
	sort.SliceStable(m.specs, func(i, j int) bool { return m.specs[i].Name < m.specs[j].Name })
	return nil
}

func toolAllowed(cfg config.MCPServerConfig, name string) bool {
	if len(cfg.AllowedTools) > 0 && !stringListContains(cfg.AllowedTools, name) {
		return false
	}
	return !stringListContains(cfg.DeniedTools, name)
}

func upstreamToolPolicy(cfg config.MCPServerConfig, tool *mcp.Tool) string {
	name := tool.Name
	if stringListContains(cfg.Policy.AlwaysApproveTools, name) {
		return "always-approval"
	}
	if stringListContains(cfg.Policy.ApprovalTools, name) {
		return "approval"
	}
	if stringListContains(cfg.Policy.ReadOnlyTools, name) {
		return "read-only"
	}
	if cfg.Policy.TrustAnnotations && tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
		return "read-only"
	}
	return cfg.Policy.Default
}

func upstreamInputSchema(value any) (map[string]any, int, error) {
	if value == nil {
		return object(nil), 0, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, 0, err
	}
	if len(raw) > maxUpstreamToolSchemaBytes {
		return nil, 0, fmt.Errorf("schema exceeds %d bytes", maxUpstreamToolSchemaBytes)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, 0, err
	}
	if schema == nil {
		return object(nil), len(raw), nil
	}
	if schemaType, exists := schema["type"]; exists && schemaType != "object" {
		return nil, 0, errors.New("input schema type must be object")
	}
	if schema["type"] == nil {
		schema["type"] = "object"
	}
	if properties, exists := schema["properties"]; exists && properties != nil {
		if _, ok := properties.(map[string]any); !ok {
			return nil, 0, errors.New("input schema properties must be an object")
		}
	} else {
		schema["properties"] = map[string]any{}
	}
	return schema, len(raw), nil
}

func upstreamPublicToolName(serverName, upstream string) string {
	normalized := normalizeUpstreamName(upstream)
	name := serverName + "__" + normalized
	if len(name) <= 64 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "_" + hex.EncodeToString(sum[:4])
	return name[:64-len(suffix)] + suffix
}

func normalizeUpstreamName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastSeparator := false
	for _, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
		if valid {
			builder.WriteRune(char)
			lastSeparator = false
			continue
		}
		if !lastSeparator && builder.Len() > 0 {
			builder.WriteByte('_')
			lastSeparator = true
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		sum := sha256.Sum256([]byte(value))
		return "tool_" + hex.EncodeToString(sum[:4])
	}
	return result
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func stringListContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
