// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/database/credential"
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
		credentialValue, resolveErr := credential.Resolve(context.Background(), connectionConfig.CredentialRef)
		if resolveErr != nil {
			err := fmt.Errorf("credential provider %q is unavailable", connectionConfig.CredentialRef.Provider)
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
		connection, err := constructor(alias, connectionConfig, credentialValue)
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
		m.mu.RLock()
		handle := m.handles[alias]
		m.mu.RUnlock()
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

func (m *Manager) Query(ctx context.Context, alias string, request QueryRequest) (result QueryResult, summary ConnectionSummary, err error) {
	handle, err := m.resolve(alias)
	if err != nil {
		return QueryResult{}, ConnectionSummary{}, err
	}
	handle.queries.Add(1)
	started := time.Now()
	defer func() { handle.record(time.Since(started), result.Truncated, err) }()
	summary = summaryFor(handle)

	query, queryHash, err := ValidateReadOnlySQL(request.SQL)
	if err != nil {
		return QueryResult{}, summary, err
	}
	if err = CheckQueryAccess(query, handle.Config.Access); err != nil {
		return QueryResult{}, summary, err
	}
	if err = handle.Connection.ValidateReadOnlySQL(query); err != nil {
		return QueryResult{}, summary, fmt.Errorf("database driver rejected query: %s", SanitizeError(err))
	}
	request.SQL = query
	request.MaxRows = clampRows(request.MaxRows, handle.Config.Limits.MaxRows)
	if err = handle.acquire(ctx); err != nil {
		return QueryResult{}, summary, err
	}
	defer handle.release()
	queryCtx, cancel := timeoutContext(ctx, handle.Config.Limits.QueryTimeoutMS)
	defer cancel()
	result, err = handle.Connection.Query(queryCtx, request)
	result.QueryHash = queryHash
	if err != nil {
		return result, summary, fmt.Errorf("database query failed: %s", SanitizeError(err))
	}
	return result, summary, nil
}

func (m *Manager) Explain(ctx context.Context, alias string, request QueryRequest) (result QueryResult, summary ConnectionSummary, err error) {
	handle, err := m.resolve(alias)
	if err != nil {
		return QueryResult{}, ConnectionSummary{}, err
	}
	handle.explains.Add(1)
	started := time.Now()
	defer func() { handle.record(time.Since(started), result.Truncated, err) }()
	summary = summaryFor(handle)

	query, queryHash, err := ValidateReadOnlySQL(request.SQL)
	if err != nil {
		return QueryResult{}, summary, err
	}
	if err = CheckQueryAccess(query, handle.Config.Access); err != nil {
		return QueryResult{}, summary, err
	}
	if err = handle.Connection.ValidateReadOnlySQL(query); err != nil {
		return QueryResult{}, summary, fmt.Errorf("database driver rejected query: %s", SanitizeError(err))
	}
	request.SQL = query
	request.MaxRows = 1
	if err = handle.acquire(ctx); err != nil {
		return QueryResult{}, summary, err
	}
	defer handle.release()
	queryCtx, cancel := timeoutContext(ctx, handle.Config.Limits.QueryTimeoutMS)
	defer cancel()
	result, err = handle.Connection.Explain(queryCtx, request)
	result.QueryHash = queryHash
	if err != nil {
		return result, summary, fmt.Errorf("database explain failed: %s", SanitizeError(err))
	}
	return result, summary, nil
}

func (m *Manager) Describe(ctx context.Context, alias string, request DescribeRequest) (result DescribeResult, summary ConnectionSummary, err error) {
	handle, err := m.resolve(alias)
	if err != nil {
		return DescribeResult{}, ConnectionSummary{}, err
	}
	handle.describes.Add(1)
	started := time.Now()
	defer func() { handle.record(time.Since(started), result.Truncated, err) }()
	summary = summaryFor(handle)

	if err = CheckDescribeAccess(request, handle.Config.Access); err != nil {
		return DescribeResult{}, summary, err
	}
	if err = handle.acquire(ctx); err != nil {
		return DescribeResult{}, summary, err
	}
	defer handle.release()
	queryCtx, cancel := timeoutContext(ctx, handle.Config.Limits.QueryTimeoutMS)
	defer cancel()
	result, err = handle.Connection.Describe(queryCtx, request)
	if err != nil {
		return result, summary, fmt.Errorf("database describe failed: %s", SanitizeError(err))
	}
	return result, summary, nil
}

func (m *Manager) PreviewMutation(ctx context.Context, alias string, request MutationRequest) (preview MutationPreview, summary ConnectionSummary, err error) {
	handle, err := m.resolveMutation(alias, request)
	if err != nil {
		return MutationPreview{}, ConnectionSummary{}, err
	}
	handle.mutationPreviews.Add(1)
	started := time.Now()
	defer func() { handle.record(time.Since(started), false, err) }()
	summary = summaryFor(handle)
	connection := handle.Connection.(MutationConnection)
	if err = handle.acquire(ctx); err != nil {
		return MutationPreview{}, summary, err
	}
	defer handle.release()
	mutationCtx, cancel := timeoutContext(ctx, handle.Config.Limits.QueryTimeoutMS)
	defer cancel()
	preview, err = connection.PreviewMutation(mutationCtx, request)
	if err != nil {
		return preview, summary, fmt.Errorf("database mutation preview failed: %s", SanitizeError(err))
	}
	return preview, summary, nil
}

func (m *Manager) Mutate(ctx context.Context, alias string, request MutationRequest) (result MutationResult, summary ConnectionSummary, err error) {
	handle, err := m.resolveMutation(alias, request)
	if err != nil {
		return MutationResult{}, ConnectionSummary{}, err
	}
	handle.mutations.Add(1)
	started := time.Now()
	defer func() { handle.record(time.Since(started), false, err) }()
	summary = summaryFor(handle)
	connection := handle.Connection.(MutationConnection)
	if err = handle.acquire(ctx); err != nil {
		return MutationResult{}, summary, err
	}
	defer handle.release()
	mutationCtx, cancel := timeoutContext(ctx, handle.Config.Limits.QueryTimeoutMS)
	defer cancel()
	result, err = connection.Mutate(mutationCtx, request)
	if err != nil {
		return result, summary, fmt.Errorf("database mutation failed: %s", SanitizeError(err))
	}
	if result.AffectedRows > 0 {
		handle.affectedRows.Add(uint64(result.AffectedRows))
	}
	return result, summary, nil
}

func (m *Manager) resolveMutation(alias string, request MutationRequest) (*Handle, error) {
	handle, err := m.resolve(alias)
	if err != nil {
		return nil, err
	}
	if handle.Config.Environment == "prod" || handle.Config.Environment == "production" {
		return nil, fmt.Errorf("structured mutations are not allowed for production database alias %q", alias)
	}
	if handle.Config.Access.Mode != "read-write" {
		return nil, fmt.Errorf("database alias %q is not configured for read-write access", alias)
	}
	if err := CheckDescribeAccess(DescribeRequest{Schema: request.Schema, Table: request.Table}, handle.Config.Access); err != nil {
		return nil, err
	}
	connection, ok := handle.Connection.(MutationConnection)
	if !ok || !connection.SupportsMutation() {
		return nil, fmt.Errorf("database driver %q does not support structured mutations", handle.Config.Driver)
	}
	return handle, nil
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
	if connection, ok := handle.Connection.(MutationConnection); ok && connection.SupportsMutation() &&
		handle.Config.Access.Mode == "read-write" && handle.Config.Environment != "prod" && handle.Config.Environment != "production" {
		capabilities = append(capabilities, "mutation_preview", "mutate")
	}
	return ConnectionSummary{
		Alias: handle.Alias, Driver: handle.Config.Driver, Environment: handle.Config.Environment,
		Access: handle.Config.Access.Mode, Capabilities: capabilities,
		Available: handle.Connection != nil && handle.InitError == nil,
		Error:     ternaryError(handle.InitError), Metrics: handle.metrics(),
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
