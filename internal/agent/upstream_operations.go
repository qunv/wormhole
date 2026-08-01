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

	"codebridge/internal/config"
	"codebridge/internal/security"
	"codebridge/internal/upstreammcp"
)

type toolContractSummary struct {
	ToolCount int               `json:"toolCount"`
	Hash      string            `json:"hash"`
	Tools     []string          `json:"tools"`
	index     map[string]string `json:"-"`
}

// UpstreamMCPStatuses returns bounded operational views for configured upstream
// servers that apply to this runtime. It never returns transport credentials,
// schemas, arguments, results, or raw resource keys.
func (r *Runtime) UpstreamMCPStatuses(ctx context.Context) []map[string]any {
	if r == nil {
		return nil
	}
	result := make([]map[string]any, 0, len(r.Config.MCPServers))
	for _, name := range config.SortedMCPServerNames(r.Config.MCPServers) {
		cfg := r.Config.MCPServers[name]
		if !cfg.IsEnabled() || !cfg.AppliesToWorkspace(r.WorkspaceID) {
			continue
		}
		status, err := r.upstreamMCPStatus(ctx, name, cfg)
		if err != nil {
			status = map[string]any{
				"name": name, "configured": true, "active": false,
				"error": security.RedactText(err.Error(), 2<<10),
			}
		}
		result = append(result, status)
	}
	return result
}

// RefreshUpstreamMCP replaces the live client session with a newly discovered
// catalog and persists it for the next daemon restart. The active downstream
// module contract is intentionally not mutated in place.
func (r *Runtime) RefreshUpstreamMCP(ctx context.Context, rawName string) (map[string]any, error) {
	if r == nil {
		return nil, errors.New("runtime is unavailable")
	}
	name := strings.ToLower(strings.TrimSpace(rawName))
	cfg, ok := r.Config.MCPServers[name]
	if !ok || !cfg.IsEnabled() || !cfg.AppliesToWorkspace(r.WorkspaceID) {
		return nil, fmt.Errorf("upstream MCP server %q is not active in workspace %q", name, r.WorkspaceID)
	}
	cwd, err := resolveUpstreamCWD(r, name, cfg)
	if err != nil {
		return nil, err
	}
	clientKey := upstreamClientResourceKey(name, cfg, cwd)
	r.shared.mu.Lock()
	client := r.shared.upstreamClients[clientKey]
	r.shared.mu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("upstream MCP server %q has no initialized client; restart Codebridge after validating its configuration", name)
	}
	if err := client.RefreshCatalog(ctx); err != nil {
		return nil, err
	}
	return r.upstreamMCPStatus(ctx, name, cfg)
}

func (r *Runtime) upstreamMCPStatus(ctx context.Context, name string, cfg config.MCPServerConfig) (map[string]any, error) {
	cwd, err := resolveUpstreamCWD(r, name, cfg)
	if err != nil {
		return nil, err
	}
	clientKey := upstreamClientResourceKey(name, cfg, cwd)
	r.shared.mu.Lock()
	client := r.shared.upstreamClients[clientKey]
	r.shared.mu.Unlock()

	r.moduleMu.RLock()
	activeModule, _ := r.modules["mcp_"+name].(*upstreamMCPModule)
	r.moduleMu.RUnlock()
	active := toolContractSummary{}
	if activeModule != nil {
		active = summarizeToolContract(activeModule.Specs())
	}

	cached := toolContractSummary{}
	cachedError := ""
	if catalog, loadErr := upstreammcp.LoadToolCatalog(clientKey); loadErr == nil {
		module, buildErr := newUpstreamMCPModuleFromTools(name, cfg, nil, catalog.Tools)
		if buildErr != nil {
			cachedError = buildErr.Error()
		} else {
			cached = summarizeToolContract(module.Specs())
		}
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		cachedError = loadErr.Error()
	}

	live := toolContractSummary{}
	liveError := ""
	health := map[string]any{"available": false, "initialized": false}
	if client != nil {
		health = map[string]any{}
		for key, value := range client.Health(ctx) {
			health[key] = value
		}
		health["initialized"] = true
		module, buildErr := newUpstreamMCPModuleFromTools(name, cfg, client, client.Tools())
		if buildErr != nil {
			liveError = buildErr.Error()
		} else {
			live = summarizeToolContract(module.Specs())
		}
	}
	desired := live
	if desired.Hash == "" {
		desired = cached
	}
	status := map[string]any{
		"name": name, "configured": true, "active": activeModule != nil,
		"transport": cfg.EffectiveTransport(), "startupMode": cfg.EffectiveStartupMode(),
		"required": cfg.Required, "refreshAvailable": client != nil,
		"health": health, "activeContract": active, "cachedContract": cached, "liveContract": live,
		"activeToDesired": diffToolContracts(active, desired),
		"cachedToLive":    diffToolContracts(cached, live),
		"restartRequired": desired.Hash != "" && desired.Hash != active.Hash,
	}
	if cachedError != "" {
		status["cachedError"] = security.RedactText(cachedError, 2<<10)
	}
	if liveError != "" {
		status["liveError"] = security.RedactText(liveError, 2<<10)
	}
	return status, nil
}

func summarizeToolContract(specs []ToolSpec) toolContractSummary {
	items := append([]ToolSpec(nil), specs...)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	type contractItem struct {
		Name        string         `json:"name"`
		Title       string         `json:"title"`
		Description string         `json:"description"`
		ReadOnly    bool           `json:"readOnly"`
		Destructive bool           `json:"destructive"`
		OpenWorld   bool           `json:"openWorld"`
		Schema      map[string]any `json:"schema"`
	}
	contract := make([]contractItem, 0, len(items))
	index := make(map[string]string, len(items))
	names := make([]string, 0, min(len(items), 200))
	for _, spec := range items {
		item := contractItem{
			Name: spec.Name, Title: spec.Title, Description: spec.Description,
			ReadOnly: spec.ReadOnly, Destructive: spec.Destructive, OpenWorld: spec.OpenWorld,
			Schema: spec.Schema,
		}
		raw, _ := json.Marshal(item)
		sum := sha256.Sum256(raw)
		fingerprint := hex.EncodeToString(sum[:12])
		index[spec.Name] = fingerprint
		contract = append(contract, item)
		if len(names) < 200 {
			names = append(names, spec.Name)
		}
	}
	raw, _ := json.Marshal(contract)
	sum := sha256.Sum256(raw)
	return toolContractSummary{
		ToolCount: len(items), Hash: "sha256:" + hex.EncodeToString(sum[:12]),
		Tools: names, index: index,
	}
}

func diffToolContracts(before, after toolContractSummary) map[string]any {
	added, removed, changed := []string{}, []string{}, []string{}
	for name, afterFingerprint := range after.index {
		beforeFingerprint, exists := before.index[name]
		if !exists {
			added = append(added, name)
		} else if beforeFingerprint != afterFingerprint {
			changed = append(changed, name)
		}
	}
	for name := range before.index {
		if _, exists := after.index[name]; !exists {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	return map[string]any{
		"added": added, "removed": removed, "changed": changed,
		"changedCount": len(added) + len(removed) + len(changed),
	}
}
