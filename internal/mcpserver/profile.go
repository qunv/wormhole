// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"codebridge/internal/agent"
	"codebridge/internal/config"
)

type ProfileDefinition struct {
	ID              string
	Name            string
	Description     string
	AllowedGroups   []string
	AllowedTools    []string
	DeniedTools     []string
	OutputMode      agent.ToolOutputMode
	CompactDefaults bool
	BuiltIn         bool
}

func BuiltInProfile(profile ToolProfile) ProfileDefinition {
	switch profile {
	case ToolProfileFast:
		return ProfileDefinition{
			ID: "fast", Name: "Fast",
			Description:  "Compact profile with workspace routing and high-value coding tools.",
			AllowedTools: sortedMapKeys(fastCodingTools), OutputMode: agent.ToolOutputStructured,
			CompactDefaults: true, BuiltIn: true,
		}
	default:
		return ProfileDefinition{
			ID: "full", Name: "Full",
			Description: "Complete profile with workspace routing and every enabled runtime tool.",
			OutputMode:  agent.ToolOutputBoth, BuiltIn: true,
		}
	}
}

func ResolveProfile(cfg config.Config, rawID string) (ProfileDefinition, bool) {
	id := strings.ToLower(strings.TrimSpace(rawID))
	switch id {
	case "full", "":
		return BuiltInProfile(ToolProfileFull), true
	case "fast":
		return BuiltInProfile(ToolProfileFast), true
	}
	profile, ok := cfg.ToolProfiles[id]
	if !ok {
		return ProfileDefinition{}, false
	}
	mode := agent.ToolOutputMode(profile.OutputMode)
	if mode == "" {
		mode = agent.ToolOutputBoth
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = id
	}
	description := strings.TrimSpace(profile.Description)
	if description == "" {
		description = "Custom tool profile " + id + "."
	}
	return ProfileDefinition{
		ID: id, Name: name, Description: description,
		AllowedGroups: append([]string(nil), profile.AllowedGroups...),
		AllowedTools:  append([]string(nil), profile.AllowedTools...),
		DeniedTools:   append([]string(nil), profile.DeniedTools...),
		OutputMode:    mode, CompactDefaults: profile.CompactDefaults,
	}, true
}

func ProfileDefinitions(cfg config.Config) []ProfileDefinition {
	profiles := []ProfileDefinition{BuiltInProfile(ToolProfileFast), BuiltInProfile(ToolProfileFull)}
	ids := make([]string, 0, len(cfg.ToolProfiles))
	for id := range cfg.ToolProfiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if profile, ok := ResolveProfile(cfg, id); ok {
			profiles = append(profiles, profile)
		}
	}
	return profiles
}

func SessionProfileEndpoint(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "full" || id == "" {
		return SessionEndpoint
	}
	if id == "fast" {
		return SessionFastEndpoint
	}
	return "/mcp/session/profiles/" + id
}

func FixedProfileEndpoint(workspaceID, profileID string) string {
	workspaceID = strings.ToLower(strings.TrimSpace(workspaceID))
	profileID = strings.ToLower(strings.TrimSpace(profileID))
	base := "/mcp"
	if workspaceID != "" && workspaceID != "default" {
		base = "/mcp/workspaces/" + workspaceID
	}
	if profileID == "full" || profileID == "" {
		return base
	}
	if profileID == "fast" {
		return base + "/fast"
	}
	return base + "/profiles/" + profileID
}

func profileToolEnabledDefinition(runtime *agent.Runtime, profile ProfileDefinition, name string) bool {
	if runtime == nil || !runtime.ToolEnabled(name) || stringListContains(profile.DeniedTools, name) {
		return false
	}
	if len(profile.AllowedGroups) == 0 && len(profile.AllowedTools) == 0 {
		return true
	}
	if stringListContains(profile.AllowedTools, name) {
		return true
	}
	return stringListContains(profile.AllowedGroups, runtime.ToolModuleName(name))
}

func profileOutputModeDefinition(profile ProfileDefinition, mode agent.ToolOutputMode) agent.ToolOutputMode {
	if profile.ID == "fast" {
		return mode
	}
	if profile.OutputMode == "" {
		return agent.ToolOutputBoth
	}
	return profile.OutputMode
}

func applyProfileDefaultsDefinition(profile ProfileDefinition, tool string, args map[string]any) {
	if !profile.CompactDefaults || args == nil {
		return
	}
	setDefault := func(key string, value any) {
		if _, exists := args[key]; !exists {
			args[key] = value
		}
	}
	switch tool {
	case "workspace_snapshot":
		setDefault("detail_level", "compact")
		setDefault("token_budget", 4_000)
		setDefault("max_entries", 120)
		setDefault("include_memory", false)
	case "task_context":
		setDefault("detail_level", "compact")
		setDefault("token_budget", 8_000)
		setDefault("max_entries", 100)
		setDefault("search_limit", 16)
		setDefault("max_read_files", 5)
		setDefault("include_memory", false)
	case "codegraph_explore":
		setDefault("detail_level", "compact")
		setDefault("token_budget", 8_000)
	}
}

func ProfileContractHash(tools []ProfileToolInfo) string {
	copyTools := append([]ProfileToolInfo(nil), tools...)
	sort.Slice(copyTools, func(i, j int) bool { return copyTools[i].Name < copyTools[j].Name })
	raw, _ := json.Marshal(copyTools)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:12])
}

func stringListContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sortedMapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key, enabled := range values {
		if enabled {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
