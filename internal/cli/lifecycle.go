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
	"codebridge/internal/figma"
)

type processState struct {
	ServerPID int       `json:"serverPid,omitempty"`
	TunnelPID int       `json:"tunnelPid,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	ConfigID  string    `json:"configId"`
	Port      int       `json:"port"`
	Workspace string    `json:"workspace"`
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
	health := readHealth(cfg.Port)
	if health != nil && fmt.Sprint(health["config_id"]) != configID {
		if pid := numberValue(health["pid"]); pid > 0 {
			fmt.Fprintf(a.Stdout, "[server] config changed; stopping PID %d\n", pid)
			_ = stopPID(pid)
			time.Sleep(700 * time.Millisecond)
		}
		health = nil
	}
	state := readState()
	var serverCmd *exec.Cmd
	if health == nil {
		serverCmd, err = a.spawnServer(executable, cfg, opts.Background)
		if err != nil {
			return err
		}
		health = waitForHealth(cfg.Port, 10*time.Second)
		if health == nil {
			_ = stopPID(serverCmd.Process.Pid)
			return fmt.Errorf("MCP server did not respond at http://127.0.0.1:%d/healthz; see %s", cfg.Port, config.LogPath())
		}
		state.ServerPID = numberValue(health["pid"])
	} else {
		state.ServerPID = numberValue(health["pid"])
	}
	fmt.Fprintf(a.Stdout, "[server] MCP OK: http://127.0.0.1:%d/mcp\n", cfg.Port)

	var tunnelCmd *exec.Cmd
	if !cfg.NoTunnel {
		tunnelCmd, err = a.spawnTunnel(cfg, opts, opts.Background)
		if err != nil {
			if serverCmd != nil {
				_ = stopPID(serverCmd.Process.Pid)
			}
			return err
		}
		state.TunnelPID = tunnelCmd.Process.Pid
	} else {
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
		go func() { wait <- serverCmd.Wait() }()
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
		"FIGMA_DESKTOP_MCP_URL="+cfg.FigmaDesktopURL,
		"FIGMA_DESKTOP_TIMEOUT_MS="+strconv.Itoa(cfg.FigmaDesktopTimeoutMS),
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
	_ = logFile.Close()
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
	checks = append(checks, check{"server", readHealth(cfg.Port) != nil, fmt.Sprintf("http://127.0.0.1:%d/healthz", cfg.Port)})
	if !cfg.NoTunnel {
		_, tunnelErr := os.Stat(cfg.TunnelBin)
		checks = append(checks, check{"tunnel-client", tunnelErr == nil, cfg.TunnelBin})
		checks = append(checks, check{"tunnel-id", cfg.TunnelID != "", ternary(cfg.TunnelID != "", "configured", "missing")})
		checks = append(checks, check{"runtime-key", os.Getenv(cfg.RuntimeKeyEnv) != "", cfg.RuntimeKeyEnv})
	}
	figmaStatus := figma.Client{Endpoint: cfg.FigmaDesktopURL, Timeout: 2 * time.Second, AllowRemote: cfg.FigmaDesktopAllowRemote, Version: a.Version}.Status(ctx)
	checks = append(checks, check{"figma-desktop", figmaStatus["connected"] == true, fmt.Sprint(figmaStatus["endpoint"])})
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
	if err := cfg.Validate(true); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Saved %s\nRun: codebridge\n", config.ConfigPath())
	return nil
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
	client := http.Client{Timeout: 1200 * time.Millisecond}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
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
	return value
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
