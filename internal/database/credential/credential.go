// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package credential resolves database credentials from named external
// providers without exposing secret values to configuration or MCP responses.
package credential

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"codebridge/internal/config"
)

const maxCredentialBytes = 64 << 10

type Resolver func(context.Context, config.CredentialReference) (string, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Resolver{}
)

func init() {
	Register("env", resolveEnv)
	Register("file", resolveFile)
}

func Register(name string, resolver Resolver) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || resolver == nil {
		return
	}
	registryMu.Lock()
	registry[name] = resolver
	registryMu.Unlock()
}

func Names() []string {
	registryMu.RLock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	registryMu.RUnlock()
	sort.Strings(names)
	return names
}

func Resolve(ctx context.Context, reference config.CredentialReference) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(reference.Provider))
	registryMu.RLock()
	resolver := registry[provider]
	registryMu.RUnlock()
	if resolver == nil {
		return "", fmt.Errorf("unsupported database credential provider %q", provider)
	}
	value, err := resolver(ctx, reference)
	if err != nil {
		return "", fmt.Errorf("resolve database credential through %s: %w", provider, err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("database credential is empty")
	}
	if len(value) > maxCredentialBytes {
		return "", fmt.Errorf("database credential exceeds %d bytes", maxCredentialBytes)
	}
	return value, nil
}

func resolveEnv(_ context.Context, reference config.CredentialReference) (string, error) {
	name := strings.TrimSpace(reference.Name)
	if name == "" {
		return "", fmt.Errorf("environment variable name is required")
	}
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("environment variable %s is not configured", name)
	}
	return value, nil
}

func resolveFile(ctx context.Context, reference config.CredentialReference) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	name := strings.TrimSpace(reference.Name)
	if name == "" {
		return "", fmt.Errorf("credential file path is required")
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(config.AppConfigDir(), path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve credential file path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve credential file symlinks: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("stat credential file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("credential file must be a regular file")
	}
	if info.Size() > maxCredentialBytes {
		return "", fmt.Errorf("credential file exceeds %d bytes", maxCredentialBytes)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("credential file must not be writable by group or others")
	}
	raw, err := os.ReadFile(canonical)
	if err != nil {
		return "", err
	}
	if len(raw) > maxCredentialBytes {
		return "", fmt.Errorf("credential file exceeds %d bytes", maxCredentialBytes)
	}
	return string(raw), nil
}
