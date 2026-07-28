// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
)

// ReadOverrideFile reads a partial configuration document. A missing file is
// equivalent to an empty override so a named workspace can inherit the global
// configuration without duplicating it.
func ReadOverrideFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeWorkspaceOverrideObject(raw, path)
}

// LoadOverrideFile applies a partial JSON object over an already assembled
// base configuration. Objects merge recursively, arrays and scalar values
// replace inherited values, and null removes an inherited key.
func LoadOverrideFile(path string, base Config) (Config, error) {
	override, err := ReadOverrideFile(path)
	if err != nil {
		return base, err
	}
	return ApplyOverride(base, override)
}

// ApplyOverride returns one normalized and validated effective configuration.
func ApplyOverride(base Config, override map[string]any) (Config, error) {
	if override == nil {
		override = map[string]any{}
	}
	normalizedOverride, err := cloneConfigObject(override)
	if err != nil {
		return base, err
	}
	baseRaw, err := json.Marshal(base)
	if err != nil {
		return base, err
	}
	baseObject, err := decodeConfigObject(baseRaw, "base config")
	if err != nil {
		return base, err
	}
	merged := mergeConfigObject(baseObject, normalizedOverride)
	mergedRaw, err := json.Marshal(merged)
	if err != nil {
		return base, err
	}
	var cfg Config
	if err := json.Unmarshal(mergedRaw, &cfg); err != nil {
		return base, fmt.Errorf("apply config override: %w", err)
	}
	migrateLegacyConfigPaths(&cfg)
	normalize(&cfg)
	return cfg, cfg.Validate(false)
}

// SaveOverrideFile validates and atomically persists a non-secret partial
// configuration. Listener and workspace ownership fields may be present for
// backward compatibility, but runtime bearer and approval tokens are never
// written.
func SaveOverrideFile(path string, base Config, override map[string]any) error {
	clean, err := cloneConfigObject(override)
	if err != nil {
		return err
	}
	delete(clean, "authToken")
	delete(clean, "approvalToken")
	if _, err := ApplyOverride(base, clean); err != nil {
		return err
	}
	persisted := make(map[string]any, len(clean)+1)
	for key, value := range clean {
		persisted[key] = value
	}
	persisted["schemaVersion"] = CurrentWorkspaceOverrideSchemaVersion
	raw, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(raw, '\n'), 0o600)
}

func cloneConfigObject(value map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return decodeWorkspaceOverrideObject(raw, "override")
}

// CompactOverride removes values that are identical to the current global
// base so legacy full snapshots converge toward partial overrides. extraRoots
// is intentionally preserved because an explicit empty list prevents future
// global roots from leaking into a named workspace.
func CompactOverride(base Config, override map[string]any) (map[string]any, error) {
	clean, err := cloneConfigObject(override)
	if err != nil {
		return nil, err
	}
	baseRaw, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	baseObject, err := decodeConfigObject(baseRaw, "base config")
	if err != nil {
		return nil, err
	}
	return compactOverrideObject(baseObject, clean, "$"), nil
}

func compactOverrideObject(base, override map[string]any, path string) map[string]any {
	result := map[string]any{}
	for key, value := range override {
		childPath := path + "." + key
		if value == nil {
			result[key] = nil
			continue
		}
		if childPath == "$.extraRoots" {
			result[key] = value
			continue
		}
		overrideObject, overrideIsObject := value.(map[string]any)
		baseValue, baseExists := base[key]
		baseObject, baseIsObject := baseValue.(map[string]any)
		if overrideIsObject {
			if !baseIsObject {
				baseObject = map[string]any{}
			}
			compacted := compactOverrideObject(baseObject, overrideObject, childPath)
			if len(compacted) > 0 {
				result[key] = compacted
			}
			continue
		}
		if !baseExists || !reflect.DeepEqual(baseValue, value) {
			result[key] = value
		}
	}
	return result
}

func mergeConfigObject(target, override map[string]any) map[string]any {
	for key, value := range override {
		if value == nil {
			delete(target, key)
			continue
		}
		overrideObject, isObject := value.(map[string]any)
		if !isObject {
			target[key] = value
			continue
		}
		targetObject, _ := target[key].(map[string]any)
		if targetObject == nil {
			targetObject = map[string]any{}
		}
		target[key] = mergeConfigObject(targetObject, overrideObject)
	}
	return target
}
