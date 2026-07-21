// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspaceregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"codebridge/internal/config"
)

const CurrentVersion = 2

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

type Registration struct {
	ID         string    `json:"id"`
	Workspace  string    `json:"workspace"`
	ConfigPath string    `json:"configPath"`
	DataDir    string    `json:"dataDir"`
	Port       int       `json:"port,omitempty"` // Legacy phase-one field; named endpoints share the daemon port.
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type Registry struct {
	Version    int                     `json:"version"`
	Workspaces map[string]Registration `json:"workspaces"`
}

func ValidateID(id string) error {
	id = NormalizeID(id)
	if id == "default" {
		return errors.New("workspace id \"default\" is reserved")
	}
	if !idPattern.MatchString(id) {
		return errors.New("workspace id must match [a-z0-9][a-z0-9_-]{0,31}")
	}
	return nil
}

func NormalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func Path() string {
	if value := strings.TrimSpace(os.Getenv("CODEBRIDGE_WORKSPACE_REGISTRY_PATH")); value != "" {
		return value
	}
	return filepath.Join(config.AppConfigDir(), "workspaces.json")
}

func ConfigPath(id string) string {
	return filepath.Join(config.AppConfigDir(), "workspaces", NormalizeID(id), "config.json")
}

func DataDir(id string) string {
	return filepath.Join(config.AppDataDir(), "instances", NormalizeID(id))
}

func Empty() Registry {
	return Registry{Version: CurrentVersion, Workspaces: map[string]Registration{}}
}

func Load() (Registry, error) {
	registry := Empty()
	raw, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return registry, nil
	}
	if err != nil {
		return registry, err
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		return registry, fmt.Errorf("parse workspace registry: %w", err)
	}
	previousVersion := registry.Version
	if previousVersion == 0 {
		previousVersion = 1
	}
	if previousVersion > CurrentVersion {
		return registry, fmt.Errorf("unsupported workspace registry version %d", registry.Version)
	}
	if registry.Workspaces == nil {
		registry.Workspaces = map[string]Registration{}
	}
	migrated := map[string]Registration{}
	for key, entry := range registry.Workspaces {
		id := NormalizeID(entry.ID)
		if id == "" {
			id = NormalizeID(key)
		}
		if err := ValidateID(id); err != nil {
			return registry, fmt.Errorf("workspace registry entry %q: %w", key, err)
		}
		entry.ID = id
		if entry.ConfigPath == "" {
			entry.ConfigPath = ConfigPath(id)
		}
		if entry.DataDir == "" {
			entry.DataDir = DataDir(id)
		}
		if previousVersion < 2 {
			entry.Enabled = true
		}
		migrated[id] = entry
	}
	registry.Version = CurrentVersion
	registry.Workspaces = migrated
	if err := validateUniquePaths(registry.Workspaces); err != nil {
		return registry, err
	}
	return registry, nil
}

func Save(registry Registry) error {
	registry.Version = CurrentVersion
	if registry.Workspaces == nil {
		registry.Workspaces = map[string]Registration{}
	}
	normalized := make(map[string]Registration, len(registry.Workspaces))
	for key, entry := range registry.Workspaces {
		id := NormalizeID(entry.ID)
		if id == "" {
			id = NormalizeID(key)
		}
		if err := ValidateID(id); err != nil {
			return fmt.Errorf("workspace registry entry %q: %w", key, err)
		}
		entry.ID = id
		if entry.ConfigPath == "" {
			entry.ConfigPath = ConfigPath(id)
		}
		if entry.DataDir == "" {
			entry.DataDir = DataDir(id)
		}
		normalized[id] = entry
	}
	if err := validateUniquePaths(normalized); err != nil {
		return err
	}
	registry.Workspaces = normalized
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".workspaces-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(raw, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func SortedIDs(registry Registry) []string {
	ids := make([]string, 0, len(registry.Workspaces))
	for id := range registry.Workspaces {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func Enabled(registry Registry) []Registration {
	ids := SortedIDs(registry)
	entries := make([]Registration, 0, len(ids))
	for _, id := range ids {
		entry := registry.Workspaces[id]
		if entry.Enabled {
			entries = append(entries, entry)
		}
	}
	return entries
}

func validateUniquePaths(entries map[string]Registration) error {
	configOwners := map[string]string{}
	dataOwners := map[string]string{}
	for id, entry := range entries {
		configKey := comparablePath(entry.ConfigPath)
		if owner := configOwners[configKey]; owner != "" && owner != id {
			return fmt.Errorf("workspaces %q and %q share config path %s", owner, id, entry.ConfigPath)
		}
		configOwners[configKey] = id

		dataKey := comparablePath(entry.DataDir)
		if owner := dataOwners[dataKey]; owner != "" && owner != id {
			return fmt.Errorf("workspaces %q and %q share data directory %s", owner, id, entry.DataDir)
		}
		dataOwners[dataKey] = id
	}
	return nil
}

func comparablePath(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

// Fingerprint returns a stable digest of the registry and every registered
// workspace config. The daemon supervisor uses it so add/remove/enable/config
// changes cause a restart instead of silently reusing a stale runtime set.
func Fingerprint() (string, error) {
	registry, err := Load()
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(fmt.Sprintf("version:%d\n", registry.Version)))
	for _, id := range SortedIDs(registry) {
		entry := registry.Workspaces[id]
		raw, readErr := os.ReadFile(entry.ConfigPath)
		if errors.Is(readErr, os.ErrNotExist) {
			raw = nil
		} else if readErr != nil {
			return "", readErr
		}
		material, _ := json.Marshal(map[string]any{
			"entry":         entry,
			"config_sha256": fmt.Sprintf("%x", sha256.Sum256(raw)),
		})
		_, _ = hash.Write(material)
		_, _ = hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil)[:16]), nil
}
