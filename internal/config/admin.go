// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Prepare normalizes and validates a configuration before removing runtime
// bearer and approval tokens from the returned value.
func Prepare(cfg Config) (Config, error) {
	normalize(&cfg)
	err := cfg.Validate(false)
	cfg.AuthToken = ""
	cfg.ApprovalToken = ""
	return cfg, err
}

// ParseJSON strictly parses and validates one complete configuration document.
// Duplicate and unknown keys are rejected.
func ParseJSON(raw []byte, source string) (Config, error) {
	return ParseJSONWithRuntimeSecrets(raw, source, "", "")
}

// ParseJSONWithRuntimeSecrets validates a non-secret JSON document with the
// runtime-only bearer and approval values currently owned by the daemon. Those
// values are removed before the parsed configuration is returned.
func ParseJSONWithRuntimeSecrets(raw []byte, source, authToken, approvalToken string) (Config, error) {
	cfg, err := parseAdminConfigJSON(raw, source)
	if err != nil {
		return Config{}, err
	}
	cfg.AuthToken = authToken
	cfg.ApprovalToken = approvalToken
	return Prepare(cfg)
}

// LoadFileForEditing loads a strict non-secret configuration while using
// runtime-only tokens solely for validation. This supports authenticated
// non-loopback listeners without exposing or persisting their bearer values.
func LoadFileForEditing(path, authToken, approvalToken string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	if err == nil {
		cfg, err = parseAdminConfigJSON(raw, path)
		if err != nil {
			return cfg, err
		}
	} else {
		normalize(&cfg)
	}
	cfg.AuthToken = authToken
	cfg.ApprovalToken = approvalToken
	return Prepare(cfg)
}

func parseAdminConfigJSON(raw []byte, source string) (Config, error) {
	object, err := decodeConfigObject(raw, source)
	if err != nil {
		return Config{}, err
	}
	cleanRaw, err := json.Marshal(object)
	if err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", source, err)
	}
	cfg := Default()
	if err := json.Unmarshal(cleanRaw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", source, err)
	}
	migrateLegacyConfigPaths(&cfg)
	normalize(&cfg)
	return cfg, nil
}

// ParseOverrideJSON strictly parses one workspace override document while
// preserving null deletion markers and array replacement semantics.
func ParseOverrideJSON(raw []byte, source string) (map[string]any, error) {
	return decodeWorkspaceOverrideObject(raw, source)
}

// DotEnvKeys returns the sorted variable names stored in a dotenv file without
// exposing any values.
func DotEnvKeys(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	values := ParseDotEnv(string(raw))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

// UpdateDotEnv atomically sets and deletes dotenv values. A nil value deletes
// the key. Variable names are validated and secret values are never returned.
func UpdateDotEnv(path string, updates map[string]*string) error {
	if len(updates) == 0 {
		return nil
	}
	for key := range updates {
		if !envNamePattern.MatchString(key) {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	text := string(raw)
	setValues := map[string]string{}
	deleteKeys := make([]string, 0)
	for key, value := range updates {
		if value == nil {
			deleteKeys = append(deleteKeys, key)
			continue
		}
		if strings.IndexByte(*value, 0) >= 0 {
			return fmt.Errorf("environment variable %q contains a NUL byte", key)
		}
		setValues[key] = *value
	}
	if len(deleteKeys) > 0 {
		text = RemoveDotEnvKeys(text, deleteKeys...)
	}
	if len(setValues) > 0 {
		text = MergeDotEnv(text, setValues)
	}
	if text == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWrite(path, []byte(text), 0o600)
}
