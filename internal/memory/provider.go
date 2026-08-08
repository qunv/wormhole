// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import "context"

type Capabilities struct {
	Search         bool `json:"search"`
	Context        bool `json:"context"`
	Remember       bool `json:"remember"`
	Forget         bool `json:"forget"`
	Observe        bool `json:"observe"`
	Sessions       bool `json:"sessions"`
	KnowledgeGraph bool `json:"knowledgeGraph"`
	Provenance     bool `json:"provenance"`
	Export         bool `json:"export"`
	Import         bool `json:"import"`
}

type HealthResult struct {
	Provider     string         `json:"provider"`
	Enabled      bool           `json:"enabled"`
	Available    bool           `json:"available"`
	Endpoint     string         `json:"endpoint,omitempty"`
	Capabilities Capabilities   `json:"capabilities"`
	Details      map[string]any `json:"details,omitempty"`
	Error        string         `json:"error,omitempty"`
}

type SearchRequest struct {
	Query       string
	Project     string
	CWD         string
	AgentID     string
	Limit       int
	Format      string
	TokenBudget int
}

type ContextRequest struct {
	Query       string
	Project     string
	CWD         string
	AgentID     string
	SessionID   string
	Limit       int
	TokenBudget int
}

type RememberRequest struct {
	Content   string
	Kind      string
	Project   string
	AgentID   string
	SessionID string
	Concepts  []string
	Files     []string
	TTLDays   int
}

type ForgetRequest struct {
	MemoryID       string
	SessionID      string
	ObservationIDs []string
}

type ObservationRequest struct {
	HookType  string
	SessionID string
	Project   string
	CWD       string
	Timestamp string
	Data      any
}

// Item is Wormhole's provider-neutral representation of a retrieved memory.
// ProviderID preserves the backend identifier without making MCP clients depend
// on a provider-specific response schema.
type Item struct {
	ID         string         `json:"id,omitempty"`
	ProviderID string         `json:"provider_id,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Content    string         `json:"content,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	Score      float64        `json:"score,omitempty"`
	Project    string         `json:"project,omitempty"`
	AgentID    string         `json:"agent_id,omitempty"`
	Concepts   []string       `json:"concepts,omitempty"`
	Files      []string       `json:"files,omitempty"`
	CreatedAt  string         `json:"created_at,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type SearchResult struct {
	Provider  string `json:"provider"`
	Project   string `json:"project,omitempty"`
	Query     string `json:"query,omitempty"`
	Format    string `json:"format,omitempty"`
	Context   string `json:"context,omitempty"`
	Memories  []Item `json:"memories"`
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated,omitempty"`
}

type ContextResult struct {
	Provider    string   `json:"provider"`
	Project     string   `json:"project,omitempty"`
	Query       string   `json:"query,omitempty"`
	Text        string   `json:"text,omitempty"`
	Memories    []Item   `json:"memories,omitempty"`
	Count       int      `json:"count"`
	TokenBudget int      `json:"token_budget,omitempty"`
	Truncated   bool     `json:"truncated,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

type RememberResult struct {
	Provider   string `json:"provider"`
	Project    string `json:"project,omitempty"`
	ID         string `json:"id,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
	Stored     bool   `json:"stored"`
}

type ForgetResult struct {
	Provider string   `json:"provider"`
	Deleted  int      `json:"deleted"`
	IDs      []string `json:"ids,omitempty"`
}

type ExportRequest struct {
	Project string
	AgentID string
}

type ExportResult struct {
	SchemaVersion int    `json:"schema_version"`
	Provider      string `json:"provider"`
	Project       string `json:"project,omitempty"`
	Memories      []Item `json:"memories"`
	Count         int    `json:"count"`
}

type ImportRequest struct {
	Project  string
	AgentID  string
	Memories []Item
}

type ImportResult struct {
	Provider string   `json:"provider"`
	Project  string   `json:"project,omitempty"`
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

type Provider interface {
	Name() string
	Capabilities() Capabilities
	Health(context.Context) HealthResult
	Search(context.Context, SearchRequest) (SearchResult, error)
	Context(context.Context, ContextRequest) (ContextResult, error)
	Remember(context.Context, RememberRequest) (RememberResult, error)
	Observe(context.Context, ObservationRequest) error
	Forget(context.Context, ForgetRequest) (ForgetResult, error)
	Close() error
}

// ConcurrencySafeProvider is an optional marker for providers that support
// concurrent method calls. Providers without this marker are serialized when
// shared across workspace runtimes.
type ConcurrencySafeProvider interface {
	ConcurrencySafe() bool
}

type Exporter interface {
	Export(context.Context, ExportRequest) (ExportResult, error)
}

type Importer interface {
	Import(context.Context, ImportRequest) (ImportResult, error)
}
