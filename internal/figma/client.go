// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package figma

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Client struct {
	Endpoint    string
	Timeout     time.Duration
	AllowRemote bool
	Version     string
}

func (c Client) validate() error {
	parsed, err := url.Parse(c.Endpoint)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("Figma MCP endpoint must use http or https")
	}
	host := parsed.Hostname()
	if c.AllowRemote || host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("Figma MCP endpoint must be loopback unless FIGMA_DESKTOP_ALLOW_REMOTE=1")
	}
	return nil
}

func (c Client) connect(ctx context.Context) (*mcp.ClientSession, context.CancelFunc, error) {
	if err := c.validate(); err != nil {
		return nil, nil, err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	client := mcp.NewClient(&mcp.Implementation{Name: "codebridge-figma-bridge", Version: c.Version}, nil)
	session, err := client.Connect(callCtx, &mcp.StreamableClientTransport{
		Endpoint: c.Endpoint, HTTPClient: &http.Client{Timeout: timeout},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return session, cancel, nil
}

func (c Client) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	session, cancel, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer session.Close()
	var tools []*mcp.Tool
	cursor := ""
	for {
		result, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		tools = append(tools, result.Tools...)
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	return tools, nil
}

func (c Client) Status(ctx context.Context) map[string]any {
	tools, err := c.ListTools(ctx)
	if err != nil {
		return map[string]any{"connected": false, "endpoint": c.Endpoint, "error": err.Error()}
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return map[string]any{"connected": true, "endpoint": c.Endpoint, "count": len(names), "tools": names}
}

func (c Client) Call(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("Figma tool name is required")
	}
	session, cancel, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer session.Close()
	return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

func BuildArguments(input map[string]any) map[string]any {
	args := map[string]any{}
	if extra, ok := input["arguments"].(map[string]any); ok {
		for key, value := range extra {
			args[key] = value
		}
	}
	nodeID := stringArg(input, "node_id", "")
	if nodeID == "" {
		if raw := stringArg(input, "url", ""); raw != "" {
			if parsed, err := url.Parse(raw); err == nil {
				nodeID = parsed.Query().Get("node-id")
			}
		}
	}
	if nodeID != "" {
		args["nodeId"] = strings.ReplaceAll(nodeID, "-", ":")
	}
	for source, target := range map[string]string{
		"client_languages": "clientLanguages", "client_frameworks": "clientFrameworks",
		"force_code": "forceCode", "enable_base64_response": "enableBase64Response",
	} {
		if value, ok := input[source]; ok {
			args[target] = value
		}
	}
	return args
}

func stringArg(input map[string]any, key, fallback string) string {
	if value, ok := input[key].(string); ok {
		return value
	}
	return fallback
}
