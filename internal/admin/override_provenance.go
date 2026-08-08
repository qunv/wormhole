// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"encoding/json"
	"sort"
	"strings"

	"wormhole/internal/config"
)

const maxOverrideProvenanceEntries = 500

type overrideProvenanceEntry struct {
	Path      string `json:"path"`
	State     string `json:"state"`
	Inherited any    `json:"inherited,omitempty"`
	Override  any    `json:"override,omitempty"`
	Effective any    `json:"effective,omitempty"`
}

type overrideProvenance struct {
	Entries           []overrideProvenanceEntry `json:"entries"`
	InheritedTopLevel []string                  `json:"inheritedTopLevel"`
	CompactedOverride map[string]any            `json:"compactedOverride"`
	RedundantPaths    []string                  `json:"redundantPaths"`
	Truncated         bool                      `json:"truncated"`
}

func describeOverride(base, effective config.Config, override map[string]any) (overrideProvenance, error) {
	base.AuthToken, base.ApprovalToken = "", ""
	effective.AuthToken, effective.ApprovalToken = "", ""
	baseObject, err := configObject(base)
	if err != nil {
		return overrideProvenance{}, err
	}
	effectiveObject, err := configObject(effective)
	if err != nil {
		return overrideProvenance{}, err
	}
	compacted, err := config.CompactOverride(base, override)
	if err != nil {
		return overrideProvenance{}, err
	}

	result := overrideProvenance{CompactedOverride: compacted}
	walkOverrideProvenance("", override, baseObject, effectiveObject, &result)
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Path < result.Entries[j].Path })

	for key := range baseObject {
		if key == "schemaVersion" || workspaceOwnedFields[key] {
			continue
		}
		if _, overridden := override[key]; !overridden {
			result.InheritedTopLevel = append(result.InheritedTopLevel, key)
		}
	}
	sort.Strings(result.InheritedTopLevel)

	originalPaths := map[string]bool{}
	compactedPaths := map[string]bool{}
	collectOverridePaths("", override, originalPaths)
	collectOverridePaths("", compacted, compactedPaths)
	for path := range originalPaths {
		if path != "schemaVersion" && !compactedPaths[path] {
			result.RedundantPaths = append(result.RedundantPaths, path)
		}
	}
	sort.Strings(result.RedundantPaths)
	return result, nil
}

func configObject(value config.Config) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	return object, nil
}

func walkOverrideProvenance(prefix string, override, base, effective map[string]any, result *overrideProvenance) {
	keys := make([]string, 0, len(override))
	for key := range override {
		if key != "schemaVersion" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if len(result.Entries) >= maxOverrideProvenanceEntries {
			result.Truncated = true
			return
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		value := override[key]
		baseValue := base[key]
		effectiveValue := effective[key]
		if value == nil {
			result.Entries = append(result.Entries, overrideProvenanceEntry{
				Path: path, State: "removed", Inherited: boundedProvenanceValue(baseValue),
			})
			continue
		}
		overrideObject, objectOverride := value.(map[string]any)
		if objectOverride && len(overrideObject) > 0 {
			baseObject, _ := baseValue.(map[string]any)
			effectiveObject, _ := effectiveValue.(map[string]any)
			if baseObject == nil {
				baseObject = map[string]any{}
			}
			if effectiveObject == nil {
				effectiveObject = map[string]any{}
			}
			walkOverrideProvenance(path, overrideObject, baseObject, effectiveObject, result)
			continue
		}
		result.Entries = append(result.Entries, overrideProvenanceEntry{
			Path: path, State: "overridden", Inherited: boundedProvenanceValue(baseValue),
			Override: boundedProvenanceValue(value), Effective: boundedProvenanceValue(effectiveValue),
		})
	}
}

func boundedProvenanceValue(value any) any {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) <= 4<<10 {
		return value
	}
	return map[string]any{"summary": "value omitted from provenance because it exceeds 4096 bytes", "bytes": len(raw)}
}

func collectOverridePaths(prefix string, value map[string]any, result map[string]bool) {
	for key, child := range value {
		if key == "schemaVersion" {
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if object, ok := child.(map[string]any); ok && len(object) > 0 {
			collectOverridePaths(path, object, result)
			continue
		}
		result[strings.TrimSpace(path)] = true
	}
}
