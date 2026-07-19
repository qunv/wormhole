// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"codebridge/internal/assets"
	"codebridge/internal/config"
	memoryfactory "codebridge/internal/memory/factory"

	"golang.org/x/term"
)

type processState struct {
	ServerPID int       `json:"serverPid,omitempty"`
	TunnelPID int       `json:"tunnelPid,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	ConfigID  string    `json:"configId"`
	Port      int       `json:"port"`
	Workspace string    `json:"workspace"`
}

type startupLogFollower struct {
	path    string
	offset  int64
	pending string
}

func (a App) start(ctx context.Context, cfg config.Config, opts options) error {
	if err := cfg.Validate(true); err != nil {
		return err
	}
	if opts.Save {
		if err := config.Save(cfg); err != nil {
			return err
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	configID := cfg.ConfigID(executable, assets.Widget())
	state := readState()
	health := readHealth(cfg.Port)
	existingPID := numberValue(healthValue(health, "pid"))
	if existingPID == 0 && health != nil && state.Port == cfg.Port && pidAlive(state.ServerPID) {
		existingPID = state.ServerPID
	}
	existingConfigID := fmt.Sprint(healthValue(health, "config_id"))
	if existingConfigID == "<nil>" || existingConfigID == "" {
		if state.Port == cfg.Port && existingPID == state.ServerPID {
			existingConfigID = state.ConfigID
		}
	}
	if health != nil && existingConfigID == configID {
		state.ServerPID = existingPID
	} else {
		if existingPID > 0 {
			reason := "workspace or configuration changed"
			if health == nil {
				reason = "stale server state detected"
			}
			fmt.Fprintf(a.Stdout, "[server] %s; stopping PID %d\n", reason, existingPID)
			if err := stopPID(existingPID); err != nil && pidAlive(existingPID) {
				return fmt.Errorf("stop existing MCP server PID %d: %w", existingPID, err)
			}
			if !waitForServerRelease(cfg.Host, cfg.Port, existingPID, 12*time.Second) {
				return fmt.Errorf("existing MCP server PID %d did not release %s", existingPID, net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
			}
			state.ServerPID = 0
			health = nil
		}
		if health == nil && !portAvailable(cfg.Host, cfg.Port) {
			return fmt.Errorf("MCP port %s is already in use by an unknown process; stop that process or choose another --port", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
		}
	}
	var serverCmd *exec.Cmd
	var serverExit <-chan error
	if health == nil {
		logOffset := fileSize(config.LogPath())
		fmt.Fprintf(a.Stdout, "[server] starting Codebridge for %s\n", cfg.Workspace)
		serverCmd, err = a.spawnServer(executable, cfg, opts.Background)
		if err != nil {
			return err
		}
		exit := make(chan error, 1)
		go func() { exit <- serverCmd.Wait() }()
		serverExit = exit
		waitTimeout := startupWaitTimeout(cfg)
		fmt.Fprintf(a.Stdout, "[server] PID %d; waiting for readiness (timeout %s)\n", serverCmd.Process.Pid, waitTimeout.Round(time.Second))
		var waitErr error
		health, waitErr = waitForHealthProgress(ctx, cfg.Port, waitTimeout, serverExit, opts.Background, &startupLogFollower{
			path: config.LogPath(), offset: logOffset,
		}, a.Stdout)
		if waitErr != nil {
			_ = stopPID(serverCmd.Process.Pid)
			return fmt.Errorf("MCP server startup failed: %w; see %s", waitErr, config.LogPath())
		}
		state.ServerPID = numberValue(healthValue(health, "pid"))
		if state.ServerPID == 0 {
			state.ServerPID = serverCmd.Process.Pid
		}
	} else {
		state.ServerPID = existingPID
	}
	fmt.Fprintf(a.Stdout, "[server] MCP OK: http://127.0.0.1:%d/mcp\n", cfg.Port)

	var tunnelCmd *exec.Cmd
	if !cfg.NoTunnel {
		if state.TunnelPID > 0 && pidAlive(state.TunnelPID) && state.ConfigID == configID {
			fmt.Fprintf(a.Stdout, "[tunnel] reusing PID %d\n", state.TunnelPID)
		} else {
			if state.TunnelPID > 0 && pidAlive(state.TunnelPID) {
				_ = stopPID(state.TunnelPID)
			}
			tunnelCmd, err = a.spawnTunnel(cfg, opts, opts.Background)
			if err != nil {
				if serverCmd != nil {
					_ = stopPID(serverCmd.Process.Pid)
				}
				return err
			}
			state.TunnelPID = tunnelCmd.Process.Pid
		}
	} else {
		if state.TunnelPID > 0 && pidAlive(state.TunnelPID) {
			fmt.Fprintf(a.Stdout, "[tunnel] stopping PID %d (--no-tunnel)\n", state.TunnelPID)
			_ = stopPID(state.TunnelPID)
		}
		state.TunnelPID = 0
	}
	state.UpdatedAt, state.ConfigID, state.Port, state.Workspace = time.Now().UTC(), configID, cfg.Port, cfg.Workspace
	if err := writeState(state); err != nil {
		return err
	}
	if opts.Background {
		fmt.Fprintln(a.Stdout, "Running in background.")
		return nil
	}
	wait := make(chan error, 2)
	if tunnelCmd != nil {
		go func() { wait <- tunnelCmd.Wait() }()
	}
	if serverCmd != nil {
		go func() { wait <- <-serverExit }()
	}
	if serverCmd == nil && tunnelCmd == nil {
		<-ctx.Done()
		return nil
	}
	select {
	case <-ctx.Done():
		if tunnelCmd != nil {
			_ = stopPID(tunnelCmd.Process.Pid)
		}
		if serverCmd != nil {
			_ = stopPID(serverCmd.Process.Pid)
		}
		return nil
	case err := <-wait:
		return err
	}
}

func (a App) spawnServer(executable string, cfg config.Config, background bool) (*exec.Cmd, error) {
	cmd := exec.Command(executable, "serve")
	cmd.Env = append(os.Environ(),
		"AGENT_WORKSPACE="+cfg.Workspace,
		"AGENT_MODE="+cfg.Mode,
		"AGENT_POLICY="+cfg.Policy,
		"AGENT_HOST="+cfg.Host,
		"PORT="+strconv.Itoa(cfg.Port),
		"AGENT_EXTRA_ROOTS_JSON="+mustJSON(cfg.ExtraRoots),
		"MCP_AUTH_TOKEN="+cfg.AuthToken,
		"AGENT_APPROVAL_TOKEN="+cfg.ApprovalToken,
		"MCP_ALLOWED_ORIGINS="+strings.Join(cfg.AllowedOrigins, ","),
		"CODEBRIDGE_MEMORY_ENABLED="+strconv.FormatBool(cfg.Memory.Enabled),
		"CODEBRIDGE_MEMORY_PROVIDER="+cfg.Memory.Provider,
		"CODEBRIDGE_MEMORY_ENDPOINT="+cfg.Memory.Endpoint,
		"CODEBRIDGE_MEMORY_SECRET_ENV="+cfg.Memory.SecretEnv,
		"CODEBRIDGE_MEMORY_TIMEOUT_MS="+strconv.Itoa(cfg.Memory.TimeoutMS),
		"CODEBRIDGE_MEMORY_CAPTURE="+cfg.Memory.CaptureMode,
		"CODEBRIDGE_MEMORY_TOKEN_BUDGET="+strconv.Itoa(cfg.Memory.TokenBudget),
		"CODEBRIDGE_MEMORY_AGENT_ID="+cfg.Memory.AgentID,
		"CODEBRIDGE_MEMORY_REQUIRED="+strconv.FormatBool(cfg.Memory.Required),
		"CODEBRIDGE_MEMORY_PROJECT_STRATEGY="+cfg.Memory.ProjectStrategy,
		"CODEBRIDGE_MEMORY_QUEUE_SIZE="+strconv.Itoa(cfg.Memory.QueueSize),
		"CODEBRIDGE_MEMORY_DELIVERY_TIMEOUT_MS="+strconv.Itoa(cfg.Memory.DeliveryTimeoutMS),
		"CODEBRIDGE_MEMORY_RETRY_MAX_ATTEMPTS="+strconv.Itoa(cfg.Memory.RetryMaxAttempts),
		"CODEBRIDGE_MEMORY_RETRY_BACKOFF_MS="+strconv.Itoa(cfg.Memory.RetryBackoffMS),
		"CODEBRIDGE_MEMORY_HEALTH_CACHE_MS="+strconv.Itoa(cfg.Memory.HealthCacheMS),
	)
	return a.startChild("server", cmd, background)
}

func (a App) spawnTunnel(cfg config.Config, opts options, background bool) (*exec.Cmd, error) {
	if cfg.TunnelID == "" {
		return nil, errors.New("tunnel ID is required; run setup, pass --tunnel-id, or use --no-tunnel")
	}
	if _, err := os.Stat(cfg.TunnelBin); err != nil {
		return nil, fmt.Errorf("tunnel-client not found at %s", cfg.TunnelBin)
	}
	runtimeKey := opts.RuntimeKey
	if runtimeKey == "" {
		runtimeKey = os.Getenv(cfg.RuntimeKeyEnv)
	}
	if runtimeKey == "" {
		return nil, fmt.Errorf("missing Runtime API key; set %s or run codebridge key set", cfg.RuntimeKeyEnv)
	}
	profilePath, err := writeTunnelProfile(cfg)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(a.Stdout, "[tunnel] Profile: %s\n", profilePath)
	cmd := exec.Command(cfg.TunnelBin,
		"run", "--profile", strings.TrimSuffix(filepath.Base(profilePath), filepath.Ext(profilePath)),
		"--profile-dir", cfg.ProfileDir, "--control-plane.tunnel-id", cfg.TunnelID,
		"--health.listen-addr", "127.0.0.1:0",
	)
	cmd.Dir = filepath.Dir(cfg.TunnelBin)
	cmd.Env = append(os.Environ(), "CONTROL_PLANE_API_KEY="+runtimeKey, "CONTROL_PLANE_TUNNEL_ID="+cfg.TunnelID)
	if cfg.AuthToken != "" {
		cmd.Env = append(cmd.Env, "MCP_AUTH_HEADER=Bearer "+cfg.AuthToken, "MCP_EXTRA_HEADERS=Authorization: env:MCP_AUTH_HEADER")
	}
	return a.startChild("tunnel", cmd, background)
}

func (a App) startChild(label string, cmd *exec.Cmd, background bool) (*exec.Cmd, error) {
	if err := os.MkdirAll(filepath.Dir(config.LogPath()), 0o700); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(config.LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(logFile, "[%s] [%s] %s\n", time.Now().UTC().Format(time.RFC3339), label, strings.Join(cmd.Args, " "))
	if background {
		cmd.Stdin = nil
		cmd.Stdout, cmd.Stderr = logFile, logFile
		prepareDetached(cmd)
	} else {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = a.Stdin, io.MultiWriter(a.Stdout, logFile), io.MultiWriter(a.Stderr, logFile)
	}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, err
	}
	if background {
		_ = logFile.Close()
	}
	return cmd, nil
}

func (a App) stop(cfg config.Config, _ options) error {
	state := readState()
	health := readHealth(cfg.Port)
	if state.TunnelPID > 0 && pidAlive(state.TunnelPID) {
		fmt.Fprintf(a.Stdout, "[tunnel] stopping PID %d\n", state.TunnelPID)
		_ = stopPID(state.TunnelPID)
	}
	serverPID := state.ServerPID
	if health != nil {
		serverPID = numberValue(health["pid"])
	}
	if serverPID > 0 && pidAlive(serverPID) {
		fmt.Fprintf(a.Stdout, "[server] stopping PID %d\n", serverPID)
		_ = stopPID(serverPID)
	}
	_ = os.Remove(config.PIDPath())
	fmt.Fprintln(a.Stdout, "Stopped.")
	return nil
}

func (a App) status(cfg config.Config, opts options) error {
	state, health := readState(), readHealth(cfg.Port)
	value := map[string]any{
		"config_path": config.ConfigPath(), "pid_path": config.PIDPath(), "log_path": config.LogPath(),
		"mcp_url": fmt.Sprintf("http://127.0.0.1:%d/mcp", cfg.Port), "server": health,
		"pids": map[string]any{
			"server": state.ServerPID, "server_alive": pidAlive(state.ServerPID),
			"tunnel": state.TunnelPID, "tunnel_alive": pidAlive(state.TunnelPID),
		},
	}
	if opts.JSON {
		raw, _ := json.MarshalIndent(value, "", "  ")
		fmt.Fprintln(a.Stdout, string(raw))
		return nil
	}
	fmt.Fprintf(a.Stdout, "Config:  %s\nMCP URL: http://127.0.0.1:%d/mcp\n", config.ConfigPath(), cfg.Port)
	if health == nil {
		fmt.Fprintln(a.Stdout, "Server:  offline")
	} else {
		fmt.Fprintf(a.Stdout, "Server:  ONLINE %v (%v/%v) pid=%v\n", health["version"], health["mode"], health["policy"], health["pid"])
	}
	fmt.Fprintf(a.Stdout, "Tunnel:  %s\n", ternary(pidAlive(state.TunnelPID), fmt.Sprintf("running pid=%d", state.TunnelPID), "offline"))
	return nil
}

func (a App) doctor(ctx context.Context, cfg config.Config, opts options) error {
	type check struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	var checks []check
	workspaceInfo, err := os.Stat(cfg.Workspace)
	checks = append(checks, check{"workspace", err == nil && workspaceInfo.IsDir(), cfg.Workspace})
	_, gitErr := exec.LookPath("git")
	checks = append(checks, check{"git", gitErr == nil, ternary(gitErr == nil, "available", "not found")})
	_, rgErr := exec.LookPath("rg")
	checks = append(checks, check{"ripgrep", rgErr == nil, ternary(rgErr == nil, "available", "optional; Go fallback will be used")})
	serverHealth := readHealth(cfg.Port)
	checks = append(checks, check{"server", serverHealth != nil, fmt.Sprintf("http://127.0.0.1:%d/healthz", cfg.Port)})
	if serverHealth != nil {
		deepHealth := readDeepHealth(cfg.Port)
		modules, _ := deepHealth["modules"].(map[string]any)
		for _, name := range config.SortedMCPServerNames(cfg.MCPServers) {
			serverConfig := cfg.MCPServers[name]
			if !serverConfig.IsEnabled() {
				continue
			}
			status, _ := modules["mcp_"+name].(map[string]any)
			available, _ := status["available"].(bool)
			detail := "not registered; inspect startup warnings and codebridge logs"
			if status != nil {
				detail = fmt.Sprintf("transport=%v tools=%v reconnects=%v", status["transport"], status["tool_count"], status["reconnect_count"])
				if errorText := strings.TrimSpace(fmt.Sprint(status["error"])); errorText != "" && errorText != "<nil>" {
					detail += " error=" + errorText
				}
			}
			checks = append(checks, check{"mcp:" + name, available, detail})
		}
	}
	if !cfg.NoTunnel {
		_, tunnelErr := os.Stat(cfg.TunnelBin)
		checks = append(checks, check{"tunnel-client", tunnelErr == nil, cfg.TunnelBin})
		checks = append(checks, check{"tunnel-id", cfg.TunnelID != "", ternary(cfg.TunnelID != "", "configured", "missing")})
		checks = append(checks, check{"runtime-key", os.Getenv(cfg.RuntimeKeyEnv) != "", cfg.RuntimeKeyEnv})
	}
	if cfg.Memory.Enabled {
		memoryProvider, memoryErr := memoryfactory.New(cfg.Memory)
		if memoryErr != nil {
			checks = append(checks, check{"memory", false, memoryErr.Error()})
		} else {
			memoryHealth := memoryProvider.Health(ctx)
			_ = memoryProvider.Close()
			checks = append(checks, check{"memory", memoryHealth.Available, fmt.Sprintf("%s %s", memoryHealth.Provider, memoryHealth.Endpoint)})
		}
	}
	ok := true
	for _, item := range checks {
		if !item.OK && item.Name == "workspace" {
			ok = false
		}
	}
	value := map[string]any{"ok": ok, "checks": checks}
	if opts.JSON {
		raw, _ := json.MarshalIndent(value, "", "  ")
		fmt.Fprintln(a.Stdout, string(raw))
	} else {
		for _, item := range checks {
			fmt.Fprintf(a.Stdout, "%s %-16s %s\n", ternary(item.OK, "OK  ", "WARN"), item.Name, item.Detail)
		}
	}
	return nil
}

func startupWaitTimeout(cfg config.Config) time.Duration {
	timeout := 10 * time.Second
	if cfg.Memory.Enabled && cfg.Memory.Required {
		timeout += time.Duration(cfg.Memory.TimeoutMS) * time.Millisecond
	}
	for _, server := range cfg.MCPServers {
		if server.IsEnabled() {
			timeout += time.Duration(server.StartupTimeoutMS) * time.Millisecond
		}
	}
	return timeout
}

func waitForHealthProgress(ctx context.Context, port int, timeout time.Duration, processExit <-chan error, followLog bool, follower *startupLogFollower, output io.Writer) (map[string]any, error) {
	startedAt := time.Now()
	deadline := startedAt.Add(timeout)
	nextProgress := startedAt.Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			if followLog && follower != nil {
				follower.flush(output, true)
			}
			return nil, fmt.Errorf("startup canceled: %w", ctx.Err())
		case err := <-processExit:
			if followLog && follower != nil {
				follower.flush(output, true)
			}
			if err == nil {
				return nil, errors.New("server process exited before health check became ready")
			}
			return nil, fmt.Errorf("server process exited before health check became ready: %w", err)
		default:
		}
		if followLog && follower != nil {
			follower.flush(output, false)
		}
		if value := readHealth(port); value != nil {
			if followLog && follower != nil {
				follower.flush(output, true)
			}
			return value, nil
		}
		if !time.Now().Before(nextProgress) {
			elapsed := time.Since(startedAt).Round(time.Second)
			fmt.Fprintf(output, "[server] still starting after %s; checking dependencies and MCP connections\n", elapsed)
			nextProgress = time.Now().Add(2 * time.Second)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if followLog && follower != nil {
		follower.flush(output, true)
	}
	return nil, fmt.Errorf("health endpoint did not become ready within %s", timeout.Round(time.Second))
}

func (f *startupLogFollower) flush(output io.Writer, final bool) {
	if f == nil || f.path == "" || output == nil {
		return
	}
	file, err := os.Open(f.path)
	if err != nil {
		return
	}
	defer file.Close()
	if _, err := file.Seek(f.offset, io.SeekStart); err != nil {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(file, 256<<10))
	if err != nil || len(raw) == 0 {
		if final && strings.TrimSpace(f.pending) != "" && strings.Contains(f.pending, "[startup]") {
			fmt.Fprintln(output, f.pending)
			f.pending = ""
		}
		return
	}
	f.offset += int64(len(raw))
	text := f.pending + string(raw)
	lines := strings.Split(text, "\n")
	f.pending = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		if strings.Contains(line, "[startup]") {
			fmt.Fprintln(output, line)
		}
	}
	if final && strings.TrimSpace(f.pending) != "" {
		if strings.Contains(f.pending, "[startup]") {
			fmt.Fprintln(output, f.pending)
		}
		f.pending = ""
	}
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (a App) logs() error {
	raw, err := os.ReadFile(config.LogPath())
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(a.Stdout, config.LogPath())
		return nil
	}
	if err != nil {
		return err
	}
	if len(raw) > 200_000 {
		raw = raw[len(raw)-200_000:]
	}
	fmt.Fprintf(a.Stdout, "%s\n%s", config.LogPath(), raw)
	return nil
}

func (a App) setup(cfg config.Config, opts options) error {
	reader := bufio.NewReader(a.Stdin)
	ask := func(label, current string) string {
		fmt.Fprintf(a.Stdout, "%s [%s]: ", label, current)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return current
		}
		return line
	}
	previousMemorySecretEnv := cfg.Memory.SecretEnv
	previousMemorySecret := ""
	if previousMemorySecretEnv != "" {
		previousMemorySecret = os.Getenv(previousMemorySecretEnv)
	}

	if opts.Workspace == "" {
		cfg.Workspace = ask("Workspace", cfg.Workspace)
	}
	if opts.Mode == "" {
		cfg.Mode = ask("Mode (safe/full)", cfg.Mode)
	}
	if opts.Policy == "" {
		cfg.Policy = ask("Policy (strict/balanced/full)", cfg.Policy)
	}
	if opts.Port == 0 {
		value := ask("MCP port", strconv.Itoa(cfg.Port))
		cfg.Port, _ = strconv.Atoi(value)
	}
	if !opts.NoTunnel {
		answer := strings.ToLower(ask("Use ChatGPT Web tunnel? (y/n)", ternary(cfg.NoTunnel, "n", "y")))
		cfg.NoTunnel = answer == "n" || answer == "no"
	}
	if !cfg.NoTunnel {
		if opts.TunnelID == "" {
			cfg.TunnelID = ask("Tunnel ID", cfg.TunnelID)
		}
		if opts.TunnelBin == "" {
			cfg.TunnelBin = ask("tunnel-client path", cfg.TunnelBin)
		}
		if _, err := os.Stat(cfg.TunnelBin); err != nil {
			answer := strings.ToLower(ask("Download tunnel-client now? (y/n)", "y"))
			if answer != "n" && answer != "no" {
				path, downloadErr := downloadTunnelClient(context.Background(), cfg.TunnelBin)
				if downloadErr != nil {
					return fmt.Errorf("download tunnel-client: %w", downloadErr)
				}
				cfg.TunnelBin = path
				fmt.Fprintf(a.Stdout, "Installed tunnel-client: %s\n", path)
			}
		}
		fmt.Fprintf(a.Stdout, "Runtime key is stored separately. Run: codebridge key set\n")
	}

	memoryAnswer := strings.ToLower(ask("Enable memory? (y/n)", ternary(cfg.Memory.Enabled, "y", "n")))
	cfg.Memory.Enabled = memoryAnswer == "y" || memoryAnswer == "yes"
	memorySecret := ""
	clearMemorySecret := false
	if cfg.Memory.Enabled {
		if cfg.Memory.Provider == "" || cfg.Memory.Provider == "none" {
			cfg.Memory.Provider = "agentmemory"
		}
		cfg.Memory.Provider = ask("Memory provider", cfg.Memory.Provider)
		cfg.Memory.Endpoint = ask("Memory endpoint", cfg.Memory.Endpoint)
		cfg.Memory.SecretEnv = ask("Memory secret env", cfg.Memory.SecretEnv)

		existingSecret := ""
		if cfg.Memory.SecretEnv != "" {
			existingSecret = os.Getenv(cfg.Memory.SecretEnv)
		}
		if existingSecret == "" && cfg.Memory.SecretEnv != previousMemorySecretEnv {
			existingSecret = previousMemorySecret
		}
		fmt.Fprint(a.Stdout, "Memory secret")
		if existingSecret != "" {
			fmt.Fprint(a.Stdout, " [configured]")
		}
		fmt.Fprint(a.Stdout, " (Enter keeps, - clears): ")
		line, err := readSecretInput(reader, a.Stdin, a.Stdout)
		if err != nil {
			return fmt.Errorf("read memory secret: %w", err)
		}
		switch memorySecret = line; memorySecret {
		case "-":
			memorySecret = ""
			clearMemorySecret = true
		case "":
			memorySecret = existingSecret
		}

		value := ask("Memory timeout (ms)", strconv.Itoa(cfg.Memory.TimeoutMS))
		timeoutMS, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid memory timeout: %w", err)
		}
		cfg.Memory.TimeoutMS = timeoutMS
		cfg.Memory.CaptureMode = ask("Memory capture (off/metadata/selected)", cfg.Memory.CaptureMode)
		value = ask("Memory token budget", strconv.Itoa(cfg.Memory.TokenBudget))
		tokenBudget, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid memory token budget: %w", err)
		}
		cfg.Memory.TokenBudget = tokenBudget
		cfg.Memory.AgentID = ask("Memory agent ID", cfg.Memory.AgentID)
		optionsJSON := "{}"
		if cfg.Memory.Options != nil {
			if raw, marshalErr := json.Marshal(cfg.Memory.Options); marshalErr == nil {
				optionsJSON = string(raw)
			}
		}
		optionsJSON = ask("Memory provider options JSON", optionsJSON)
		var providerOptions map[string]any
		if err := json.Unmarshal([]byte(optionsJSON), &providerOptions); err != nil {
			return fmt.Errorf("invalid memory provider options JSON: %w", err)
		}
		cfg.Memory.Options = providerOptions
		required := strings.ToLower(ask("Require memory at startup? (y/n)", ternary(cfg.Memory.Required, "y", "n")))
		cfg.Memory.Required = required == "y" || required == "yes"
		cfg.Memory.ProjectStrategy = ask("Memory project strategy (git-origin/path-hash)", cfg.Memory.ProjectStrategy)

		value = ask("Memory observation queue size", strconv.Itoa(cfg.Memory.QueueSize))
		queueSize, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid memory queue size: %w", err)
		}
		cfg.Memory.QueueSize = queueSize
		value = ask("Memory delivery timeout (ms)", strconv.Itoa(cfg.Memory.DeliveryTimeoutMS))
		deliveryTimeout, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid memory delivery timeout: %w", err)
		}
		cfg.Memory.DeliveryTimeoutMS = deliveryTimeout
		value = ask("Memory retry max attempts", strconv.Itoa(cfg.Memory.RetryMaxAttempts))
		retryAttempts, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid memory retry attempts: %w", err)
		}
		cfg.Memory.RetryMaxAttempts = retryAttempts
		value = ask("Memory retry backoff (ms)", strconv.Itoa(cfg.Memory.RetryBackoffMS))
		retryBackoff, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid memory retry backoff: %w", err)
		}
		cfg.Memory.RetryBackoffMS = retryBackoff
		value = ask("Memory health cache (ms)", strconv.Itoa(cfg.Memory.HealthCacheMS))
		healthCache, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid memory health cache: %w", err)
		}
		cfg.Memory.HealthCacheMS = healthCache
	}
	if err := cfg.Validate(true); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	if err := saveMemorySecret(cfg, previousMemorySecretEnv, memorySecret, clearMemorySecret); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Saved config: %s\n", config.ConfigPath())
	if memorySecret != "" || clearMemorySecret {
		fmt.Fprintf(a.Stdout, "Updated memory secret: %s\n", config.DotEnvPath())
	}
	fmt.Fprintln(a.Stdout, "Run: codebridge restart")
	return nil
}

func readSecretInput(reader *bufio.Reader, input io.Reader, output io.Writer) (string, error) {
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		raw, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(output)
		return strings.TrimSpace(string(raw)), err
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func saveMemorySecret(cfg config.Config, previousSecretEnv, secret string, clear bool) error {
	path := config.DotEnvPath()
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	cleaned := config.RemoveDotEnvKeys(string(existing),
		"CODEBRIDGE_MEMORY_ENABLED",
		"CODEBRIDGE_MEMORY_PROVIDER",
		"CODEBRIDGE_MEMORY_ENDPOINT",
		"CODEBRIDGE_MEMORY_SECRET_ENV",
		"CODEBRIDGE_MEMORY_TIMEOUT_MS",
		"CODEBRIDGE_MEMORY_CAPTURE",
		"CODEBRIDGE_MEMORY_TOKEN_BUDGET",
		"CODEBRIDGE_MEMORY_AGENT_ID",
		"CODEBRIDGE_MEMORY_REQUIRED",
		"CODEBRIDGE_MEMORY_PROJECT_STRATEGY",
	)
	if previousSecretEnv != "" && previousSecretEnv != cfg.Memory.SecretEnv {
		cleaned = config.RemoveDotEnvKeys(cleaned, previousSecretEnv)
	}
	if clear && cfg.Memory.SecretEnv != "" {
		cleaned = config.RemoveDotEnvKeys(cleaned, cfg.Memory.SecretEnv)
	}
	if cfg.Memory.SecretEnv != "" && secret != "" {
		cleaned = config.MergeDotEnv(cleaned, map[string]string{cfg.Memory.SecretEnv: secret})
	}
	if cleaned == "" {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return os.Remove(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(cleaned), 0o600)
}

func (a App) installCLI() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	binDir := filepath.Join(home, ".local", "bin")
	name := "codebridge"
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			binDir = filepath.Join(local, "Codebridge", "bin")
		}
		name += ".exe"
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	target := filepath.Join(binDir, name)
	source, err := os.Open(executable)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Installed %s\n", target)
	return nil
}

func writeTunnelProfile(cfg config.Config) (string, error) {
	if cfg.TunnelID == "" {
		return "", errors.New("missing tunnel ID")
	}
	if err := os.MkdirAll(cfg.ProfileDir, 0o700); err != nil {
		return "", err
	}
	name := cfg.Profile
	if filepath.Ext(name) == "" {
		name += ".yaml"
	}
	path := filepath.Join(cfg.ProfileDir, name)
	lines := []string{
		"config_version: 1", "control_plane:", `  base_url: "https://api.openai.com"`,
		fmt.Sprintf(`  tunnel_id: "%s"`, yamlEscape(cfg.TunnelID)),
		`  api_key: "env:CONTROL_PLANE_API_KEY"`,
	}
	if cfg.Organization != "" {
		lines = append(lines, "  extra_headers:", fmt.Sprintf(`    - "OpenAI-Organization: %s"`, yamlEscape(cfg.Organization)))
	}
	lines = append(lines, "log:", "  level: info", "  format: json", "mcp:", "  server_urls:",
		"    - channel: main", fmt.Sprintf(`      url: "http://127.0.0.1:%d/mcp"`, cfg.Port))
	return path, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func yamlEscape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), `\`, `\\`), `"`, `\"`)
}

func readHealth(port int) map[string]any {
	for _, path := range []string{"/internal/healthz", "/healthz"} {
		if value := readHealthPath(port, path); value != nil {
			return value
		}
	}
	return nil
}

func readDeepHealth(port int) map[string]any {
	return readHealthPathWithTimeout(port, "/internal/healthz?deep=1", 12*time.Second)
}

func readHealthPath(port int, path string) map[string]any {
	return readHealthPathWithTimeout(port, path, 1200*time.Millisecond)
}

func readHealthPathWithTimeout(port int, path string, timeout time.Duration) map[string]any {
	client := http.Client{Timeout: timeout}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var value map[string]any
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value) != nil {
		return nil
	}
	if value["status"] != "ok" {
		return nil
	}
	if path == "/healthz" && value["pid"] == nil && value["config_id"] == nil {
		return nil
	}
	return value
}

func healthValue(health map[string]any, key string) any {
	if health == nil {
		return nil
	}
	return health[key]
}

func portAvailable(host string, port int) bool {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

func waitForServerRelease(host string, port, pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) && portAvailable(host, port) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !pidAlive(pid) && portAvailable(host, port)
}

func waitForHealth(port int, timeout time.Duration) map[string]any {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if value := readHealth(port); value != nil {
			return value
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}

func readState() processState {
	var state processState
	raw, err := os.ReadFile(config.PIDPath())
	if err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	return state
}

func writeState(state processState) error {
	if err := os.MkdirAll(filepath.Dir(config.PIDPath()), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(state, "", "  ")
	return os.WriteFile(config.PIDPath(), append(raw, '\n'), 0o600)
}

func numberValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		number, _ := typed.Int64()
		return int(number)
	default:
		return 0
	}
}

func mustJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func detectWorkspace(cwd string) string {
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel")
	if raw, err := cmd.Output(); err == nil && strings.TrimSpace(string(raw)) != "" {
		return strings.TrimSpace(string(raw))
	}
	absolute, _ := filepath.Abs(cwd)
	return absolute
}
