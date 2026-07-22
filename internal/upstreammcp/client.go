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
	"sync/atomic"
	"time"

	"codebridge/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	stderrLimit              = 32 << 10
	circuitFailureThreshold  = 3
	commandTerminateDuration = 5 * time.Second
	commandShutdownPhases    = 3
)

type clientSessionState struct {
	session *mcp.ClientSession
	refs    int
	retired bool
}

type healthCacheEntry struct {
	checkedAt time.Time
	available bool
	errorText string
	session   *clientSessionState
}

type Client struct {
	name    string
	cfg     config.MCPServerConfig
	version string
	cwd     string

	connectMu     sync.Mutex
	healthCheckMu sync.Mutex
	mu            sync.RWMutex

	session          *clientSessionState
	tools            []*mcp.Tool
	closed           bool
	connectedAt      time.Time
	lastError        string
	reconnects       int
	resolvedCommand  string
	processID        int
	endpointHost     string
	secrets          []string
	stderr           *boundedCounter
	callSlots        chan struct{}
	healthCache      healthCacheEntry
	consecutiveFails int
	circuitOpenUntil time.Time
	breakerTrips     uint64

	queuedCalls       atomic.Int64
	inFlightCalls     atomic.Int64
	maxInFlightCalls  atomic.Int64
	completedCalls    atomic.Uint64
	rejectedCalls     atomic.Uint64
	circuitRejected   atomic.Uint64
	healthCacheHits   atomic.Uint64
	healthCacheMisses atomic.Uint64
}

type transportBuild struct {
	transport       mcp.Transport
	command         *exec.Cmd
	resolvedCommand string
	endpointHost    string
	secrets         []string
}

func effectiveClientConfig(cfg config.MCPServerConfig) config.MCPServerConfig {
	if cfg.StartupTimeoutMS <= 0 {
		cfg.StartupTimeoutMS = config.DefaultMCPStartupTimeoutMS
	}
	if cfg.CallTimeoutMS <= 0 {
		cfg.CallTimeoutMS = config.DefaultMCPCallTimeoutMS
	}
	if cfg.HealthTimeoutMS <= 0 {
		cfg.HealthTimeoutMS = config.DefaultMCPHealthTimeoutMS
	}
	if cfg.HealthCacheMS <= 0 {
		cfg.HealthCacheMS = config.DefaultMCPHealthCacheMS
	}
	if cfg.FailureCooldownMS <= 0 {
		cfg.FailureCooldownMS = config.DefaultMCPFailureCooldownMS
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = config.DefaultMCPMaxConcurrency
	}
	if cfg.MaxTools <= 0 {
		cfg.MaxTools = config.DefaultMCPMaxTools
	}
	return cfg
}

// StartupWaitTimeout returns the supervisor budget for one connection attempt.
// CommandTransport.Close can wait before SIGTERM, after SIGTERM, and after
// SIGKILL, so stdio startup failures need cleanup time beyond StartupTimeoutMS.
func StartupWaitTimeout(cfg config.MCPServerConfig) time.Duration {
	cfg = effectiveClientConfig(cfg)
	timeout := time.Duration(cfg.StartupTimeoutMS) * time.Millisecond
	if cfg.EffectiveTransport() == "stdio" {
		timeout += commandShutdownPhases * commandTerminateDuration
	}
	return timeout
}

func New(ctx context.Context, name string, cfg config.MCPServerConfig, version, cwd string) (*Client, error) {
	cfg = effectiveClientConfig(cfg)
	client := &Client{
		name: name, cfg: cfg, version: version, cwd: cwd,
		stderr: newBoundedCounter(stderrLimit), callSlots: make(chan struct{}, cfg.MaxConcurrency),
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
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.acquireCallSlot(ctx); err != nil {
		return nil, err
	}
	defer c.releaseCallSlot()

	state, err := c.borrowSessionOrReconnect(ctx)
	if err != nil {
		return nil, err
	}
	result, callErr := c.callSession(ctx, state.session, tool, args)
	c.releaseSession(state)
	if callErr == nil {
		c.clearErrorFor(state)
		return result, nil
	}
	if !shouldInvalidateSession(callErr) {
		return nil, c.sanitizedError(callErr)
	}
	c.invalidateSession(state, callErr)
	if !retryReadOnly {
		return nil, c.sanitizedError(callErr)
	}
	state, err = c.borrowSessionOrReconnect(ctx)
	if err != nil {
		return nil, err
	}
	result, callErr = c.callSession(ctx, state.session, tool, args)
	c.releaseSession(state)
	if callErr != nil {
		if shouldInvalidateSession(callErr) {
			c.invalidateSession(state, callErr)
		}
		return nil, c.sanitizedError(callErr)
	}
	c.clearErrorFor(state)
	return result, nil
}

func (c *Client) Health(ctx context.Context) map[string]any {
	if ctx == nil {
		ctx = context.Background()
	}
	if cached, ok := c.cachedHealth(); ok {
		c.healthCacheHits.Add(1)
		return c.healthStatus(cached, true)
	}
	c.healthCheckMu.Lock()
	defer c.healthCheckMu.Unlock()
	if cached, ok := c.cachedHealth(); ok {
		c.healthCacheHits.Add(1)
		return c.healthStatus(cached, true)
	}
	c.healthCacheMisses.Add(1)
	entry := healthCacheEntry{checkedAt: time.Now().UTC()}
	state, err := c.borrowCurrentSession()
	if err != nil {
		entry.errorText = c.currentError(err.Error())
		c.storeHealth(entry, nil)
		return c.healthStatus(entry, false)
	}
	entry.session = state
	timeout := time.Duration(c.cfg.HealthTimeoutMS) * time.Millisecond
	healthCtx, cancel := context.WithTimeout(ctx, timeout)
	err = state.session.Ping(healthCtx, nil)
	healthContextErr := healthCtx.Err()
	cancel()
	c.releaseSession(state)
	if err != nil {
		if healthContextErr != nil {
			err = healthContextErr
		}
		if shouldInvalidateSession(err) {
			c.invalidateSession(state, err)
		}
		entry.errorText = c.sanitizedError(err).Error()
	} else if c.sessionIsCurrent(state) {
		entry.available = true
		c.clearErrorFor(state)
	} else {
		entry.errorText = c.currentError("upstream MCP session changed during health check")
	}
	c.storeHealth(entry, state)
	return c.healthStatus(entry, false)
}

func (c *Client) Close() error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()
	var closeSession *mcp.ClientSession
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	state := c.session
	c.session = nil
	c.processID = 0
	c.healthCache = healthCacheEntry{}
	if state != nil {
		state.retired = true
		if state.refs == 0 {
			closeSession = state.session
		}
	}
	c.mu.Unlock()
	if closeSession == nil {
		return nil
	}
	if err := closeSession.Close(); err != nil {
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

	var closePrevious *mcp.ClientSession
	c.mu.Lock()
	previous := c.session
	c.session = &clientSessionState{session: session}
	if previous != nil {
		previous.retired = true
		if previous.refs == 0 {
			closePrevious = previous.session
		}
	}
	if discover {
		c.tools = tools
	}
	c.connectedAt = time.Now().UTC()
	c.lastError = ""
	c.consecutiveFails = 0
	c.circuitOpenUntil = time.Time{}
	c.healthCache = healthCacheEntry{}
	c.resolvedCommand = build.resolvedCommand
	c.endpointHost = build.endpointHost
	c.secrets = append([]string(nil), build.secrets...)
	c.processID = 0
	if build.command != nil && build.command.Process != nil {
		c.processID = build.command.Process.Pid
	}
	c.mu.Unlock()
	if closePrevious != nil {
		_ = closePrevious.Close()
	}
	return nil
}

func (c *Client) borrowSessionOrReconnect(ctx context.Context) (*clientSessionState, error) {
	if state, err := c.borrowCurrentSession(); err == nil {
		return state, nil
	}
	c.connectMu.Lock()
	defer c.connectMu.Unlock()
	if state, err := c.borrowCurrentSession(); err == nil {
		return state, nil
	}
	if err := c.circuitError(); err != nil {
		c.circuitRejected.Add(1)
		return nil, err
	}
	if err := c.reconnectLocked(ctx); err != nil {
		return nil, err
	}
	return c.borrowCurrentSession()
}

func (c *Client) borrowCurrentSession() (*clientSessionState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("upstream MCP client is closed")
	}
	state := c.session
	if state == nil || state.retired || state.session == nil {
		return nil, errors.New("upstream MCP session is disconnected")
	}
	state.refs++
	return state, nil
}

func (c *Client) releaseSession(state *clientSessionState) {
	if state == nil {
		return
	}
	var closeSession *mcp.ClientSession
	c.mu.Lock()
	if state.refs > 0 {
		state.refs--
	}
	if state.retired && state.refs == 0 {
		closeSession = state.session
		state.session = nil
	}
	c.mu.Unlock()
	if closeSession != nil {
		_ = closeSession.Close()
	}
}

func (c *Client) sessionIsCurrent(state *clientSessionState) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return state != nil && c.session == state && !state.retired && state.session != nil
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

func (c *Client) invalidateSession(failed *clientSessionState, callErr error) {
	if failed == nil {
		return
	}
	var closeSession *mcp.ClientSession
	c.mu.Lock()
	if c.session == failed {
		c.session = nil
		c.processID = 0
		failed.retired = true
		c.recordFailureLocked(callErr)
		c.healthCache = healthCacheEntry{}
		if failed.refs == 0 {
			closeSession = failed.session
			failed.session = nil
		}
	}
	c.mu.Unlock()
	if closeSession != nil {
		_ = closeSession.Close()
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
			transport: &mcp.CommandTransport{Command: cmd, TerminateDuration: commandTerminateDuration},
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

func (c *Client) acquireCallSlot(ctx context.Context) error {
	c.queuedCalls.Add(1)
	select {
	case c.callSlots <- struct{}{}:
		c.queuedCalls.Add(-1)
		active := c.inFlightCalls.Add(1)
		for {
			current := c.maxInFlightCalls.Load()
			if active <= current || c.maxInFlightCalls.CompareAndSwap(current, active) {
				break
			}
		}
		return nil
	case <-ctx.Done():
		c.queuedCalls.Add(-1)
		c.rejectedCalls.Add(1)
		return ctx.Err()
	}
}

func (c *Client) releaseCallSlot() {
	c.inFlightCalls.Add(-1)
	c.completedCalls.Add(1)
	<-c.callSlots
}

func (c *Client) status(available bool, errorText string) map[string]any {
	c.mu.RLock()
	connectedAt := c.connectedAt
	toolCount := len(c.tools)
	reconnects := c.reconnects
	resolvedCommand := c.resolvedCommand
	processID := c.processID
	endpointHost := c.endpointHost
	consecutiveFailures := c.consecutiveFails
	circuitOpenUntil := c.circuitOpenUntil
	breakerTrips := c.breakerTrips
	c.mu.RUnlock()
	stderrBytes, stderrTruncated := c.stderr.Stats()
	result := map[string]any{
		"server": c.name, "transport": c.cfg.EffectiveTransport(), "available": available,
		"tool_count": toolCount, "reconnect_count": reconnects,
		"max_concurrency": c.cfg.MaxConcurrency, "queued_calls": c.queuedCalls.Load(),
		"in_flight_calls": c.inFlightCalls.Load(), "max_in_flight_calls": c.maxInFlightCalls.Load(),
		"completed_calls": c.completedCalls.Load(), "rejected_calls": c.rejectedCalls.Load(),
		"circuit_rejected": c.circuitRejected.Load(), "breaker_trips": breakerTrips,
		"consecutive_failures": consecutiveFailures,
		"health_cache_hits":    c.healthCacheHits.Load(), "health_cache_misses": c.healthCacheMisses.Load(),
		"health_cache_ttl_ms": c.cfg.HealthCacheMS,
		"stderr_bytes":        stderrBytes, "stderr_truncated": stderrTruncated,
	}
	if !circuitOpenUntil.IsZero() && time.Now().UTC().Before(circuitOpenUntil) {
		result["circuit_open"] = true
		result["circuit_open_until"] = circuitOpenUntil
	} else {
		result["circuit_open"] = false
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

func (c *Client) cachedHealth() (healthCacheEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry := c.healthCache
	validSession := entry.session == c.session
	return entry, validSession && !entry.checkedAt.IsZero() && time.Since(entry.checkedAt) < time.Duration(c.cfg.HealthCacheMS)*time.Millisecond
}

func (c *Client) storeHealth(entry healthCacheEntry, state *clientSessionState) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != state {
		return false
	}
	entry.session = state
	c.healthCache = entry
	return true
}

func (c *Client) healthStatus(entry healthCacheEntry, cached bool) map[string]any {
	result := c.status(entry.available, entry.errorText)
	result["health_cached"] = cached
	result["health_checked_at"] = entry.checkedAt
	result["health_age_ms"] = max(int64(0), time.Since(entry.checkedAt).Milliseconds())
	return result
}

func (c *Client) circuitError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.circuitOpenUntil.IsZero() && time.Now().UTC().Before(c.circuitOpenUntil) {
		return errors.New("upstream MCP circuit is cooling down")
	}
	return nil
}

func (c *Client) recordFailureLocked(err error) {
	if err == nil {
		return
	}
	c.lastError = c.sanitizeLocked(err.Error())
	c.consecutiveFails++
	if c.consecutiveFails >= circuitFailureThreshold {
		c.circuitOpenUntil = time.Now().UTC().Add(time.Duration(c.cfg.FailureCooldownMS) * time.Millisecond)
		c.breakerTrips++
	}
}

func (c *Client) setSecrets(values []string) {
	c.mu.Lock()
	c.secrets = append([]string(nil), values...)
	c.mu.Unlock()
}

func (c *Client) setError(err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	c.mu.Lock()
	c.recordFailureLocked(err)
	c.healthCache = healthCacheEntry{}
	c.mu.Unlock()
}

func shouldInvalidateSession(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func (c *Client) clearErrorFor(state *clientSessionState) {
	c.mu.Lock()
	if c.session == state {
		c.lastError = ""
		c.consecutiveFails = 0
		c.circuitOpenUntil = time.Time{}
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
