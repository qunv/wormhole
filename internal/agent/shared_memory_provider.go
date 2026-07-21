// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"fmt"
	"sync"

	"codebridge/internal/memory"
)

// synchronizedMemoryProvider serializes calls into a pooled provider. Built-in
// providers are concurrency-safe, but third-party factory registrations are not
// required to be. The wrapper makes daemon-wide reuse safe without changing the
// provider interface contract.
type synchronizedMemoryProvider struct {
	mu       sync.Mutex
	provider memory.Provider
}

func wrapSharedMemoryProvider(provider memory.Provider) memory.Provider {
	base := &synchronizedMemoryProvider{provider: provider}
	_, exporter := provider.(memory.Exporter)
	_, importer := provider.(memory.Importer)
	switch {
	case exporter && importer:
		return &synchronizedMemoryExportImporter{base}
	case exporter:
		return &synchronizedMemoryExporter{base}
	case importer:
		return &synchronizedMemoryImporter{base}
	default:
		return base
	}
}

func (p *synchronizedMemoryProvider) Name() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.provider.Name()
}

func (p *synchronizedMemoryProvider) Capabilities() memory.Capabilities {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.provider.Capabilities()
}

func (p *synchronizedMemoryProvider) Health(ctx context.Context) memory.HealthResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.provider.Health(ctx)
}

func (p *synchronizedMemoryProvider) Search(ctx context.Context, request memory.SearchRequest) (memory.SearchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.provider.Search(ctx, request)
}

func (p *synchronizedMemoryProvider) Context(ctx context.Context, request memory.ContextRequest) (memory.ContextResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.provider.Context(ctx, request)
}

func (p *synchronizedMemoryProvider) Remember(ctx context.Context, request memory.RememberRequest) (memory.RememberResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.provider.Remember(ctx, request)
}

func (p *synchronizedMemoryProvider) Observe(ctx context.Context, request memory.ObservationRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.provider.Observe(ctx, request)
}

func (p *synchronizedMemoryProvider) Forget(ctx context.Context, request memory.ForgetRequest) (memory.ForgetResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.provider.Forget(ctx, request)
}

func (p *synchronizedMemoryProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.provider.Close()
}

type synchronizedMemoryExporter struct{ *synchronizedMemoryProvider }

type synchronizedMemoryImporter struct{ *synchronizedMemoryProvider }

type synchronizedMemoryExportImporter struct{ *synchronizedMemoryProvider }

func (p *synchronizedMemoryExporter) Export(ctx context.Context, request memory.ExportRequest) (memory.ExportResult, error) {
	return p.export(ctx, request)
}

func (p *synchronizedMemoryImporter) Import(ctx context.Context, request memory.ImportRequest) (memory.ImportResult, error) {
	return p.importMemories(ctx, request)
}

func (p *synchronizedMemoryExportImporter) Export(ctx context.Context, request memory.ExportRequest) (memory.ExportResult, error) {
	return p.export(ctx, request)
}

func (p *synchronizedMemoryExportImporter) Import(ctx context.Context, request memory.ImportRequest) (memory.ImportResult, error) {
	return p.importMemories(ctx, request)
}

func (p *synchronizedMemoryProvider) export(ctx context.Context, request memory.ExportRequest) (memory.ExportResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	exporter, ok := p.provider.(memory.Exporter)
	if !ok {
		return memory.ExportResult{}, fmt.Errorf("memory provider %q does not support export", p.provider.Name())
	}
	return exporter.Export(ctx, request)
}

func (p *synchronizedMemoryProvider) importMemories(ctx context.Context, request memory.ImportRequest) (memory.ImportResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	importer, ok := p.provider.(memory.Importer)
	if !ok {
		return memory.ImportResult{}, fmt.Errorf("memory provider %q does not support import", p.provider.Name())
	}
	return importer.Import(ctx, request)
}
