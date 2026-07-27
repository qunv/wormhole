// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
	return decodeConfigObject(raw, path)
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
	raw, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(raw, '\n'), 0o600)
}

func decodeConfigObject(raw []byte, source string) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", source, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("parse config %s: trailing JSON value", source)
		}
		return nil, fmt.Errorf("parse config %s: %w", source, err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parse config %s: top-level value must be an object", source)
	}
	return object, nil
}

func cloneConfigObject(value map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return decodeConfigObject(raw, "override")
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
