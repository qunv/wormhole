// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/mcpserver"
	"codebridge/internal/security"
	"codebridge/internal/workspaceregistry"
)

const (
	diagnosticBundleVersion = 1
	maxDiagnosticBundle     = 4 << 20
	maxDiagnosticLogTail    = 64 << 10
	maxDiagnosticAudit      = 50
)

func (h *Handler) getDiagnostics(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 8*time.Second)
	defer cancel()
	cfg, err := h.editableConfig()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "diagnostics_config_failed", err.Error())
		return
	}
	knownSecrets, secretSummary, err := h.diagnosticSecrets()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "diagnostics_secrets_failed", err.Error())
		return
	}
	knownSecrets = append(knownSecrets, h.Runtime.Config.AuthToken, h.Runtime.Config.ApprovalToken)
	configValue, err := diagnosticConfig(cfg, knownSecrets)
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "diagnostics_config_failed", err.Error())
		return
	}

	workspaces := make([]map[string]any, 0, len(h.runtimeEntries()))
	for _, item := range h.runtimeEntries() {
		workspaces = append(workspaces, map[string]any{
			"id": item.ID, "root": item.Runtime.Workspace.Primary,
			"configId": item.Runtime.ConfigID, "mode": item.Runtime.Config.Mode,
			"policy": item.Runtime.Config.Policy, "startupWarnings": item.Runtime.StartupWarnings(),
			"metrics": item.Runtime.RuntimeMetrics(true, 20),
			"modules": redactDiagnosticValue(item.Runtime.ModuleHealth(ctx), knownSecrets),
		})
	}

	registrySummary := []map[string]any{}
	if registry, registryErr := workspaceregistry.Load(); registryErr == nil {
		for _, id := range workspaceregistry.SortedIDs(registry) {
			entry := registry.Workspaces[id]
			_, active := h.Runtimes[id]
			registrySummary = append(registrySummary, map[string]any{
				"id": id, "workspace": entry.Workspace, "enabled": entry.Enabled,
				"active": active, "configPath": entry.ConfigPath, "dataDir": entry.DataDir,
			})
		}
	}

	profiles := []map[string]any{}
	for _, profile := range mcpserver.ProfileDefinitions(cfg) {
		tools := h.Router.ProfileToolsDefinition(profile)
		_, active := h.Router.ResolveProfile(profile.ID)
		profiles = append(profiles, map[string]any{
			"id": profile.ID, "active": active, "builtIn": profile.BuiltIn,
			"endpoint":  mcpserver.SessionProfileEndpoint(profile.ID),
			"toolCount": len(tools), "contractHash": mcpserver.ProfileContractHash(tools),
		})
	}

	logs := map[string]any{}
	logPaths := map[string]string{"server": config.ServerLogPath()}
	for _, tunnel := range cfg.EffectiveTunnels() {
		logPaths["tunnel:"+tunnel.Name] = config.TunnelLogPathFor(tunnel.Name)
	}
	for name, path := range logPaths {
		text, truncated, readErr := readDiagnosticTextTail(path, knownSecrets)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			logs[name] = map[string]any{"path": path, "error": security.RedactText(readErr.Error(), 1<<10)}
			continue
		}
		logs[name] = map[string]any{"path": path, "tail": text, "truncated": truncated}
	}

	audit := h.diagnosticAudit(knownSecrets)
	routerStats := map[string]any{"enabled": false}
	if h.Router != nil {
		routerStats = h.Router.Stats()
		routerStats["enabled"] = true
	}
	bundle := map[string]any{
		"diagnosticVersion": diagnosticBundleVersion,
		"generatedAt":       time.Now().UTC(), "name": "Codebridge",
		"version": h.Runtime.Version, "tier": h.Runtime.Tier,
		"activeConfigId": h.Runtime.ConfigID,
		"paths": map[string]any{
			"home": config.AppHomeDir(), "config": config.ConfigPath(),
			"registry": workspaceregistry.Path(), "state": config.AppDataDir(),
		},
		"config": configValue, "secrets": secretSummary,
		"workspaces": workspaces, "registry": registrySummary,
		"profiles": profiles, "sessionRouter": routerStats,
		"sharedResources": h.Runtime.SharedResourceStats(),
		"audit":           audit, "logs": logs,
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "diagnostics_encode_failed", err.Error())
		return
	}
	if len(raw) > maxDiagnosticBundle {
		h.sendError(writer, http.StatusInternalServerError, "diagnostics_too_large", fmt.Sprintf("Diagnostic bundle exceeds %d bytes", maxDiagnosticBundle))
		return
	}
	raw = append(raw, '\n')
	filename := "codebridge-diagnostics-" + time.Now().UTC().Format("20060102T150405Z") + ".json"
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Length", fmt.Sprint(len(raw)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(raw)
}

func (h *Handler) diagnosticSecrets() ([]string, []map[string]any, error) {
	references, err := h.secretReferences()
	if err != nil {
		return nil, nil, err
	}
	managedKeys, err := config.DotEnvKeys(config.DotEnvPath())
	if err != nil {
		return nil, nil, err
	}
	managed := map[string]bool{}
	for _, key := range managedKeys {
		managed[key] = true
	}
	names := make([]string, 0, len(references))
	for name := range references {
		names = append(names, name)
	}
	sort.Strings(names)
	known := make([]string, 0, len(names))
	summary := make([]map[string]any, 0, len(names))
	for _, name := range names {
		value, exists := os.LookupEnv(name)
		if exists && value != "" {
			known = append(known, value)
		}
		source := "missing"
		if managed[name] {
			source = "dotenv"
		} else if exists && value != "" {
			source = "environment"
		}
		summary = append(summary, map[string]any{
			"name": name, "configured": exists && value != "", "managed": managed[name],
			"source": source, "referencedBy": references[name],
		})
	}
	return known, summary, nil
}

func diagnosticConfig(cfg config.Config, secrets []string) (any, error) {
	cfg.AuthToken, cfg.ApprovalToken = "", ""
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if servers, ok := object["mcpServers"].(map[string]any); ok {
		for _, value := range servers {
			server, _ := value.(map[string]any)
			if server == nil {
				continue
			}
			for _, field := range []string{"env", "headers"} {
				if values, ok := server[field].(map[string]any); ok {
					keys := make([]string, 0, len(values))
					for key := range values {
						keys = append(keys, key)
					}
					sort.Strings(keys)
					server[field+"Keys"] = keys
					delete(server, field)
				}
			}
		}
	}
	return redactDiagnosticValue(security.RedactDeep(object, 0), secrets), nil
}

func redactDiagnosticValue(value any, secrets []string) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			result[key] = redactDiagnosticValue(child, secrets)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = redactDiagnosticValue(child, secrets)
		}
		return result
	case string:
		for _, secret := range secrets {
			if secret != "" {
				typed = strings.ReplaceAll(typed, secret, "[REDACTED]")
			}
		}
		return security.RedactText(typed, 16<<10)
	default:
		return value
	}
}

func readDiagnosticTextTail(path string, secrets []string) (string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, errors.New("diagnostic log is not a regular file")
	}
	offset := max(int64(0), info.Size()-maxDiagnosticLogTail)
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", false, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxDiagnosticLogTail+1))
	if err != nil {
		return "", false, err
	}
	truncated := offset > 0 || len(raw) > maxDiagnosticLogTail
	if len(raw) > maxDiagnosticLogTail {
		raw = raw[:maxDiagnosticLogTail]
	}
	text := string(raw)
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	return security.RedactText(text, maxDiagnosticLogTail), truncated, nil
}

func (h *Handler) diagnosticAudit(secrets []string) []map[string]any {
	items := make([]map[string]any, 0, maxDiagnosticAudit)
	for _, runtime := range h.runtimeEntries() {
		records, _, err := readAuditTail(runtime.Runtime.Store.AuditPath)
		if err != nil {
			continue
		}
		for _, record := range records {
			delete(record, "args")
			delete(record, "session_id")
			delete(record, "workspace")
			record["workspaceId"] = runtime.ID
			items = append(items, redactDiagnosticValue(record, secrets).(map[string]any))
			if len(items) == maxDiagnosticAudit {
				return items
			}
		}
	}
	return items
}
