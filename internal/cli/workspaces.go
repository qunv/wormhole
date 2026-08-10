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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"wormhole/internal/config"
	"wormhole/internal/workspaceregistry"
)

type namedWorkspaceConfig struct {
	Registration workspaceregistry.Registration
	Config       config.Config
}

func (a App) workspaceCommand(ctx context.Context, defaultConfig config.Config, opts options) error {
	if len(opts.Rest) == 0 {
		return a.workspace(defaultConfig, opts)
	}

	switch opts.Rest[0] {
	case "add":
		return a.workspaceAdd(defaultConfig, opts)
	case "list":
		return a.workspaceList(defaultConfig, opts)
	case "start", "stop", "status":
		if len(opts.Rest) < 2 {
			return fmt.Errorf("usage: wormhole workspace %s <id>", opts.Rest[0])
		}
		return a.workspaceLifecycle(ctx, defaultConfig, opts.Rest[0], opts.Rest[1], opts)
	case "remove":
		if len(opts.Rest) < 2 {
			return errors.New("usage: wormhole workspace remove <id>")
		}
		return a.workspaceRemove(defaultConfig, opts.Rest[1], opts)
	case "compact":
		if len(opts.Rest) < 2 {
			return errors.New("usage: wormhole workspace compact <id> [--dry-run]")
		}
		return a.workspaceCompact(defaultConfig, opts.Rest[1], opts)
	default:
		return a.workspace(defaultConfig, opts)
	}
}

func (a App) workspaceAdd(defaultConfig config.Config, opts options) error {
	if len(opts.Rest) < 3 {
		return errors.New("usage: wormhole workspace add <id> <path> [--force]")
	}
	entry, _, err := a.registerWorkspace(defaultConfig, opts.Rest[1], opts.Rest[2], opts, opts.Force)
	if err != nil {
		return err
	}

	fmt.Fprintf(a.Stdout, "Workspace %s added\n", entry.ID)
	fmt.Fprintf(a.Stdout, "  root:     %s\n", entry.Workspace)
	fmt.Fprintf(a.Stdout, "  endpoint: http://127.0.0.1:%d%s\n", defaultConfig.Port, workspaceEndpoint(entry.ID))
	fmt.Fprintf(a.Stdout, "  config:   %s\n", entry.ConfigPath)
	if opts.Port != 0 && opts.Port != defaultConfig.Port {
		fmt.Fprintf(a.Stdout, "  note:     --port %d ignored; named workspaces share daemon port %d\n", opts.Port, defaultConfig.Port)
	}
	if opts.TunnelID != "" {
		fmt.Fprintln(a.Stdout, "  note:     --tunnel-id ignored; named endpoints use the daemon tunnel")
	}
	if opts.NoTunnel {
		fmt.Fprintln(a.Stdout, "  note:     --no-tunnel ignored; named endpoints follow the daemon tunnel setting")
	}
	if opts.Profile != "" {
		fmt.Fprintln(a.Stdout, "  note:     --profile ignored; the shared daemon owns one tunnel profile")
	}
	fmt.Fprintln(a.Stdout, "Restart Wormhole to activate the endpoint, or run: wormhole workspace start "+entry.ID)
	return nil
}

func (a App) registerWorkspace(defaultConfig config.Config, rawID, rawRoot string, opts options, replace bool) (workspaceregistry.Registration, bool, error) {
	var entry workspaceregistry.Registration
	var created bool
	err := withLifecycleLock(context.Background(), "workspace register", func() error {
		var err error
		entry, created, err = a.registerWorkspaceUnlocked(defaultConfig, rawID, rawRoot, opts, replace)
		return err
	})
	return entry, created, err
}

func (a App) registerWorkspaceUnlocked(defaultConfig config.Config, rawID, rawRoot string, opts options, replace bool) (workspaceregistry.Registration, bool, error) {
	id := workspaceregistry.NormalizeID(rawID)
	if err := workspaceregistry.ValidateID(id); err != nil {
		return workspaceregistry.Registration{}, false, err
	}
	root, err := filepath.Abs(rawRoot)
	if err != nil {
		return workspaceregistry.Registration{}, false, err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return workspaceregistry.Registration{}, false, fmt.Errorf("workspace does not exist: %s", root)
	}
	root = detectWorkspace(root)

	registry, err := workspaceregistry.Load()
	if err != nil {
		return workspaceregistry.Registration{}, false, err
	}
	previous, exists := registry.Workspaces[id]
	if exists && !replace {
		return workspaceregistry.Registration{}, false, fmt.Errorf("workspace %q already exists; pass --force to replace it", id)
	}

	now := time.Now().UTC()
	createdAt := now
	if exists && !previous.CreatedAt.IsZero() {
		createdAt = previous.CreatedAt
	}
	configPath := workspaceregistry.ConfigPath(id)
	dataDir := workspaceregistry.DataDir(id)
	if exists {
		if previous.ConfigPath != "" {
			configPath = previous.ConfigPath
		}
		if previous.DataDir != "" {
			dataDir = previous.DataDir
		}
	}
	entry := workspaceregistry.Registration{
		ID: id, Workspace: root,
		ConfigPath: configPath, DataDir: dataDir,
		Port: defaultConfig.Port, Enabled: true,
		CreatedAt: createdAt, UpdatedAt: now,
	}

	override := map[string]any{}
	if exists {
		override, err = config.ReadOverrideFile(previous.ConfigPath)
		if err != nil {
			return workspaceregistry.Registration{}, false, fmt.Errorf("load workspace %q override: %w", id, err)
		}
	}
	// Registry identity and listener/tunnel security belong to the shared
	// daemon. Remove legacy snapshot fields whenever a workspace registration is
	// refreshed so the persisted document converges toward a minimal override.
	for _, field := range []string{
		"workspace", "port", "host", "authToken", "approvalToken", "allowedOrigins",
		"noTunnel", "tunnelBin", "tunnelId", "organizationId", "profile", "profileDir", "runtimeKeyEnv", "remoteIngresses",
	} {
		delete(override, field)
	}
	// Extra roots are workspace-specific. A newly registered workspace starts
	// with none even when the primary workspace has global extra roots.
	if len(opts.ExtraRoots) > 0 || !exists || opts.Force {
		override["extraRoots"] = append([]string(nil), opts.ExtraRoots...)
	}
	if opts.Mode != "" {
		override["mode"] = opts.Mode
	}
	if opts.Policy != "" {
		override["policy"] = opts.Policy
	}
	configSnapshot, err := captureWorkspaceFile(entry.ConfigPath)
	if err != nil {
		return workspaceregistry.Registration{}, false, fmt.Errorf("snapshot workspace %q override: %w", id, err)
	}
	if err := saveWorkspaceOverride(entry.ConfigPath, defaultConfig, override); err != nil {
		return workspaceregistry.Registration{}, false, err
	}
	registry.Workspaces[id] = entry
	if err := saveWorkspaceRegistry(registry); err != nil {
		rollbackErr := configSnapshot.restore()
		return workspaceregistry.Registration{}, false, errors.Join(
			fmt.Errorf("save workspace registry: %w", err),
			wrapOptionalError("rollback workspace override", rollbackErr),
		)
	}
	return entry, !exists, nil
}

func (a App) ensureAutoWorkspace(defaultConfig config.Config, root string, opts options) (workspaceregistry.Registration, bool, bool, error) {
	var entry workspaceregistry.Registration
	var created, enabled bool
	err := withLifecycleLock(context.Background(), "workspace auto-register", func() error {
		var err error
		entry, created, enabled, err = a.ensureAutoWorkspaceUnlocked(defaultConfig, root, opts)
		return err
	})
	return entry, created, enabled, err
}

func (a App) ensureAutoWorkspaceUnlocked(defaultConfig config.Config, root string, opts options) (workspaceregistry.Registration, bool, bool, error) {
	root = detectWorkspace(root)
	if sameWorkspacePath(root, defaultConfig.Workspace) {
		return workspaceregistry.Registration{ID: workspaceregistry.IDFromPath(root), Workspace: root, Enabled: true}, false, false, nil
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		return workspaceregistry.Registration{}, false, false, err
	}
	for _, id := range workspaceregistry.SortedIDs(registry) {
		entry := registry.Workspaces[id]
		if !sameWorkspacePath(entry.Workspace, root) {
			continue
		}
		wasEnabled := entry.Enabled
		updated, _, registerErr := a.registerWorkspaceUnlocked(defaultConfig, id, root, opts, true)
		return updated, false, !wasEnabled, registerErr
	}

	id := autoWorkspaceID(registry, root)
	entry, created, err := a.registerWorkspaceUnlocked(defaultConfig, id, root, opts, false)
	return entry, created, created, err
}

func autoWorkspaceID(registry workspaceregistry.Registry, root string) string {
	base := workspaceregistry.IDFromPath(root)
	if _, exists := registry.Workspaces[base]; !exists && base != "default" {
		return base
	}
	sum := sha256.Sum256([]byte(canonicalWorkspacePath(root)))
	for _, hashLength := range []int{8, 12, 16, 24} {
		suffix := "-" + hex.EncodeToString(sum[:])[:hashLength]
		prefixLength := 32 - len(suffix)
		prefix := base
		if len(prefix) > prefixLength {
			prefix = strings.TrimRight(prefix[:prefixLength], "-")
		}
		if prefix == "" || prefix == "default" {
			prefix = "workspace"
			if len(prefix) > prefixLength {
				prefix = prefix[:prefixLength]
			}
		}
		candidate := prefix + suffix
		if _, exists := registry.Workspaces[candidate]; !exists {
			return candidate
		}
	}
	return "workspace-" + hex.EncodeToString(sum[:])[:22]
}

func sameWorkspacePath(left, right string) bool {
	return canonicalWorkspacePath(left) == canonicalWorkspacePath(right)
}

func canonicalWorkspacePath(path string) string {
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		absolute = filepath.Clean(path)
	}
	absolute = filepath.Clean(absolute)
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = filepath.Clean(resolved)
	}
	if runtime.GOOS == "windows" {
		absolute = strings.ToLower(absolute)
	}
	return absolute
}

func (a App) workspaceRemove(defaultConfig config.Config, rawID string, opts options) error {
	return withLifecycleLock(context.Background(), "workspace remove", func() error {
		return a.workspaceRemoveUnlocked(defaultConfig, rawID, opts)
	})
}

func (a App) workspaceRemoveUnlocked(defaultConfig config.Config, rawID string, opts options) error {
	id := workspaceregistry.NormalizeID(rawID)
	registry, err := workspaceregistry.Load()
	if err != nil {
		return err
	}
	entry, exists := registry.Workspaces[id]
	if !exists {
		return fmt.Errorf("workspace %q is not registered", id)
	}
	var configSnapshot workspaceFileSnapshot
	if opts.Force {
		configSnapshot, err = captureWorkspaceFile(entry.ConfigPath)
		if err != nil {
			return fmt.Errorf("snapshot workspace config: %w", err)
		}
		if err := removeWorkspaceConfig(entry.ConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove workspace config: %w", err)
		}
	}
	delete(registry.Workspaces, id)
	if err := saveWorkspaceRegistry(registry); err != nil {
		var rollbackErr error
		if opts.Force {
			rollbackErr = configSnapshot.restore()
		}
		return errors.Join(
			fmt.Errorf("save workspace registry: %w", err),
			wrapOptionalError("restore workspace config", rollbackErr),
		)
	}
	fmt.Fprintf(a.Stdout, "Workspace %s removed from the daemon registry\n", id)
	if readHealth(defaultConfig.Port) != nil {
		fmt.Fprintln(a.Stdout, "Restart Wormhole to remove the active endpoint.")
	}
	return nil
}

func (a App) workspaceCompact(defaultConfig config.Config, rawID string, opts options) error {
	return withLifecycleLock(context.Background(), "workspace compact", func() error {
		return a.workspaceCompactUnlocked(defaultConfig, rawID, opts)
	})
}

func (a App) workspaceCompactUnlocked(defaultConfig config.Config, rawID string, opts options) error {
	id := workspaceregistry.NormalizeID(rawID)
	registry, err := workspaceregistry.Load()
	if err != nil {
		return err
	}
	entry, exists := registry.Workspaces[id]
	if !exists {
		return fmt.Errorf("workspace %q is not registered", id)
	}
	override, err := config.ReadOverrideFile(entry.ConfigPath)
	if err != nil {
		return err
	}
	originalRaw, err := json.Marshal(override)
	if err != nil {
		return err
	}
	var original map[string]any
	if err := json.Unmarshal(originalRaw, &original); err != nil {
		return err
	}
	for _, field := range []string{
		"workspace", "port", "host", "authToken", "approvalToken", "allowedOrigins",
		"noTunnel", "tunnelBin", "tunnelId", "organizationId", "profile", "profileDir", "runtimeKeyEnv", "remoteIngresses",
	} {
		delete(override, field)
	}
	compacted, err := config.CompactOverride(defaultConfig, override)
	if err != nil {
		return err
	}
	afterRaw, _ := json.Marshal(compacted)
	changed := string(originalRaw) != string(afterRaw)
	result := map[string]any{
		"workspace": id, "config_path": entry.ConfigPath, "changed": changed,
		"dry_run": opts.DryRun, "before": original, "after": compacted,
	}
	if !opts.DryRun {
		// Always rewrite non-dry-run compactions so an unchanged legacy document
		// still receives the current schemaVersion metadata.
		if err := saveWorkspaceOverride(entry.ConfigPath, defaultConfig, compacted); err != nil {
			return err
		}
	}
	if opts.JSON || opts.DryRun {
		raw, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Stdout, string(raw))
		return nil
	}
	fmt.Fprintf(a.Stdout, "Workspace %s override compacted (changed=%t)\n", id, changed)
	return nil
}

func (a App) workspaceList(defaultConfig config.Config, opts options) error {
	registry, err := workspaceregistry.Load()
	if err != nil {
		return err
	}
	health := readHealth(defaultConfig.Port)
	online := workspaceHealthMap(health)
	ids := workspaceregistry.SortedIDs(registry)
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		entry := registry.Workspaces[id]
		_, active := online[id]
		items = append(items, map[string]any{
			"id": id, "workspace": entry.Workspace, "enabled": entry.Enabled,
			"endpoint": workspaceEndpoint(id), "online": active,
			"config_path": entry.ConfigPath, "data_dir": entry.DataDir,
		})
	}
	if opts.JSON {
		raw, _ := json.MarshalIndent(map[string]any{
			"daemon": map[string]any{
				"id":        workspaceregistry.IDFromPath(defaultConfig.Workspace),
				"workspace": defaultConfig.Workspace, "port": defaultConfig.Port,
				"online": health != nil, "endpoint": "/mcp",
			},
			"workspaces": items,
		}, "", "  ")
		fmt.Fprintln(a.Stdout, string(raw))
		return nil
	}
	fmt.Fprintf(a.Stdout, "%s\t%s\tport=%d\t%s\n", workspaceregistry.IDFromPath(defaultConfig.Workspace), ternary(health != nil, "online", "offline"), defaultConfig.Port, defaultConfig.Workspace)
	for _, item := range items {
		status := "disabled"
		enabled, online := item["enabled"] == true, item["online"] == true
		if enabled {
			status = "offline"
		}
		if enabled && online {
			status = "online"
		} else if !enabled && online {
			status = "stale-online"
		}
		fmt.Fprintf(a.Stdout, "%s\t%s\t%s\t%s\n", item["id"], status, item["endpoint"], item["workspace"])
	}
	return nil
}

func (a App) workspaceLifecycle(ctx context.Context, defaultConfig config.Config, action, rawID string, opts options) error {
	if action != "start" && action != "stop" && action != "status" {
		return fmt.Errorf("unsupported workspace action: %s", action)
	}
	if action == "status" {
		return a.workspaceLifecycleUnlocked(ctx, defaultConfig, action, rawID, opts)
	}
	return withLifecycleLock(ctx, "workspace "+action, func() error {
		return a.workspaceLifecycleUnlocked(ctx, defaultConfig, action, rawID, opts)
	})
}

func (a App) workspaceLifecycleUnlocked(ctx context.Context, defaultConfig config.Config, action, rawID string, opts options) error {
	id := workspaceregistry.NormalizeID(rawID)
	registry, err := workspaceregistry.Load()
	if err != nil {
		return err
	}
	entry, exists := registry.Workspaces[id]
	if !exists {
		return fmt.Errorf("workspace %q is not registered; run wormhole workspace list", id)
	}
	health := readHealth(defaultConfig.Port)
	_, online := workspaceHealthMap(health)[id]

	if action == "status" {
		value := map[string]any{
			"id": id, "workspace": entry.Workspace, "enabled": entry.Enabled,
			"online": online, "endpoint": workspaceEndpoint(id),
			"daemon_online": health != nil,
		}
		if opts.JSON {
			raw, _ := json.MarshalIndent(value, "", "  ")
			fmt.Fprintln(a.Stdout, string(raw))
			return nil
		}
		fmt.Fprintf(a.Stdout, "Workspace: %s\nRoot:      %s\nEnabled:   %t\nOnline:    %t\nEndpoint:  http://127.0.0.1:%d%s\n", id, entry.Workspace, entry.Enabled, online, defaultConfig.Port, workspaceEndpoint(id))
		return nil
	}

	desiredEnabled := action == "start"
	changed := entry.Enabled != desiredEnabled
	if changed {
		entry.Enabled = desiredEnabled
		entry.UpdatedAt = time.Now().UTC()
		registry.Workspaces[id] = entry
		if err := saveWorkspaceRegistry(registry); err != nil {
			return err
		}
	}

	if action == "start" {
		if !changed && online {
			fmt.Fprintf(a.Stdout, "Workspace %s is already online at http://127.0.0.1:%d%s\n", id, defaultConfig.Port, workspaceEndpoint(id))
			return nil
		}
		if changed {
			fmt.Fprintf(a.Stdout, "Workspace %s enabled\n", id)
		}
		fmt.Fprintln(a.Stdout, "Reconciling the shared daemon to activate the endpoint")
		return a.startUnlocked(ctx, defaultConfig, options{Background: true, RuntimeKey: opts.RuntimeKey})
	}
	if action == "stop" {
		if changed {
			fmt.Fprintf(a.Stdout, "Workspace %s disabled\n", id)
		}
		if health != nil && (changed || online) {
			fmt.Fprintln(a.Stdout, "Reconciling the shared daemon to unload the endpoint")
			return a.startUnlocked(ctx, defaultConfig, options{Background: true, RuntimeKey: opts.RuntimeKey})
		}
		if !changed {
			fmt.Fprintf(a.Stdout, "Workspace %s is already disabled\n", id)
		}
		return nil
	}
	return nil
}

func (a App) printAutoWorkspace(entry workspaceregistry.Registration, created, enabled bool, port int) {
	if entry.ConfigPath == "" {
		return
	}
	switch {
	case created:
		fmt.Fprintf(a.Stdout, "[workspace] auto-registered %s for %s\n", entry.ID, entry.Workspace)
	case enabled:
		fmt.Fprintf(a.Stdout, "[workspace] re-enabled %s for %s\n", entry.ID, entry.Workspace)
	default:
		fmt.Fprintf(a.Stdout, "[workspace] using %s for %s\n", entry.ID, entry.Workspace)
	}
	fmt.Fprintf(a.Stdout, "[workspace] endpoint http://127.0.0.1:%d%s\n", port, workspaceEndpoint(entry.ID))
}

func workspaceEndpoint(id string) string {
	return "/mcp/workspaces/" + workspaceregistry.NormalizeID(id)
}

func workspaceHealthMap(health map[string]any) map[string]map[string]any {
	result := map[string]map[string]any{}
	if health == nil {
		return result
	}
	items, _ := health["workspaces"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		id := strings.TrimSpace(fmt.Sprint(item["id"]))
		if id != "" && id != "<nil>" {
			result[id] = item
		}
	}
	return result
}

func loadNamedWorkspaceConfigs(defaultConfig config.Config) ([]namedWorkspaceConfig, error) {
	registry, err := workspaceregistry.Load()
	if err != nil {
		return nil, err
	}
	entries := workspaceregistry.Enabled(registry)
	result := make([]namedWorkspaceConfig, 0, len(entries))
	for _, entry := range entries {
		cfg, err := config.LoadOverrideFile(entry.ConfigPath, defaultConfig)
		if err != nil {
			return nil, fmt.Errorf("load workspace %q config: %w", entry.ID, err)
		}
		cfg.Workspace = entry.Workspace
		cfg.Host = defaultConfig.Host
		cfg.Port = defaultConfig.Port
		cfg.AuthToken = defaultConfig.AuthToken
		cfg.ApprovalToken = defaultConfig.ApprovalToken
		cfg.AllowedOrigins = append([]string(nil), defaultConfig.AllowedOrigins...)
		cfg.NoTunnel = true
		cfg.TunnelID = ""
		cfg.RemoteIngresses = nil
		result = append(result, namedWorkspaceConfig{Registration: entry, Config: cfg})
	}
	return result, nil
}

func daemonConfigID(cfg config.Config, binaryPath string, widget []byte) (string, error) {
	return daemonConfigIDWithInputs(cfg, config.NewIdentityInputs(binaryPath, widget, ""))
}

func daemonConfigIDWithInputs(cfg config.Config, inputs config.IdentityInputs) (string, error) {
	registryFingerprint, err := workspaceregistry.Fingerprint()
	if err != nil {
		return "", err
	}
	named, err := loadNamedWorkspaceConfigs(cfg)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("default:" + cfg.ConfigIDWithInputs(inputs) + "\n"))
	_, _ = hash.Write([]byte("registry:" + registryFingerprint + "\n"))
	for _, item := range named {
		_, _ = hash.Write([]byte(item.Registration.ID + ":" + item.Config.ConfigIDWithInputs(inputs) + "\n"))
	}
	return hex.EncodeToString(hash.Sum(nil)[:8]), nil
}
