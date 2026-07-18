// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package factory

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/memory"
	"codebridge/internal/memory/agentmemory"
	"codebridge/internal/memory/noop"
)

type Constructor func(config.MemoryConfig) (memory.Provider, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Constructor{}
)

func init() {
	Register("none", func(config.MemoryConfig) (memory.Provider, error) { return noop.New(), nil })
	Register("agentmemory", func(cfg config.MemoryConfig) (memory.Provider, error) {
		secret := ""
		if cfg.SecretEnv != "" {
			secret = os.Getenv(cfg.SecretEnv)
		}
		return agentmemory.New(agentmemory.Config{
			Endpoint: cfg.Endpoint, Secret: secret,
			Timeout: time.Duration(cfg.TimeoutMS) * time.Millisecond,
			Options: cfg.Options,
		})
	})
}

func Register(name string, constructor Constructor) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || constructor == nil {
		return
	}
	registryMu.Lock()
	registry[name] = constructor
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

func New(cfg config.MemoryConfig) (memory.Provider, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if !cfg.Enabled || provider == "" || provider == "none" {
		return noop.New(), nil
	}
	registryMu.RLock()
	constructor := registry[provider]
	registryMu.RUnlock()
	if constructor == nil {
		return nil, fmt.Errorf("unsupported memory provider %q", cfg.Provider)
	}
	return constructor(cfg)
}
