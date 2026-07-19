// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package factory

import (
	"sort"
	"strings"
	"sync"

	"codebridge/internal/config"
	"codebridge/internal/database"
	mysqldatabase "codebridge/internal/database/mysql"
	"codebridge/internal/database/postgres"
)

var (
	registryMu sync.RWMutex
	registry   = map[string]database.Constructor{}
)

func init() {
	Register("mysql", mysqldatabase.New)
	Register("postgres", postgres.New)
}

func Register(name string, constructor database.Constructor) {
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

func New(cfg config.DatabaseConfig) (*database.Manager, error) {
	registryMu.RLock()
	constructors := make(map[string]database.Constructor, len(registry))
	for name, constructor := range registry {
		constructors[name] = constructor
	}
	registryMu.RUnlock()
	return database.NewManager(cfg, constructors)
}
