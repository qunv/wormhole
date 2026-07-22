// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agentmemory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codebridge/internal/memory"
)

const defaultMaxResponseBytes = 4 << 20

type Config struct {
	Endpoint string
	Secret   string
	Timeout  time.Duration
	Options  map[string]any
}

func anySlice(value any) []any {
	if entries, ok := value.([]any); ok {
		return entries
	}
	return nil
}

type Provider struct {
	endpoint         string
	secret           string
	client           *http.Client
	healthPath       string
	configPath       string
	searchPath       string
	contextPath      string
	rememberPath     string
	observePath      string
	forgetPath       string
	exportPath       string
	contextFallback  bool
	maxResponseBytes int64
}

type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("agentmemory returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("agentmemory returned HTTP %d: %s", e.StatusCode, e.Message)
}

func New(cfg Config) (*Provider, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:3111"
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, fmt.Errorf("agentmemory endpoint must use http or https: %s", endpoint)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}
	maxResponse := int64(optionInt(cfg.Options, "maxResponseBytes", defaultMaxResponseBytes))
	if maxResponse <= 0 {
		maxResponse = defaultMaxResponseBytes
	}
	return &Provider{
		endpoint: endpoint, secret: cfg.Secret, client: &http.Client{Timeout: cfg.Timeout},
		healthPath:       optionPath(cfg.Options, "healthPath", "/agentmemory/health"),
		configPath:       optionPath(cfg.Options, "configPath", "/agentmemory/config/flags"),
		searchPath:       optionPath(cfg.Options, "searchPath", "/agentmemory/search"),
		contextPath:      optionPath(cfg.Options, "contextPath", "/agentmemory/context"),
		rememberPath:     optionPath(cfg.Options, "rememberPath", "/agentmemory/remember"),
		observePath:      optionPath(cfg.Options, "observePath", "/agentmemory/observe"),
		forgetPath:       optionPath(cfg.Options, "forgetPath", "/agentmemory/forget"),
		exportPath:       optionPath(cfg.Options, "exportPath", "/agentmemory/export"),
		contextFallback:  optionBool(cfg.Options, "contextFallback", true),
		maxResponseBytes: maxResponse,
	}, nil
}

func (*Provider) Name() string          { return "agentmemory" }
func (*Provider) ConcurrencySafe() bool { return true }

func (*Provider) Capabilities() memory.Capabilities {
	return memory.Capabilities{
		Search: true, Context: true, Remember: true, Forget: true,
		Observe: true, Sessions: true, KnowledgeGraph: true, Provenance: true,
		Export: true, Import: true,
	}
}

func (p *Provider) Health(ctx context.Context) memory.HealthResult {
	result := memory.HealthResult{
		Provider: "agentmemory", Enabled: true, Endpoint: p.endpoint,
		Capabilities: p.Capabilities(),
	}
	var body map[string]any
	if err := p.call(ctx, http.MethodGet, p.healthPath, nil, &body); err != nil {
		result.Error = err.Error()
		return result
	}
	result.Available = true
	result.Capabilities = capabilitiesFromBody(result.Capabilities, body)
	result.Details = map[string]any{}
	for _, key := range []string{"status", "service", "version", "viewerPort", "viewerSkipped", "circuitBreaker"} {
		if value, exists := body[key]; exists {
			result.Details[key] = value
		}
	}
	var flags map[string]any
	if err := p.call(ctx, http.MethodGet, p.configPath, nil, &flags); err == nil {
		result.Capabilities = capabilitiesFromFlags(result.Capabilities, flags)
		for _, key := range []string{"version", "provider", "embeddingProvider"} {
			if value, exists := flags[key]; exists {
				result.Details[key] = value
			}
		}
	}
	return result
}

func (p *Provider) Search(ctx context.Context, req memory.SearchRequest) (memory.SearchResult, error) {
	payload := map[string]any{"query": req.Query}
	putOptional(payload, "project", req.Project)
	putOptional(payload, "cwd", req.CWD)
	putOptional(payload, "agentId", req.AgentID)
	putPositive(payload, "limit", req.Limit)
	putOptional(payload, "format", req.Format)
	putPositive(payload, "token_budget", req.TokenBudget)
	var body any
	if err := p.call(ctx, http.MethodPost, p.searchPath, payload, &body); err != nil {
		return memory.SearchResult{}, err
	}
	return normalizeSearch(p.Name(), req, body), nil
}

func (p *Provider) Context(ctx context.Context, req memory.ContextRequest) (memory.ContextResult, error) {
	payload := map[string]any{}
	putOptional(payload, "sessionId", req.SessionID)
	putOptional(payload, "project", req.Project)
	putPositive(payload, "budget", req.TokenBudget)
	var body any
	var result memory.ContextResult
	if err := p.call(ctx, http.MethodPost, p.contextPath, payload, &body); err == nil {
		result = normalizeContext(p.Name(), req, body)
	} else if !p.contextFallback || !isUnsupportedContext(err) {
		return memory.ContextResult{}, err
	} else {
		result = memory.ContextResult{
			Provider: p.Name(), Project: req.Project, Query: req.Query,
			TokenBudget: req.TokenBudget,
			Warnings:    []string{"agentmemory context endpoint unavailable; used search fallback"},
		}
	}
	if strings.TrimSpace(req.Query) == "" {
		return result, nil
	}
	search, err := p.Search(ctx, memory.SearchRequest{
		Query: req.Query, Project: req.Project, CWD: req.CWD, AgentID: req.AgentID,
		Limit: req.Limit, Format: "narrative", TokenBudget: req.TokenBudget,
	})
	if err != nil {
		if result.Text == "" && len(result.Memories) == 0 {
			return memory.ContextResult{}, err
		}
		result.Warnings = append(result.Warnings, "relevant-memory search failed: "+err.Error())
		return result, nil
	}
	if search.Context != "" {
		if result.Text == "" {
			result.Text = search.Context
		} else if !strings.Contains(result.Text, search.Context) {
			result.Text += "\n\nRelevant memories:\n" + search.Context
		}
	}
	result.Memories = mergeItems(result.Memories, search.Memories)
	result.Count = len(result.Memories)
	result.Truncated = result.Truncated || search.Truncated
	return result, nil
}

func (p *Provider) Remember(ctx context.Context, req memory.RememberRequest) (memory.RememberResult, error) {
	payload := map[string]any{"content": req.Content}
	putOptional(payload, "type", req.Kind)
	putOptional(payload, "project", req.Project)
	putOptional(payload, "agentId", req.AgentID)
	putOptional(payload, "sessionId", req.SessionID)
	if len(req.Concepts) > 0 {
		payload["concepts"] = req.Concepts
	}
	if len(req.Files) > 0 {
		payload["files"] = req.Files
	}
	putPositive(payload, "ttlDays", req.TTLDays)
	var body any
	if err := p.call(ctx, http.MethodPost, p.rememberPath, payload, &body); err != nil {
		return memory.RememberResult{}, err
	}
	return normalizeRemember(p.Name(), req.Project, body), nil
}

func (p *Provider) Observe(ctx context.Context, req memory.ObservationRequest) error {
	payload := map[string]any{
		"hookType": req.HookType, "sessionId": req.SessionID, "project": req.Project,
		"cwd": req.CWD, "timestamp": req.Timestamp, "data": req.Data,
	}
	var body any
	return p.call(ctx, http.MethodPost, p.observePath, payload, &body)
}

func (p *Provider) Forget(ctx context.Context, req memory.ForgetRequest) (memory.ForgetResult, error) {
	payload := map[string]any{}
	putOptional(payload, "memoryId", req.MemoryID)
	putOptional(payload, "sessionId", req.SessionID)
	if len(req.ObservationIDs) > 0 {
		payload["observationIds"] = req.ObservationIDs
	}
	var body any
	if err := p.call(ctx, http.MethodPost, p.forgetPath, payload, &body); err != nil {
		return memory.ForgetResult{}, err
	}
	return normalizeForget(p.Name(), req, body), nil
}

func (p *Provider) Export(ctx context.Context, req memory.ExportRequest) (memory.ExportResult, error) {
	var body any
	if err := p.call(ctx, http.MethodGet, p.exportPath, nil, &body); err != nil {
		return memory.ExportResult{}, err
	}
	result := memory.ExportResult{
		SchemaVersion: 1, Provider: p.Name(), Project: req.Project, Memories: []memory.Item{},
	}
	entry, ok := body.(map[string]any)
	if !ok {
		return result, nil
	}
	for _, raw := range anySlice(entry["memories"]) {
		item, ok := normalizeItem(raw)
		if !ok {
			continue
		}
		if req.Project != "" && item.Project != req.Project {
			continue
		}
		if req.AgentID != "" && item.AgentID != "" && item.AgentID != req.AgentID {
			continue
		}
		result.Memories = append(result.Memories, item)
	}
	result.Count = len(result.Memories)
	return result, nil
}

func (p *Provider) Import(ctx context.Context, req memory.ImportRequest) (memory.ImportResult, error) {
	result := memory.ImportResult{Provider: p.Name(), Project: req.Project}
	for index, item := range req.Memories {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			content = strings.TrimSpace(item.Summary)
		}
		if content == "" {
			result.Skipped++
			result.Errors = append(result.Errors, fmt.Sprintf("memory %d has no content or summary", index))
			continue
		}
		project := req.Project
		if project == "" {
			project = item.Project
		}
		_, err := p.Remember(ctx, memory.RememberRequest{
			Content: content, Kind: item.Kind, Project: project, AgentID: req.AgentID,
			Concepts: item.Concepts, Files: item.Files,
		})
		if err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, fmt.Sprintf("memory %d: %v", index, err))
			continue
		}
		result.Imported++
	}
	return result, nil
}

func (*Provider) Close() error { return nil }

func (p *Provider) call(ctx context.Context, method, path string, payload any, target any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, p.endpoint+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if p.secret != "" {
		request.Header.Set("Authorization", "Bearer "+p.secret)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("agentmemory request: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, p.maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read agentmemory response: %w", err)
	}
	if int64(len(raw)) > p.maxResponseBytes {
		return fmt.Errorf("agentmemory response exceeds %d bytes", p.maxResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := strings.TrimSpace(string(raw))
		if len(message) > 500 {
			message = message[:500]
		}
		return &HTTPError{StatusCode: response.StatusCode, Message: message}
	}
	if target != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("decode agentmemory response: %w", err)
		}
	}
	return nil
}

func isUnsupportedContext(err error) bool {
	httpErr, ok := err.(*HTTPError)
	return ok && (httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusMethodNotAllowed)
}

func mergeItems(groups ...[]memory.Item) []memory.Item {
	seen := map[string]bool{}
	var out []memory.Item
	for _, group := range groups {
		for _, item := range group {
			key := item.ProviderID
			if key == "" {
				key = item.ID
			}
			if key == "" {
				key = item.Content + "\x00" + item.Summary
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, item)
		}
	}
	return out
}

func normalizeSearch(provider string, req memory.SearchRequest, body any) memory.SearchResult {
	result := memory.SearchResult{
		Provider: provider, Project: req.Project, Query: req.Query, Format: req.Format,
		Memories: []memory.Item{},
	}
	appendBodyToSearch(&result, body)
	if result.Count == 0 {
		result.Count = len(result.Memories)
	}
	return result
}

func appendBodyToSearch(result *memory.SearchResult, body any) {
	switch value := body.(type) {
	case string:
		result.Context = value
	case []any:
		for _, entry := range value {
			if item, ok := normalizeItem(entry); ok {
				result.Memories = append(result.Memories, item)
			}
		}
	case map[string]any:
		if result.Context == "" {
			result.Context = firstString(value, "context", "narrative", "answer", "formatted", "text")
		}
		if count := firstInt(value, "count", "total", "totalCount"); count > 0 {
			result.Count = count
		}
		result.Truncated = firstBool(value, "truncated", "hasMore")
		for _, key := range []string{"memories", "results", "hits", "items"} {
			if entries, ok := value[key].([]any); ok {
				appendBodyToSearch(result, entries)
				return
			}
		}
		if nested, exists := value["data"]; exists && nested != body {
			appendBodyToSearch(result, nested)
		}
	}
}

func normalizeContext(provider string, req memory.ContextRequest, body any) memory.ContextResult {
	search := normalizeSearch(provider, memory.SearchRequest{
		Query: req.Query, Project: req.Project, CWD: req.CWD, AgentID: req.AgentID,
		Limit: req.Limit, Format: "narrative", TokenBudget: req.TokenBudget,
	}, body)
	return memory.ContextResult{
		Provider: provider, Project: req.Project, Query: req.Query, Text: search.Context,
		Memories: search.Memories, Count: search.Count, TokenBudget: req.TokenBudget,
		Truncated: search.Truncated,
	}
}

func normalizeItem(value any) (memory.Item, bool) {
	entry, ok := value.(map[string]any)
	if !ok {
		if text, textOK := value.(string); textOK && strings.TrimSpace(text) != "" {
			return memory.Item{Content: text}, true
		}
		return memory.Item{}, false
	}
	id := firstString(entry, "id", "memoryId", "memory_id", "uuid", "observationId")
	content := firstString(entry, "content", "text", "memory", "body")
	summary := firstString(entry, "summary", "title")
	if id == "" && content == "" && summary == "" {
		return memory.Item{}, false
	}
	if id == "" {
		sum := sha256.Sum256([]byte(content + "\x00" + summary))
		id = "content:" + fmt.Sprintf("%x", sum[:8])
	}
	project := firstString(entry, "project")
	agentID := firstString(entry, "agentId", "agent_id")
	concepts := stringSlice(entry["concepts"])
	files := stringSlice(entry["files"])
	if nested, ok := entry["metadata"].(map[string]any); ok {
		if project == "" {
			project = firstString(nested, "project")
		}
		if agentID == "" {
			agentID = firstString(nested, "agentId", "agent_id")
		}
		if len(concepts) == 0 {
			concepts = stringSlice(nested["concepts"])
		}
		if len(files) == 0 {
			files = stringSlice(nested["files"])
		}
	}
	item := memory.Item{
		ID: id, ProviderID: id, Kind: firstString(entry, "kind", "type", "memoryType"),
		Content: content, Summary: summary, Score: firstFloat(entry, "score", "similarity", "relevance"),
		Project: project, AgentID: agentID, Concepts: concepts, Files: files,
		CreatedAt: firstString(entry, "createdAt", "created_at", "timestamp"),
	}
	metadata := map[string]any{}
	for _, key := range []string{"source", "tier", "sessionId", "observationId"} {
		if raw, exists := entry[key]; exists {
			metadata[key] = raw
		}
	}
	if len(metadata) > 0 {
		item.Metadata = metadata
	}
	return item, true
}

func normalizeRemember(provider, project string, body any) memory.RememberResult {
	result := memory.RememberResult{Provider: provider, Project: project, Stored: true}
	if entry, ok := body.(map[string]any); ok {
		result.ProviderID = firstString(entry, "id", "memoryId", "memory_id", "uuid")
		result.ID = result.ProviderID
		if stored, exists := entry["stored"].(bool); exists {
			result.Stored = stored
		}
	}
	return result
}

func normalizeForget(provider string, req memory.ForgetRequest, body any) memory.ForgetResult {
	ids := append([]string{}, req.ObservationIDs...)
	if req.MemoryID != "" {
		ids = append(ids, req.MemoryID)
	}
	if req.SessionID != "" {
		ids = append(ids, req.SessionID)
	}
	result := memory.ForgetResult{Provider: provider, IDs: ids, Deleted: len(ids)}
	if entry, ok := body.(map[string]any); ok {
		if count, exists := lookupInt(entry, "deleted", "count", "deletedCount"); exists {
			result.Deleted = count
		}
		if returned := stringSlice(entry["ids"]); len(returned) > 0 {
			result.IDs = returned
		}
	}
	return result
}

func capabilitiesFromBody(base memory.Capabilities, body map[string]any) memory.Capabilities {
	for _, key := range []string{"capabilities", "features"} {
		values, ok := body[key].(map[string]any)
		if !ok {
			continue
		}
		setCapability := func(target *bool, names ...string) {
			for _, name := range names {
				if value, exists := values[name].(bool); exists {
					*target = value
					return
				}
			}
		}
		setCapability(&base.Search, "search")
		setCapability(&base.Context, "context")
		setCapability(&base.Remember, "remember")
		setCapability(&base.Forget, "forget")
		setCapability(&base.Observe, "observe")
		setCapability(&base.Sessions, "sessions")
		setCapability(&base.KnowledgeGraph, "knowledgeGraph", "knowledge_graph", "graph")
		setCapability(&base.Provenance, "provenance")
		setCapability(&base.Export, "export")
		setCapability(&base.Import, "import")
	}
	return base
}

func capabilitiesFromFlags(base memory.Capabilities, body map[string]any) memory.Capabilities {
	flags, _ := body["flags"].([]any)
	for _, raw := range flags {
		flag, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key := firstString(flag, "key")
		enabled, _ := flag["enabled"].(bool)
		switch key {
		case "GRAPH_EXTRACTION_ENABLED":
			base.KnowledgeGraph = enabled
		}
	}
	return base
}

func optionPath(options map[string]any, key, fallback string) string {
	value := optionString(options, key, fallback)
	if !strings.HasPrefix(value, "/") {
		return fallback
	}
	return value
}

func optionString(options map[string]any, key, fallback string) string {
	if value, ok := options[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func optionInt(options map[string]any, key string, fallback int) int {
	switch value := options[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	case json.Number:
		number, _ := value.Int64()
		return int(number)
	default:
		return fallback
	}
}

func optionBool(options map[string]any, key string, fallback bool) bool {
	if value, ok := options[key].(bool); ok {
		return value
	}
	return fallback
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstInt(values map[string]any, keys ...string) int {
	value, _ := lookupInt(values, keys...)
	return value
}

func lookupInt(values map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		switch value := values[key].(type) {
		case int:
			return value, true
		case float64:
			return int(value), true
		case json.Number:
			number, _ := value.Int64()
			return int(number), true
		case string:
			if parsed, err := strconv.Atoi(value); err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func firstFloat(values map[string]any, keys ...string) float64 {
	for _, key := range keys {
		switch value := values[key].(type) {
		case float64:
			return value
		case int:
			return float64(value)
		case json.Number:
			number, _ := value.Float64()
			return number
		}
	}
	return 0
}

func firstBool(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := values[key].(bool); ok {
			return value
		}
	}
	return false
}

func stringSlice(value any) []string {
	var out []string
	switch entries := value.(type) {
	case []string:
		return append(out, entries...)
	case []any:
		for _, entry := range entries {
			if text, ok := entry.(string); ok && text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func putOptional(target map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		target[key] = value
	}
}

func putPositive(target map[string]any, key string, value int) {
	if value > 0 {
		target[key] = value
	}
}
