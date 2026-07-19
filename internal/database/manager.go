// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"codebridge/internal/config"
)

type Manager struct {
	mu      sync.RWMutex
	handles map[string]*Handle
}

func NewManager(cfg config.DatabaseConfig, constructors map[string]Constructor) (*Manager, error) {
	manager := &Manager{handles: map[string]*Handle{}}
	if !cfg.Enabled {
		return manager, nil
	}
	aliases := make([]string, 0, len(cfg.Connections))
	for alias := range cfg.Connections {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		connectionConfig := cfg.Connections[alias]
		credential := ""
		if connectionConfig.CredentialRef.Provider == "env" {
			credential = os.Getenv(connectionConfig.CredentialRef.Name)
		}
		if credential == "" {
			err := fmt.Errorf("credential is not configured")
			if connectionConfig.Required {
				manager.Close()
				return nil, fmt.Errorf("required database connection %q is unavailable: %w", alias, err)
			}
			manager.handles[alias] = newHandle(alias, connectionConfig, nil, err)
			continue
		}
		constructor := constructors[connectionConfig.Driver]
		if constructor == nil {
			manager.Close()
			return nil, fmt.Errorf("unsupported database driver %q", connectionConfig.Driver)
		}
		connection, err := constructor(alias, connectionConfig, credential)
		if err != nil {
			if connectionConfig.Required {
				manager.Close()
				return nil, fmt.Errorf("required database connection %q failed to initialize: %w", alias, err)
			}
			manager.handles[alias] = newHandle(alias, connectionConfig, nil, err)
			continue
		}
		handle := newHandle(alias, connectionConfig, connection, nil)
		manager.handles[alias] = handle
		if connectionConfig.Required {
			ctx, cancel := timeoutContext(context.Background(), connectionConfig.Limits.QueryTimeoutMS)
			health := connection.Health(ctx)
			cancel()
			if !health.Available {
				manager.Close()
				return nil, fmt.Errorf("required database connection %q is unavailable: %s", alias, health.Error)
			}
		}
	}
	return manager, nil
}

func (m *Manager) Enabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.handles) > 0
}

func (m *Manager) Aliases() []string {
	m.mu.RLock()
	aliases := make([]string, 0, len(m.handles))
	for alias := range m.handles {
		aliases = append(aliases, alias)
	}
	m.mu.RUnlock()
	sort.Strings(aliases)
	return aliases
}

func (m *Manager) List(ctx context.Context, checkHealth bool) []ConnectionSummary {
	aliases := m.Aliases()
	results := make([]ConnectionSummary, 0, len(aliases))
	for _, alias := range aliases {
		handle, _ := m.resolve(alias)
		summary := summaryFor(handle)
		if checkHealth && handle.Connection != nil {
			healthCtx, cancel := timeoutContext(ctx, handle.Config.Limits.QueryTimeoutMS)
			health := handle.Connection.Health(healthCtx)
			cancel()
			summary.Available = health.Available
			summary.Error = health.Error
		}
		results = append(results, summary)
	}
	return results
}

func (m *Manager) Query(ctx context.Context, alias string, request QueryRequest) (QueryResult, ConnectionSummary, error) {
	handle, err := m.resolve(alias)
	if err != nil {
		return QueryResult{}, ConnectionSummary{}, err
	}
	query, queryHash, err := ValidateReadOnlySQL(request.SQL)
	if err != nil {
		return QueryResult{}, summaryFor(handle), err
	}
	if err := CheckQueryAccess(query, handle.Config.Access); err != nil {
		return QueryResult{}, summaryFor(handle), err
	}
	if err := handle.Connection.ValidateReadOnlySQL(query); err != nil {
		return QueryResult{}, summaryFor(handle), fmt.Errorf("database driver rejected query: %s", SanitizeError(err))
	}
	request.SQL = query
	request.MaxRows = clampRows(request.MaxRows, handle.Config.Limits.MaxRows)
	if err := handle.acquire(ctx); err != nil {
		return QueryResult{}, summaryFor(handle), err
	}
	defer handle.release()
	queryCtx, cancel := timeoutContext(ctx, handle.Config.Limits.QueryTimeoutMS)
	defer cancel()
	result, err := handle.Connection.Query(queryCtx, request)
	result.QueryHash = queryHash
	if err != nil {
		return result, summaryFor(handle), fmt.Errorf("database query failed: %s", SanitizeError(err))
	}
	return result, summaryFor(handle), nil
}

func (m *Manager) Explain(ctx context.Context, alias string, request QueryRequest) (QueryResult, ConnectionSummary, error) {
	handle, err := m.resolve(alias)
	if err != nil {
		return QueryResult{}, ConnectionSummary{}, err
	}
	query, queryHash, err := ValidateReadOnlySQL(request.SQL)
	if err != nil {
		return QueryResult{}, summaryFor(handle), err
	}
	if err := CheckQueryAccess(query, handle.Config.Access); err != nil {
		return QueryResult{}, summaryFor(handle), err
	}
	if err := handle.Connection.ValidateReadOnlySQL(query); err != nil {
		return QueryResult{}, summaryFor(handle), fmt.Errorf("database driver rejected query: %s", SanitizeError(err))
	}
	request.SQL = query
	request.MaxRows = 1
	if err := handle.acquire(ctx); err != nil {
		return QueryResult{}, summaryFor(handle), err
	}
	defer handle.release()
	queryCtx, cancel := timeoutContext(ctx, handle.Config.Limits.QueryTimeoutMS)
	defer cancel()
	result, err := handle.Connection.Explain(queryCtx, request)
	result.QueryHash = queryHash
	if err != nil {
		return result, summaryFor(handle), fmt.Errorf("database explain failed: %s", SanitizeError(err))
	}
	return result, summaryFor(handle), nil
}

func (m *Manager) Describe(ctx context.Context, alias string, request DescribeRequest) (DescribeResult, ConnectionSummary, error) {
	handle, err := m.resolve(alias)
	if err != nil {
		return DescribeResult{}, ConnectionSummary{}, err
	}
	if err := CheckDescribeAccess(request, handle.Config.Access); err != nil {
		return DescribeResult{}, summaryFor(handle), err
	}
	if err := handle.acquire(ctx); err != nil {
		return DescribeResult{}, summaryFor(handle), err
	}
	defer handle.release()
	queryCtx, cancel := timeoutContext(ctx, handle.Config.Limits.QueryTimeoutMS)
	defer cancel()
	result, err := handle.Connection.Describe(queryCtx, request)
	if err != nil {
		return result, summaryFor(handle), fmt.Errorf("database describe failed: %s", SanitizeError(err))
	}
	return result, summaryFor(handle), nil
}

func (m *Manager) resolve(alias string) (*Handle, error) {
	alias = strings.TrimSpace(alias)
	m.mu.RLock()
	handle := m.handles[alias]
	m.mu.RUnlock()
	if handle == nil {
		return nil, fmt.Errorf("unknown database alias %q; available aliases: %s", alias, strings.Join(m.Aliases(), ", "))
	}
	if handle.Connection == nil {
		return nil, fmt.Errorf("database alias %q is unavailable: %s", alias, SanitizeError(handle.InitError))
	}
	return handle, nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, handle := range m.handles {
		if handle.Connection != nil {
			_ = handle.Connection.Close()
		}
	}
}

func summaryFor(handle *Handle) ConnectionSummary {
	capabilities := []string{"describe", "query", "explain"}
	return ConnectionSummary{
		Alias: handle.Alias, Driver: handle.Config.Driver, Environment: handle.Config.Environment,
		Access: handle.Config.Access.Mode, Capabilities: capabilities,
		Available: handle.Connection != nil && handle.InitError == nil,
		Error:     ternaryError(handle.InitError),
	}
}

func clampRows(requested, hardLimit int) int {
	if requested <= 0 || requested > hardLimit {
		return hardLimit
	}
	return requested
}

func ternaryError(err error) string {
	if err == nil {
		return ""
	}
	return SanitizeError(err)
}
