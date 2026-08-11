// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"wormhole/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func remoteIngressNames[T any](items map[string]T) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func remoteIngressLabel(name string) string {
	return "remote-ingress-" + strings.ToLower(strings.TrimSpace(name))
}

func ownedRemoteIngressProcess(name string, process tunnelProcessState, stateOwned bool) (int, string, bool) {
	if process.PID <= 0 {
		return 0, "", false
	}
	if process.Identity != "" {
		if processMatches(process.PID, process.Identity) {
			return process.PID, process.Identity, true
		}
		if !stateOwned || !processLooksLikeWormholeChild(process.PID, remoteIngressLabel(name)) {
			return 0, "", false
		}
		identity, err := captureChildProcessIdentity(process.PID, remoteIngressLabel(name), 500*time.Millisecond)
		if err != nil {
			return 0, "", false
		}
		return process.PID, identity, true
	}
	if !stateOwned {
		return 0, "", false
	}
	identity, err := processIdentity(process.PID)
	if err != nil {
		return 0, "", false
	}
	return process.PID, identity, true
}

func desiredRemoteIngressMap(cfg config.Config) map[string]config.NamedRemoteIngress {
	desired := map[string]config.NamedRemoteIngress{}
	for _, ingress := range cfg.EnabledRemoteIngresses() {
		if ingress.Config.Provider == "cloudflare" {
			desired[ingress.Name] = ingress
		}
	}
	return desired
}

func inspectRemoteIngressOwnership(state *processState, stateOwned bool) (map[string]tunnelOwnership, error) {
	if state.RemoteIngresses == nil {
		state.RemoteIngresses = map[string]tunnelProcessState{}
	}
	result := make(map[string]tunnelOwnership, len(state.RemoteIngresses))
	for _, name := range remoteIngressNames(state.RemoteIngresses) {
		process := state.RemoteIngresses[name]
		pid, identity, owned := ownedRemoteIngressProcess(name, process, stateOwned)
		if process.PID > 0 && !owned && pidAlive(process.PID) {
			return nil, fmt.Errorf("remote ingress %q PID %d is alive but is not owned by the current Wormhole process state", name, process.PID)
		}
		result[name] = tunnelOwnership{PID: pid, Identity: identity, Owned: owned}
	}
	return result, nil
}

func (a App) reconcileRemoteIngresses(
	cfg config.Config,
	state *processState,
	configID string,
	stateOwned bool,
	background bool,
) (map[string]*exec.Cmd, error) {
	desired := desiredRemoteIngressMap(cfg)
	started := map[string]*exec.Cmd{}
	ownership, err := inspectRemoteIngressOwnership(state, stateOwned)
	if err != nil {
		return nil, fmt.Errorf("refusing to reconcile remote ingresses: %w", err)
	}

	for _, name := range remoteIngressNames(state.RemoteIngresses) {
		process := state.RemoteIngresses[name]
		current := ownership[name]
		pid, identity, owned := current.PID, current.Identity, current.Owned
		_, wanted := desired[name]
		if wanted && owned && state.ConfigID == configID {
			state.RemoteIngresses[name] = tunnelProcessState{PID: pid, Identity: identity}
			fmt.Fprintf(a.Stdout, "[remote-ingress:%s] reusing PID %d\n", name, pid)
			continue
		}
		if owned {
			fmt.Fprintf(a.Stdout, "[remote-ingress:%s] stopping PID %d\n", name, pid)
			if err := stopPID(pid); err != nil && processStillMatches(pid, identity) {
				return nil, fmt.Errorf("stop existing remote ingress %q PID %d: %w", name, pid, err)
			}
			if !waitForProcessExit(pid, identity, 5*time.Second) {
				return nil, fmt.Errorf("existing remote ingress %q PID %d did not exit", name, pid)
			}
		} else if process.PID > 0 {
			fmt.Fprintf(a.Stdout, "[remote-ingress:%s] ignored stale PID %d because it is no longer alive\n", name, process.PID)
		}
		delete(state.RemoteIngresses, name)
	}

	for _, name := range remoteIngressNames(desired) {
		if _, exists := state.RemoteIngresses[name]; exists {
			continue
		}
		cmd, err := a.spawnRemoteIngress(desired[name], background)
		if err != nil {
			_ = cleanupRemoteIngressCommands(state, started)
			return nil, err
		}
		identity := ""
		if background {
			identity, err = captureChildProcessIdentity(cmd.Process.Pid, remoteIngressLabel(name), 2*time.Second)
		} else {
			identity, err = processIdentity(cmd.Process.Pid)
		}
		if err != nil {
			_ = stopPID(cmd.Process.Pid)
			_ = cleanupRemoteIngressCommands(state, started)
			return nil, fmt.Errorf("capture remote ingress %q process identity for PID %d: %w", name, cmd.Process.Pid, err)
		}
		state.RemoteIngresses[name] = tunnelProcessState{PID: cmd.Process.Pid, Identity: identity}
		started[name] = cmd
	}
	return started, nil
}

func (a App) spawnRemoteIngress(ingress config.NamedRemoteIngress, background bool) (*exec.Cmd, error) {
	cmd, err := remoteIngressCommand(ingress)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(a.Stdout, "[remote-ingress:%s] local MCP: http://127.0.0.1:%d/mcp\n", ingress.Name, ingress.Config.LocalPort)
	if ingress.Config.PublicURL != "" {
		fmt.Fprintf(a.Stdout, "[remote-ingress:%s] public MCP: %s\n", ingress.Name, ingress.Config.PublicURL)
	}
	return a.startChild(remoteIngressLabel(ingress.Name), cmd, background)
}

func remoteIngressCommand(ingress config.NamedRemoteIngress) (*exec.Cmd, error) {
	cfg := ingress.Config
	if cfg.Provider == "external" {
		return nil, fmt.Errorf("remote ingress %q uses external provider and has no managed child process", ingress.Name)
	}
	providerToken := strings.TrimSpace(os.Getenv(cfg.ProviderTokenEnv))
	if providerToken == "" {
		return nil, fmt.Errorf("missing provider token for remote ingress %q; set %s from the Admin Secrets page or environment", ingress.Name, cfg.ProviderTokenEnv)
	}
	if token, _, _ := remoteIngressAuthToken(cfg); token == "" {
		if cfg.AuthTokenFallbackEnv == "" {
			return nil, fmt.Errorf("missing MCP bearer token for remote ingress %q; set %s from the Admin Secrets page or environment", ingress.Name, cfg.AuthTokenEnv)
		}
		return nil, fmt.Errorf("missing MCP bearer token for remote ingress %q; set %s or %s from the Admin Secrets page or environment", ingress.Name, cfg.AuthTokenEnv, cfg.AuthTokenFallbackEnv)
	}
	binary := cfg.Binary
	if filepath.IsAbs(binary) {
		if _, err := os.Stat(binary); err != nil {
			return nil, fmt.Errorf("remote ingress %q binary not found at %s", ingress.Name, binary)
		}
	} else {
		resolved, err := exec.LookPath(binary)
		if err != nil {
			return nil, fmt.Errorf("remote ingress %q requires %s in PATH or remoteIngresses.%s.binary", ingress.Name, binary, ingress.Name)
		}
		binary = resolved
	}
	if cfg.Provider != "cloudflare" {
		return nil, fmt.Errorf("unsupported remote ingress provider %q", cfg.Provider)
	}
	cmd := exec.Command(binary, "tunnel", "--no-autoupdate", "run")
	cmd.Dir = filepath.Dir(binary)
	cmd.Env = append(os.Environ(), "TUNNEL_TOKEN="+providerToken)
	return cmd, nil
}

func cleanupRemoteIngressCommands(state *processState, commands map[string]*exec.Cmd) error {
	var errs []error
	for _, name := range remoteIngressNames(commands) {
		cmd := commands[name]
		process := state.RemoteIngresses[name]
		if processStillMatches(process.PID, process.Identity) {
			if err := stopPID(cmd.Process.Pid); err != nil && processStillMatches(process.PID, process.Identity) {
				errs = append(errs, fmt.Errorf("stop started remote ingress %q PID %d: %w", name, cmd.Process.Pid, err))
			}
		}
		if waitForProcessExit(process.PID, process.Identity, 5*time.Second) {
			delete(state.RemoteIngresses, name)
		} else {
			errs = append(errs, fmt.Errorf("started remote ingress %q PID %d did not exit", name, process.PID))
		}
	}
	return errors.Join(errs...)
}

func (a App) stopAllRemoteIngresses(state *processState, stateOwned bool) error {
	ownership, err := inspectRemoteIngressOwnership(state, stateOwned)
	if err != nil {
		return fmt.Errorf("refusing to stop remote ingresses: %w", err)
	}
	var errs []error
	for _, name := range remoteIngressNames(state.RemoteIngresses) {
		process := state.RemoteIngresses[name]
		current := ownership[name]
		pid, identity, owned := current.PID, current.Identity, current.Owned
		if owned {
			fmt.Fprintf(a.Stdout, "[remote-ingress:%s] stopping PID %d\n", name, pid)
			if err := stopPID(pid); err != nil && processStillMatches(pid, identity) {
				errs = append(errs, fmt.Errorf("stop remote ingress %q PID %d: %w", name, pid, err))
				continue
			}
			if !waitForProcessExit(pid, identity, 5*time.Second) {
				errs = append(errs, fmt.Errorf("remote ingress %q PID %d did not exit", name, pid))
				continue
			}
		} else if process.PID > 0 {
			fmt.Fprintf(a.Stdout, "[remote-ingress:%s] ignored stale PID %d because it is no longer alive\n", name, process.PID)
		}
		delete(state.RemoteIngresses, name)
	}
	return errors.Join(errs...)
}

func remoteIngressPIDMap(items map[string]tunnelProcessState) map[string]int {
	out := make(map[string]int, len(items))
	for name, process := range items {
		out[name] = process.PID
	}
	return out
}

func remoteIngressStateEmpty(state processState) bool { return len(state.RemoteIngresses) == 0 }

func remoteIngressPortReachable(port int) bool {
	connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

type remoteIngressProbeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	ToolCount       int    `json:"toolCount"`
	ContractHash    string `json:"contractHash"`
}

type remoteIngressProbeTransport struct {
	token string
}

func (t remoteIngressProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(clone)
}

func remoteProbeHTTPClient(token string) *http.Client {
	return &http.Client{
		Transport: remoteIngressProbeTransport{token: token},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// probeRemoteIngress proves that the dedicated listener is more than a live
// TCP socket: it authenticates, negotiates MCP, and retrieves the configured
// tool catalog. It intentionally probes the loopback listener rather than the public
// publisher so doctor remains deterministic and does not create Internet I/O.
func remoteIngressAuthToken(cfg config.RemoteIngressConfig) (string, bool, bool) {
	primary := strings.TrimSpace(os.Getenv(cfg.AuthTokenEnv))
	fallback := ""
	if cfg.AuthTokenFallbackEnv != "" {
		fallback = strings.TrimSpace(os.Getenv(cfg.AuthTokenFallbackEnv))
	}
	if fallback != "" {
		return fallback, primary != "", true
	}
	return primary, primary != "", false
}

func probeRemoteIngress(ctx context.Context, ingress config.NamedRemoteIngress) (remoteIngressProbeResult, error) {
	token, _, _ := remoteIngressAuthToken(ingress.Config)
	if token == "" {
		if ingress.Config.AuthTokenFallbackEnv == "" {
			return remoteIngressProbeResult{}, fmt.Errorf("missing MCP bearer token in %s", ingress.Config.AuthTokenEnv)
		}
		return remoteIngressProbeResult{}, fmt.Errorf("missing MCP bearer token in %s or %s", ingress.Config.AuthTokenEnv, ingress.Config.AuthTokenFallbackEnv)
	}
	return probeRemoteMCPURL(ctx, fmt.Sprintf("http://127.0.0.1:%d/mcp", ingress.Config.LocalPort), token)
}

func probeRemoteMCPURL(ctx context.Context, endpoint, token string) (remoteIngressProbeResult, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "wormhole-remote-verify", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
		HTTPClient:           remoteProbeHTTPClient(token),
	}, nil)
	if err != nil {
		return remoteIngressProbeResult{}, err
	}
	defer session.Close()
	tools, err := listRemoteProbeTools(ctx, session, 256)
	if err != nil {
		return remoteIngressProbeResult{}, err
	}
	protocol := "unknown"
	if initialized := session.InitializeResult(); initialized != nil && strings.TrimSpace(initialized.ProtocolVersion) != "" {
		protocol = initialized.ProtocolVersion
	}
	hash, err := remoteToolContractHash(tools)
	if err != nil {
		return remoteIngressProbeResult{}, err
	}
	return remoteIngressProbeResult{ProtocolVersion: protocol, ToolCount: len(tools), ContractHash: hash}, nil
}

func listRemoteProbeTools(ctx context.Context, session *mcp.ClientSession, limit int) ([]*mcp.Tool, error) {
	if limit <= 0 {
		limit = 1
	}
	var tools []*mcp.Tool
	cursor := ""
	seen := map[string]bool{}
	for {
		result, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		tools = append(tools, result.Tools...)
		if len(tools) > limit {
			return nil, fmt.Errorf("remote MCP tool catalog exceeds verification limit %d", limit)
		}
		if result.NextCursor == "" {
			break
		}
		if seen[result.NextCursor] {
			return nil, errors.New("remote MCP tool catalog repeated a pagination cursor")
		}
		seen[result.NextCursor] = true
		cursor = result.NextCursor
	}
	return tools, nil
}

func remoteToolContractHash(tools []*mcp.Tool) (string, error) {
	ordered := append([]*mcp.Tool(nil), tools...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i] == nil {
			return ordered[j] != nil
		}
		if ordered[j] == nil {
			return false
		}
		return ordered[i].Name < ordered[j].Name
	})
	raw, err := json.Marshal(ordered)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8]), nil
}
