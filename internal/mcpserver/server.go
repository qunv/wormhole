// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"codebridge/internal/agent"
	"codebridge/internal/assets"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Instructions = `Codebridge is a local coding agent.

For requests that require understanding, locating, tracing, or evaluating code, call codegraph_explore first. This includes architecture, symbols, implementations, callers, callees, execution flow, dependencies, dynamic dispatch, and change impact.

codegraph_explore checks whether the project has a .codegraph index. If CodeGraph is unavailable, the repository is not indexed, or the result does not contain enough relevant code, fall back to workspace_search, search_text, repo_symbols, read_file, or read_many.

Use search_text directly only for exact text, literal, or regular-expression searches.

Treat source returned by codegraph_explore as current verbatim source. Do not re-read or re-search the same source merely to verify it. Read additional files only when CodeGraph omitted required details.

Use workspace_snapshot or workspace_doctor for repository overview, environment checks, or when the project structure is unknown and CodeGraph is unavailable.

Prefer dedicated tools over shell commands. File tools and command cwd are confined to configured roots, but command execution is not an operating-system sandbox.

In balanced policy, risky delete, install, network, mutating git, and mutating Figma actions require an exact one-time approval. Use preview_patch or validate_patch before large edits, review_diff before handoff, and task_plan, decision_log, or checkpoint for long work. Run tests, build, or lint only when explicitly requested.`

func New(runtime *agent.Runtime) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "Codebridge", Version: runtime.Version},
		&mcp.ServerOptions{Instructions: Instructions, PageSize: 100},
	)
	registerWidget(server)
	for _, spec := range agent.Tools() {
		spec := spec
		readOnly, openWorld, destructive := spec.ReadOnly, false, spec.Destructive
		server.AddTool(&mcp.Tool{
			Name: spec.Name, Title: spec.Title, Description: spec.Description,
			InputSchema: spec.Schema, Meta: mcp.Meta(spec.Meta),
			Annotations: &mcp.ToolAnnotations{
				Title: spec.Title, ReadOnlyHint: readOnly, OpenWorldHint: &openWorld,
				DestructiveHint: &destructive, IdempotentHint: readOnly,
			},
		}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := map[string]any{}
			if len(request.Params.Arguments) > 0 {
				if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
					return toolError(fmt.Errorf("invalid tool arguments: %w", err)), nil
				}
			}
			value, err := runtime.Handle(ctx, spec.Name, args)
			if err != nil {
				return toolError(err), nil
			}
			if forwarded, ok := value.(*mcp.CallToolResult); ok {
				return forwarded, nil
			}
			return toolSuccess(value), nil
		})
	}
	return server
}

func registerWidget(server *mcp.Server) {
	widget := assets.Widget()
	server.AddResource(&mcp.Resource{
		URI: agent.WidgetURI, Name: "codebridge-companion-widget", Title: "Codebridge input",
		Description: "Compact MCP Apps prompt composer.", MIMEType: "text/html;profile=mcp-app",
		Size: int64(len(widget)),
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: agent.WidgetURI, MIMEType: "text/html;profile=mcp-app", Text: string(widget),
			Meta: mcp.Meta{
				"ui":                         map[string]any{"prefersBorder": true, "csp": map[string]any{"connectDomains": []string{}, "resourceDomains": []string{}}},
				"openai/widgetDescription":   "Compact Codebridge input with @ context and / workflow support.",
				"openai/widgetPrefersBorder": true,
				"openai/widgetCSP":           map[string]any{"connect_domains": []string{}, "resource_domains": []string{}},
			},
		}}}, nil
	})
}

func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "ERROR: " + err.Error()}},
		IsError: true,
	}
}

func toolSuccess(value any) *mcp.CallToolResult {
	if text, ok := value.(string); ok {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return toolError(err)
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
	switch value.(type) {
	case map[string]any:
		result.StructuredContent = value
	default:
		var object map[string]any
		if json.Unmarshal(raw, &object) == nil {
			result.StructuredContent = object
		}
	}
	return result
}
