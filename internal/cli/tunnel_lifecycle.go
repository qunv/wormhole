// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"codebridge/internal/config"
)

func migrateTunnelProcessState(state *processState) {
	if state.Tunnels == nil {
		state.Tunnels = map[string]tunnelProcessState{}
	}
	if state.TunnelPID > 0 {
		if _, exists := state.Tunnels["default"]; !exists {
			state.Tunnels["default"] = tunnelProcessState{PID: state.TunnelPID, Identity: state.TunnelIdentity}
		}
	}
	state.TunnelPID = 0
	state.TunnelIdentity = ""
}

func sortedBoolKeys(items map[string]bool) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func tunnelNames[T any](items map[string]T) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func tunnelLabel(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "default" {
		return "tunnel"
	}
	return "tunnel-" + name
}

func ownedNamedTunnelProcess(name string, process tunnelProcessState, stateOwned bool) (int, string, bool) {
	if process.PID <= 0 {
		return 0, "", false
	}
	if process.Identity != "" {
		if processMatches(process.PID, process.Identity) {
			return process.PID, process.Identity, true
		}
		if !stateOwned || !processLooksLikeCodebridgeChild(process.PID, tunnelLabel(name)) {
			return 0, "", false
		}
		identity, err := captureChildProcessIdentity(process.PID, tunnelLabel(name), 500*time.Millisecond)
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

func desiredTunnelMap(cfg config.Config) map[string]config.NamedTunnel {
	desired := map[string]config.NamedTunnel{}
	if cfg.NoTunnel {
		return desired
	}
	for _, tunnel := range cfg.EnabledTunnels() {
		desired[tunnel.Name] = tunnel
	}
	return desired
}

func runtimeKeyForTunnel(tunnel config.NamedTunnel, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	return strings.TrimSpace(os.Getenv(tunnel.Config.RuntimeKeyEnv))
}

func runtimeKeyIdentityMaterial(cfg config.Config, override string) string {
	parts := make([]string, 0)
	for _, tunnel := range cfg.EnabledTunnels() {
		parts = append(parts, tunnel.Name+"\x00"+tunnel.Config.RuntimeKeyEnv+"\x00"+runtimeKeyForTunnel(tunnel, override))
	}
	return strings.Join(parts, "\x01")
}

type tunnelOwnership struct {
	PID      int
	Identity string
	Owned    bool
}

func inspectTunnelOwnership(state *processState, stateOwned bool) (map[string]tunnelOwnership, error) {
	result := make(map[string]tunnelOwnership, len(state.Tunnels))
	for _, name := range tunnelNames(state.Tunnels) {
		process := state.Tunnels[name]
		pid, identity, owned := ownedNamedTunnelProcess(name, process, stateOwned)
		if process.PID > 0 && !owned && pidAlive(process.PID) {
			return nil, fmt.Errorf("tunnel %q PID %d is alive but is not owned by the current Codebridge process state", name, process.PID)
		}
		result[name] = tunnelOwnership{PID: pid, Identity: identity, Owned: owned}
	}
	return result, nil
}

func (a App) reconcileTunnels(
	cfg config.Config,
	opts options,
	state *processState,
	configID string,
	stateOwned bool,
) (map[string]*exec.Cmd, error) {
	migrateTunnelProcessState(state)
	desired := desiredTunnelMap(cfg)
	started := map[string]*exec.Cmd{}
	ownership, err := inspectTunnelOwnership(state, stateOwned)
	if err != nil {
		return nil, fmt.Errorf("refusing to reconcile tunnels: %w", err)
	}

	for _, name := range tunnelNames(state.Tunnels) {
		process := state.Tunnels[name]
		current := ownership[name]
		pid, identity, owned := current.PID, current.Identity, current.Owned
		_, wanted := desired[name]
		if wanted && owned && state.ConfigID == configID {
			state.Tunnels[name] = tunnelProcessState{PID: pid, Identity: identity}
			fmt.Fprintf(a.Stdout, "[tunnel:%s] reusing PID %d\n", name, pid)
			continue
		}
		if owned {
			fmt.Fprintf(a.Stdout, "[tunnel:%s] stopping PID %d\n", name, pid)
			if err := stopPID(pid); err != nil && processStillMatches(pid, identity) {
				return nil, fmt.Errorf("stop existing tunnel %q PID %d: %w", name, pid, err)
			}
			if !waitForProcessExit(pid, identity, 5*time.Second) {
				return nil, fmt.Errorf("existing tunnel %q PID %d did not exit", name, pid)
			}
		} else if process.PID > 0 {
			fmt.Fprintf(a.Stdout, "[tunnel:%s] ignored stale PID %d because it is no longer alive\n", name, process.PID)
		}
		delete(state.Tunnels, name)
	}

	for _, name := range tunnelNames(desired) {
		if _, exists := state.Tunnels[name]; exists {
			continue
		}
		tunnel := desired[name]
		key := runtimeKeyForTunnel(tunnel, opts.RuntimeKey)
		cmd, err := a.spawnTunnel(cfg, tunnel, key, opts.Background)
		if err != nil {
			_ = cleanupTunnelCommands(state, started)
			return nil, err
		}
		identity := ""
		if opts.Background {
			identity, err = captureChildProcessIdentity(cmd.Process.Pid, tunnelLabel(name), 2*time.Second)
		} else {
			identity, err = processIdentity(cmd.Process.Pid)
		}
		if err != nil {
			_ = stopPID(cmd.Process.Pid)
			_ = cleanupTunnelCommands(state, started)
			return nil, fmt.Errorf("capture tunnel %q process identity for PID %d: %w", name, cmd.Process.Pid, err)
		}
		state.Tunnels[name] = tunnelProcessState{PID: cmd.Process.Pid, Identity: identity}
		started[name] = cmd
	}
	return started, nil
}

func cleanupTunnelCommands(state *processState, commands map[string]*exec.Cmd) error {
	var errs []error
	for _, name := range tunnelNames(commands) {
		cmd := commands[name]
		process := state.Tunnels[name]
		if processStillMatches(process.PID, process.Identity) {
			if err := stopPID(cmd.Process.Pid); err != nil && processStillMatches(process.PID, process.Identity) {
				errs = append(errs, fmt.Errorf("stop started tunnel %q PID %d: %w", name, cmd.Process.Pid, err))
			}
		}
		if waitForProcessExit(process.PID, process.Identity, 5*time.Second) {
			delete(state.Tunnels, name)
		} else {
			errs = append(errs, fmt.Errorf("started tunnel %q PID %d did not exit", name, process.PID))
		}
	}
	return errors.Join(errs...)
}

func (a App) stopAllTunnels(state *processState, stateOwned bool) error {
	migrateTunnelProcessState(state)
	ownership, err := inspectTunnelOwnership(state, stateOwned)
	if err != nil {
		return fmt.Errorf("refusing to stop tunnels: %w", err)
	}
	var errs []error
	for _, name := range tunnelNames(state.Tunnels) {
		process := state.Tunnels[name]
		current := ownership[name]
		pid, identity, owned := current.PID, current.Identity, current.Owned
		if owned {
			fmt.Fprintf(a.Stdout, "[tunnel:%s] stopping PID %d\n", name, pid)
			if err := stopPID(pid); err != nil && processStillMatches(pid, identity) {
				errs = append(errs, fmt.Errorf("stop tunnel %q PID %d: %w", name, pid, err))
				continue
			}
			if !waitForProcessExit(pid, identity, 5*time.Second) {
				errs = append(errs, fmt.Errorf("tunnel %q PID %d did not exit", name, pid))
				continue
			}
		} else if process.PID > 0 {
			fmt.Fprintf(a.Stdout, "[tunnel:%s] ignored stale PID %d because it is no longer alive\n", name, process.PID)
		}
		delete(state.Tunnels, name)
	}
	return errors.Join(errs...)
}

func tunnelPIDMap(items map[string]tunnelProcessState) map[string]int {
	out := make(map[string]int, len(items))
	for name, process := range items {
		out[name] = process.PID
	}
	return out
}

func tunnelStateEmpty(state processState) bool {
	return len(state.Tunnels) == 0 && state.TunnelPID == 0
}
