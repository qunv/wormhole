// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package upstreammcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"codebridge/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const stderrLimit = 32 << 10

type Client struct {
	name    string
	cfg     config.MCPServerConfig
	version string
	cwd     string

	connectMu sync.Mutex
	mu        sync.RWMutex

	session         *mcp.ClientSession
	tools           []*mcp.Tool
	closed          bool
	connectedAt     time.Time
	lastError       string
	reconnects      int
	resolvedCommand string
	processID       int
	endpointHost    string
	secrets         []string
	stderr          *boundedCounter
}

type transportBuild struct {
	transport       mcp.Transport
	command         *exec.Cmd
	resolvedCommand string
	endpointHost    string
	secrets         []string
}

func New(ctx context.Context, name string, cfg config.MCPServerConfig, version, cwd string) (*Client, error) {
	client := &Client{
		name: name, cfg: cfg, version: version, cwd: cwd,
		stderr: newBoundedCounter(stderrLimit),
	}
	client.connectMu.Lock()
	defer client.connectMu.Unlock()
	startupCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.StartupTimeoutMS)*time.Millisecond)
	defer cancel()
	if err := client.connectLocked(startupCtx, true); err != nil {
		return nil, err
	}
	return client, nil
}

func (c *Client) Name() string { return c.name }

func (c *Client) Transport() string { return c.cfg.EffectiveTransport() }

func (c *Client) Tools() []*mcp.Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]*mcp.Tool(nil), c.tools...)
}

func (c *Client) Call(ctx context.Context, tool string, args map[string]any, retryReadOnly bool) (*mcp.CallToolResult, error) {
	session, err := c.sessionOrReconnect(ctx)
	if err != nil {
		return nil, err
	}
	result, err := c.callSession(ctx, session, tool, args)
	if err == nil {
		c.clearErrorFor(session)
		return result, nil
	}
	if !shouldInvalidateSession(err) {
		return nil, c.sanitizedError(err)
	}
	c.invalidateSession(session, err)
	if !retryReadOnly {
		return nil, c.sanitizedError(err)
	}
	session, err = c.sessionOrReconnect(ctx)
	if err != nil {
		return nil, err
	}
	result, err = c.callSession(ctx, session, tool, args)
	if err != nil {
		if shouldInvalidateSession(err) {
			c.invalidateSession(session, err)
		}
		return nil, c.sanitizedError(err)
	}
	c.clearErrorFor(session)
	return result, nil
}

func (c *Client) Health(ctx context.Context) map[string]any {
	c.mu.RLock()
	closed, session := c.closed, c.session
	c.mu.RUnlock()
	if closed {
		return c.status(false, "upstream MCP client is closed")
	}
	if session == nil {
		return c.status(false, c.currentError("upstream MCP session is disconnected"))
	}
	timeout := time.Duration(c.cfg.HealthTimeoutMS) * time.Millisecond
	healthCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := session.Ping(healthCtx, nil); err != nil {
		if healthCtx.Err() != nil {
			err = healthCtx.Err()
		}
		if shouldInvalidateSession(err) {
			c.invalidateSession(session, err)
		}
		return c.status(false, c.sanitizedError(err).Error())
	}
	c.clearErrorFor(session)
	return c.status(true, "")
}

func (c *Client) Close() error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	session := c.session
	c.session = nil
	c.processID = 0
	c.mu.Unlock()
	if session == nil {
		return nil
	}
	if err := session.Close(); err != nil {
		return c.sanitizedError(err)
	}
	return nil
}

func (c *Client) connectLocked(ctx context.Context, discover bool) error {
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return errors.New("upstream MCP client is closed")
	}
	build, err := c.buildTransport()
	if err != nil {
		c.setError(err)
		return c.sanitizedError(err)
	}
	client := mcp.NewClient(
		&mcp.Implementation{Name: "codebridge-upstream-" + c.name, Version: c.version},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}},
	)
	session, err := client.Connect(ctx, build.transport, nil)
	if err != nil {
		c.setSecrets(build.secrets)
		c.setError(err)
		return c.sanitizedError(err)
	}
	var tools []*mcp.Tool
	if discover {
		tools, err = listTools(ctx, session, c.cfg.MaxTools)
		if err != nil {
			_ = session.Close()
			c.setSecrets(build.secrets)
			c.setError(err)
			return c.sanitizedError(err)
		}
		if len(tools) == 0 {
			_ = session.Close()
			err = errors.New("upstream MCP server returned no tools")
			c.setError(err)
			return err
		}
	}

	c.mu.Lock()
	previous := c.session
	c.session = session
	if discover {
		c.tools = tools
	}
	c.connectedAt = time.Now().UTC()
	c.lastError = ""
	c.resolvedCommand = build.resolvedCommand
	c.endpointHost = build.endpointHost
	c.secrets = append([]string(nil), build.secrets...)
	c.processID = 0
	if build.command != nil && build.command.Process != nil {
		c.processID = build.command.Process.Pid
	}
	c.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	return nil
}

func (c *Client) sessionOrReconnect(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.RLock()
	closed, session := c.closed, c.session
	c.mu.RUnlock()
	if closed {
		return nil, errors.New("upstream MCP client is closed")
	}
	if session != nil {
		return session, nil
	}

	c.connectMu.Lock()
	defer c.connectMu.Unlock()
	c.mu.RLock()
	closed, session = c.closed, c.session
	c.mu.RUnlock()
	if closed {
		return nil, errors.New("upstream MCP client is closed")
	}
	if session != nil {
		return session, nil
	}
	if err := c.reconnectLocked(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	session = c.session
	c.mu.RUnlock()
	if session == nil {
		return nil, errors.New("upstream MCP session is disconnected")
	}
	return session, nil
}

func (c *Client) reconnectLocked(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(c.cfg.StartupTimeoutMS)*time.Millisecond)
	defer cancel()
	if err := c.connectLocked(timeoutCtx, false); err != nil {
		return err
	}
	c.mu.Lock()
	c.reconnects++
	c.mu.Unlock()
	return nil
}

func (c *Client) callSession(ctx context.Context, session *mcp.ClientSession, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	if session == nil {
		return nil, errors.New("upstream MCP session is disconnected")
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(c.cfg.CallTimeoutMS)*time.Millisecond)
	defer cancel()
	result, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil && callCtx.Err() != nil {
		return nil, callCtx.Err()
	}
	return result, err
}

func (c *Client) invalidateSession(failed *mcp.ClientSession, callErr error) {
	detached := false
	c.mu.Lock()
	if c.session == failed {
		c.session = nil
		c.processID = 0
		c.lastError = c.sanitizeLocked(callErr.Error())
		detached = true
	}
	c.mu.Unlock()
	if detached && failed != nil {
		_ = failed.Close()
	}
}

func (c *Client) buildTransport() (transportBuild, error) {
	switch c.cfg.EffectiveTransport() {
	case "stdio":
		resolved, err := exec.LookPath(c.cfg.Command)
		if err != nil {
			return transportBuild{}, fmt.Errorf("resolve command %q: %w", c.cfg.Command, err)
		}
		environment, secrets, err := buildEnvironment(c.cfg)
		if err != nil {
			return transportBuild{}, err
		}
		cmd, err := newUpstreamCommand(resolved, c.cfg.Args)
		if err != nil {
			return transportBuild{}, err
		}
		cmd.Dir = c.cwd
		cmd.Env = environment
		cmd.Stderr = c.stderr
		return transportBuild{
			transport: &mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second},
			command:   cmd, resolvedCommand: resolved, secrets: secrets,
		}, nil
	case "streamable-http":
		endpointHost, headers, secrets, err := buildHTTPConfig(c.cfg)
		if err != nil {
			return transportBuild{}, err
		}
		httpClient := &http.Client{
			Timeout:   time.Duration(c.cfg.CallTimeoutMS) * time.Millisecond,
			Transport: &headerTransport{base: http.DefaultTransport, headers: headers},
		}
		return transportBuild{
			transport: &mcp.StreamableClientTransport{
				Endpoint: c.cfg.URL, HTTPClient: httpClient,
				DisableStandaloneSSE: true, MaxRetries: -1,
			},
			endpointHost: endpointHost, secrets: secrets,
		}, nil
	default:
		return transportBuild{}, fmt.Errorf("unsupported upstream MCP transport %q", c.cfg.EffectiveTransport())
	}
}

func (c *Client) status(available bool, errorText string) map[string]any {
	c.mu.RLock()
	connectedAt := c.connectedAt
	toolCount := len(c.tools)
	reconnects := c.reconnects
	resolvedCommand := c.resolvedCommand
	processID := c.processID
	endpointHost := c.endpointHost
	c.mu.RUnlock()
	stderrBytes, stderrTruncated := c.stderr.Stats()
	result := map[string]any{
		"server": c.name, "transport": c.cfg.EffectiveTransport(), "available": available,
		"tool_count": toolCount, "reconnect_count": reconnects,
		"stderr_bytes": stderrBytes, "stderr_truncated": stderrTruncated,
	}
	if !connectedAt.IsZero() {
		result["connected_at"] = connectedAt
	}
	if resolvedCommand != "" {
		result["command"] = filepath.Base(resolvedCommand)
	}
	if processID > 0 {
		result["process_id"] = processID
	}
	if endpointHost != "" {
		result["endpoint_host"] = endpointHost
	}
	if errorText != "" {
		result["error"] = c.sanitize(errorText)
	}
	return result
}

func (c *Client) setSecrets(values []string) {
	c.mu.Lock()
	c.secrets = append([]string(nil), values...)
	c.mu.Unlock()
}

func (c *Client) setError(err error) {
	c.mu.Lock()
	c.lastError = c.sanitizeLocked(err.Error())
	c.mu.Unlock()
}

func shouldInvalidateSession(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func (c *Client) clearErrorFor(session *mcp.ClientSession) {
	c.mu.Lock()
	if c.session == session {
		c.lastError = ""
	}
	c.mu.Unlock()
}

func (c *Client) currentError(fallback string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.lastError != "" {
		return c.lastError
	}
	return fallback
}

func (c *Client) sanitizedError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New(c.sanitize(err.Error()))
}

func (c *Client) sanitize(value string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sanitizeLocked(value)
}

func (c *Client) sanitizeLocked(value string) string {
	for _, secret := range c.secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func listTools(ctx context.Context, session *mcp.ClientSession, maxTools int) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	cursor := ""
	seenCursors := map[string]bool{}
	seenNames := map[string]bool{}
	for {
		result, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("list upstream MCP tools: %w", err)
		}
		for _, tool := range result.Tools {
			if tool == nil || strings.TrimSpace(tool.Name) == "" {
				return nil, errors.New("upstream MCP server returned a tool without a name")
			}
			if seenNames[tool.Name] {
				return nil, fmt.Errorf("upstream MCP server returned duplicate tool %q", tool.Name)
			}
			seenNames[tool.Name] = true
			tools = append(tools, tool)
			if len(tools) > maxTools {
				return nil, fmt.Errorf("upstream MCP server returned more than %d tools", maxTools)
			}
		}
		if result.NextCursor == "" {
			break
		}
		if seenCursors[result.NextCursor] {
			return nil, errors.New("upstream MCP tool pagination repeated a cursor")
		}
		seenCursors[result.NextCursor] = true
		cursor = result.NextCursor
	}
	sort.SliceStable(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, nil
}

func buildEnvironment(cfg config.MCPServerConfig) ([]string, []string, error) {
	values := map[string]string{}
	for _, name := range defaultInheritedEnvironment() {
		if value, exists := os.LookupEnv(name); exists {
			values[name] = value
		}
	}
	for _, name := range cfg.InheritEnv {
		if value, exists := os.LookupEnv(name); exists {
			values[name] = value
		}
	}
	for name, value := range cfg.Env {
		values[name] = value
	}
	var secrets []string
	for target, source := range cfg.EnvRefs {
		value, exists := os.LookupEnv(source)
		if !exists || value == "" {
			return nil, nil, fmt.Errorf("environment variable %q required by envRefs.%s is empty", source, target)
		}
		values[target] = value
		secrets = append(secrets, value)
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+values[name])
	}
	return environment, secrets, nil
}

func defaultInheritedEnvironment() []string {
	return []string{
		"PATH", "HOME", "USERPROFILE", "SystemRoot", "ComSpec", "PATHEXT",
		"TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "APPDATA", "LOCALAPPDATA",
	}
}

func buildHTTPConfig(cfg config.MCPServerConfig) (string, http.Header, []string, error) {
	parsed, err := url.Parse(cfg.URL)
	if err != nil {
		return "", nil, nil, err
	}
	host := parsed.Hostname()
	if !cfg.AllowRemote && !isLoopbackHost(host) {
		return "", nil, nil, errors.New("remote upstream MCP endpoint requires allowRemote=true")
	}
	headers := http.Header{}
	for name, value := range cfg.Headers {
		headers.Set(name, value)
	}
	var secrets []string
	for header, source := range cfg.HeaderRefs {
		value, exists := os.LookupEnv(source)
		if !exists || value == "" {
			return "", nil, nil, fmt.Errorf("environment variable %q required by headerRefs.%s is empty", source, header)
		}
		headers.Set(header, value)
		secrets = append(secrets, value)
	}
	return host, headers, secrets, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t *headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	for name, values := range t.headers {
		clone.Header.Del(name)
		for _, value := range values {
			clone.Header.Add(name, value)
		}
	}
	return t.base.RoundTrip(clone)
}

type boundedCounter struct {
	mu        sync.Mutex
	limit     int64
	bytes     int64
	truncated bool
}

func newBoundedCounter(limit int64) *boundedCounter { return &boundedCounter{limit: limit} }

func (w *boundedCounter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.bytes += int64(len(p))
	if w.bytes > w.limit {
		w.truncated = true
	}
	return len(p), nil
}

func (w *boundedCounter) Stats() (int64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytes, w.truncated
}
