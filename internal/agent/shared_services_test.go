package agent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wormhole/internal/config"
	"wormhole/internal/memory"
	"wormhole/internal/upstreammcp"
)

type sharedTestMemoryProvider struct {
	closed     atomic.Int64
	observed   atomic.Int64
	closeErr   error
	providerID int64
}

func (p *sharedTestMemoryProvider) Name() string { return "shared-test" }
func (p *sharedTestMemoryProvider) Capabilities() memory.Capabilities {
	return memory.Capabilities{Search: true, Context: true, Remember: true, Forget: true, Observe: true}
}
func (p *sharedTestMemoryProvider) Health(context.Context) memory.HealthResult {
	return memory.HealthResult{Provider: p.Name(), Enabled: true, Available: true, Capabilities: p.Capabilities()}
}
func (p *sharedTestMemoryProvider) Search(context.Context, memory.SearchRequest) (memory.SearchResult, error) {
	return memory.SearchResult{Provider: p.Name()}, nil
}
func (p *sharedTestMemoryProvider) Context(context.Context, memory.ContextRequest) (memory.ContextResult, error) {
	return memory.ContextResult{Provider: p.Name()}, nil
}
func (p *sharedTestMemoryProvider) Remember(context.Context, memory.RememberRequest) (memory.RememberResult, error) {
	return memory.RememberResult{Provider: p.Name(), Stored: true}, nil
}
func (p *sharedTestMemoryProvider) Observe(context.Context, memory.ObservationRequest) error {
	p.observed.Add(1)
	return nil
}
func (p *sharedTestMemoryProvider) Forget(context.Context, memory.ForgetRequest) (memory.ForgetResult, error) {
	return memory.ForgetResult{Provider: p.Name()}, nil
}
func (p *sharedTestMemoryProvider) Close() error {
	p.closed.Add(1)
	return p.closeErr
}

type sharedExportImportProvider struct{ *sharedTestMemoryProvider }

func (p *sharedExportImportProvider) Export(context.Context, memory.ExportRequest) (memory.ExportResult, error) {
	return memory.ExportResult{Provider: p.Name(), Count: 1}, nil
}

func (p *sharedExportImportProvider) Import(context.Context, memory.ImportRequest) (memory.ImportResult, error) {
	return memory.ImportResult{Provider: p.Name(), Imported: 1}, nil
}

type concurrencyProbeProvider struct {
	sharedTestMemoryProvider
	active     atomic.Int64
	overlapped atomic.Bool
}

func (p *concurrencyProbeProvider) Observe(context.Context, memory.ObservationRequest) error {
	if p.active.Add(1) > 1 {
		p.overlapped.Store(true)
	}
	time.Sleep(5 * time.Millisecond)
	p.active.Add(-1)
	return nil
}

type concurrencySafeProbeProvider struct{ concurrencyProbeProvider }

func (*concurrencySafeProbeProvider) ConcurrencySafe() bool { return true }

func TestSharedMemoryWrapperPreservesOptionalInterfaces(t *testing.T) {
	provider := &sharedExportImportProvider{sharedTestMemoryProvider: &sharedTestMemoryProvider{}}
	wrapped := wrapSharedMemoryProvider(provider)
	if _, ok := wrapped.(memory.Exporter); !ok {
		t.Fatal("shared wrapper dropped exporter capability")
	}
	if _, ok := wrapped.(memory.Importer); !ok {
		t.Fatal("shared wrapper dropped importer capability")
	}
	plain := wrapSharedMemoryProvider(&sharedTestMemoryProvider{})
	if _, ok := plain.(memory.Exporter); ok {
		t.Fatal("plain provider unexpectedly gained exporter capability")
	}
}

func TestSharedMemoryWrapperSerializesProviderCalls(t *testing.T) {
	provider := &concurrencyProbeProvider{}
	wrapped := wrapSharedMemoryProvider(provider)
	var wg sync.WaitGroup
	for index := 0; index < 12; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = wrapped.Observe(context.Background(), memory.ObservationRequest{})
		}()
	}
	wg.Wait()
	if provider.overlapped.Load() {
		t.Fatal("pooled provider calls overlapped")
	}
}

func TestSharedMemoryWrapperAllowsOptedInConcurrentProviderCalls(t *testing.T) {
	provider := &concurrencySafeProbeProvider{}
	wrapped := wrapSharedMemoryProvider(provider)
	if wrapped != provider {
		t.Fatal("concurrency-safe provider was unnecessarily wrapped")
	}
	var wg sync.WaitGroup
	for index := 0; index < 12; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = wrapped.Observe(context.Background(), memory.ObservationRequest{})
		}()
	}
	wg.Wait()
	if !provider.overlapped.Load() {
		t.Fatal("concurrency-safe provider calls were serialized")
	}
}

func TestSharedServicesReuseAuditWriterByPath(t *testing.T) {
	shared := NewSharedServices("test")
	path := filepath.Join(t.TempDir(), "audit.log")
	first, err := shared.acquireAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := shared.acquireAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	other, err := shared.acquireAudit(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == other {
		t.Fatal("audit writers were not pooled by canonical path")
	}
	auditStats := shared.Stats()["audit"].(map[string]any)
	if auditStats["writers"] != 2 || auditStats["acquires"] != uint64(3) || auditStats["reuses"] != uint64(1) {
		t.Fatalf("unexpected audit reuse stats: %#v", auditStats)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Append([]byte("closed\n")); err == nil {
		t.Fatal("shared close left audit writer accepting records")
	}
}

func TestSharedServicesReuseMemoryAndCloseOnce(t *testing.T) {
	shared := NewSharedServices("test")
	var created atomic.Int64
	var provider *sharedTestMemoryProvider
	shared.memoryFactory = func(config.MemoryConfig) (memory.Provider, error) {
		provider = &sharedTestMemoryProvider{providerID: created.Add(1)}
		return provider, nil
	}

	cfg := config.Default()
	cfg.NoTunnel, cfg.Policy = true, "full"
	cfg.Memory.Enabled = true
	cfg.Memory.Provider = "shared-test"
	cfg.Memory.CaptureMode = "selected"
	cfg.Memory.QueueSize = 16

	first := newRuntimeWithSharedForTest(t, shared, "first", cfg)
	second := newRuntimeWithSharedForTest(t, shared, "second", cfg)
	if created.Load() != 1 {
		t.Fatalf("memory providers created = %d, want 1", created.Load())
	}
	if first.Memory != second.Memory || first.MemoryRecorder != second.MemoryRecorder {
		t.Fatal("compatible runtimes did not reuse memory provider and recorder")
	}

	for _, runtime := range []*Runtime{first, second} {
		if _, err := runtime.Handle(context.Background(), "save_note", map[string]any{"title": runtime.WorkspaceID, "body": "shared recorder"}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for provider.observed.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if provider.observed.Load() != 2 {
		t.Fatalf("shared recorder observations = %d, want 2", provider.observed.Load())
	}

	first.Close()
	if provider.closed.Load() != 0 {
		t.Fatal("closing one borrowed runtime closed the shared provider")
	}
	if _, err := second.Handle(context.Background(), "workspace_info", nil); err != nil {
		t.Fatalf("second runtime stopped after first closed: %v", err)
	}
	second.Close()
	if provider.closed.Load() != 0 {
		t.Fatal("borrowed runtime shutdown closed the shared provider")
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	if provider.closed.Load() != 1 {
		t.Fatalf("provider close count = %d, want 1", provider.closed.Load())
	}
}

func TestSharedServicesKeepIncompatibleMemoryConfigsSeparate(t *testing.T) {
	shared := NewSharedServices("test")
	var created atomic.Int64
	shared.memoryFactory = func(config.MemoryConfig) (memory.Provider, error) {
		return &sharedTestMemoryProvider{providerID: created.Add(1)}, nil
	}
	defer shared.Close()

	base := config.Default()
	base.NoTunnel = true
	base.Memory.Enabled = true
	base.Memory.Provider = "shared-test"
	base.Memory.Endpoint = "http://memory-one"
	first := newRuntimeWithSharedForTest(t, shared, "first", base)
	defer first.Close()

	other := base
	other.Memory.Endpoint = "http://memory-two"
	second := newRuntimeWithSharedForTest(t, shared, "second", other)
	defer second.Close()
	if first.Memory == second.Memory || created.Load() != 2 {
		t.Fatalf("incompatible memory configs were pooled: created=%d", created.Load())
	}
}

func TestSharedServicesIgnoreRuntimeOnlyMemorySettingsForProviderPooling(t *testing.T) {
	shared := NewSharedServices("test")
	var created atomic.Int64
	shared.memoryFactory = func(config.MemoryConfig) (memory.Provider, error) {
		created.Add(1)
		return &sharedTestMemoryProvider{}, nil
	}
	defer shared.Close()

	base := config.Default()
	base.NoTunnel = true
	base.Memory.Enabled = true
	base.Memory.Provider = "shared-test"
	base.Memory.Endpoint = "http://memory"
	first := newRuntimeWithSharedForTest(t, shared, "first", base)
	defer first.Close()

	other := base
	other.Memory.AgentID = "another-agent"
	other.Memory.TokenBudget += 500
	other.Memory.ProjectStrategy = "path-hash"
	other.Memory.Required = !base.Memory.Required
	other.Memory.HealthCacheMS += 1_000
	second := newRuntimeWithSharedForTest(t, shared, "second", other)
	defer second.Close()

	if first.Memory != second.Memory || created.Load() != 1 {
		t.Fatalf("runtime-only memory settings prevented provider reuse: created=%d", created.Load())
	}
	if first.MemoryRecorder != second.MemoryRecorder {
		t.Fatal("identical delivery settings did not reuse recorder")
	}
}

func TestSharedServicesReuseUpstreamClientAndContract(t *testing.T) {
	upstream := newAgentUpstreamHTTPServer(t)
	shared := NewSharedServices("test")
	defer shared.Close()

	cfg := config.Default()
	cfg.NoTunnel, cfg.Policy = true, "full"
	cfg.MCPServers["community"] = agentUpstreamConfig(upstream.URL)
	first := newRuntimeWithSharedForTest(t, shared, "first", cfg)
	defer first.Close()
	second := newRuntimeWithSharedForTest(t, shared, "second", cfg)
	defer second.Close()

	firstModuleValue, _ := first.Module("mcp_community")
	secondModuleValue, _ := second.Module("mcp_community")
	firstModule := firstModuleValue.(*upstreamMCPModule)
	secondModule := secondModuleValue.(*upstreamMCPModule)
	if firstModule != secondModule || firstModule.client != secondModule.client {
		t.Fatal("compatible upstream configuration did not reuse client and contract")
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, runtime := range []*Runtime{first, second} {
		wg.Add(1)
		go func(runtime *Runtime) {
			defer wg.Done()
			_, err := runtime.Handle(context.Background(), "community__read_data", map[string]any{"query": runtime.WorkspaceID})
			errs <- err
		}(runtime)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent shared upstream call: %v", err)
		}
	}

	first.Close()
	if _, err := second.Handle(context.Background(), "community__read_data", map[string]any{"query": "after-close"}); err != nil {
		t.Fatalf("closing one runtime disconnected shared upstream: %v", err)
	}
	stats := shared.Stats()
	upstreamStats := stats["upstream_mcp"].(map[string]any)
	if upstreamStats["clients"] != 1 || upstreamStats["contracts"] != 1 {
		t.Fatalf("unexpected shared upstream stats: %#v", upstreamStats)
	}
}

func TestSharedServicesSingleflightConcurrentUpstreamCreation(t *testing.T) {
	upstream := newAgentUpstreamHTTPServer(t)
	shared := NewSharedServices("test")
	defer shared.Close()

	var created atomic.Int64
	shared.upstreamFactory = func(ctx context.Context, name string, cfg config.MCPServerConfig, version, cwd string) (*upstreammcp.Client, error) {
		created.Add(1)
		time.Sleep(50 * time.Millisecond)
		return upstreammcp.New(ctx, name, cfg, version, cwd)
	}
	base := config.Default()
	base.NoTunnel, base.Policy = true, "full"
	base.MCPServers["community"] = agentUpstreamConfig(upstream.URL)
	roots := []string{t.TempDir(), t.TempDir()}
	dataDirs := []string{t.TempDir(), t.TempDir()}

	type result struct {
		runtime *Runtime
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for index, id := range []string{"first", "second"} {
		index, id := index, id
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := base
			cfg.Workspace = roots[index]
			runtime, err := NewWorkspaceContextWithSharedServices(
				context.Background(), id, dataDirs[index], cfg, "test", "pro", id+"-config", shared, nil,
			)
			results <- result{runtime: runtime, err: err}
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		result.runtime.Close()
	}
	if got := created.Load(); got != 1 {
		t.Fatalf("concurrent upstream factory calls = %d, want 1", got)
	}
}

func TestSharedServicesCacheUpstreamCreationFailureDuringCooldown(t *testing.T) {
	shared := NewSharedServices("test")
	defer shared.Close()

	marker := errors.New("upstream unavailable")
	var created atomic.Int64
	shared.upstreamFactory = func(context.Context, string, config.MCPServerConfig, string, string) (*upstreammcp.Client, error) {
		created.Add(1)
		return nil, marker
	}
	base := config.Default()
	base.NoTunnel = true
	server := agentUpstreamConfig("http://127.0.0.1:1/mcp")
	server.FailureCooldownMS = 5_000
	base.MCPServers["community"] = server

	for _, id := range []string{"first", "second", "third"} {
		cfg := base
		cfg.Workspace = t.TempDir()
		runtime, err := NewWorkspaceContextWithSharedServices(
			context.Background(), id, t.TempDir(), cfg, "test", "pro", id+"-config", shared, nil,
		)
		if err != nil {
			t.Fatalf("optional failed upstream prevented runtime %s: %v", id, err)
		}
		runtime.Close()
	}
	if got := created.Load(); got != 1 {
		t.Fatalf("upstream factory calls during failure cooldown = %d, want 1", got)
	}
}

func TestLazyUpstreamBootstrapsCatalogThenDefersLaterStartup(t *testing.T) {
	t.Setenv("WORMHOLE_DATA_DIR", t.TempDir())
	upstream := newAgentUpstreamHTTPServer(t)

	base := config.Default()
	base.NoTunnel, base.Policy = true, "full"
	serverConfig := agentUpstreamConfig(upstream.URL)
	serverConfig.StartupMode = config.MCPStartupModeLazy
	base.MCPServers["community"] = serverConfig

	bootstrapShared := NewSharedServices("test")
	var bootstrapCalls atomic.Int64
	bootstrapShared.upstreamFactory = func(ctx context.Context, name string, cfg config.MCPServerConfig, version, cwd string) (*upstreammcp.Client, error) {
		bootstrapCalls.Add(1)
		return upstreammcp.New(ctx, name, cfg, version, cwd)
	}
	bootstrapRuntime := newRuntimeWithSharedForTest(t, bootstrapShared, "bootstrap", base)
	moduleValue, _ := bootstrapRuntime.Module("mcp_community")
	if moduleValue.(*upstreamMCPModule).client.Deferred() {
		t.Fatal("cache-miss lazy startup did not bootstrap eagerly")
	}
	bootstrapRuntime.Close()
	if err := bootstrapShared.Close(); err != nil {
		t.Fatal(err)
	}
	if got := bootstrapCalls.Load(); got != 1 {
		t.Fatalf("bootstrap factory calls = %d, want 1", got)
	}
	clientKey := upstreamClientResourceKey("community", serverConfig, "")
	if catalog, err := upstreammcp.LoadToolCatalog(clientKey); err != nil || len(catalog.Tools) != 2 {
		t.Fatalf("bootstrap catalog missing: tools=%d err=%v", len(catalog.Tools), err)
	}

	upstream.Close()
	lazyShared := NewSharedServices("test")
	defer lazyShared.Close()
	var lazyFactoryCalls atomic.Int64
	lazyShared.upstreamFactory = func(context.Context, string, config.MCPServerConfig, string, string) (*upstreammcp.Client, error) {
		lazyFactoryCalls.Add(1)
		return nil, errors.New("lazy startup unexpectedly invoked eager factory")
	}
	lazyRuntime := newRuntimeWithSharedForTest(t, lazyShared, "lazy", base)
	defer lazyRuntime.Close()
	lazyModuleValue, _ := lazyRuntime.Module("mcp_community")
	lazyModule := lazyModuleValue.(*upstreamMCPModule)
	if !lazyModule.client.Deferred() || lazyFactoryCalls.Load() != 0 {
		t.Fatalf("cached lazy startup was not deferred: deferred=%t factory_calls=%d", lazyModule.client.Deferred(), lazyFactoryCalls.Load())
	}
	if _, err := lazyRuntime.Handle(context.Background(), "community__read_data", map[string]any{"query": "first-call"}); err == nil {
		t.Fatal("first lazy call unexpectedly succeeded after upstream shutdown")
	}
}

func TestBackgroundUpstreamUsesCatalogAndConnectsAsynchronously(t *testing.T) {
	t.Setenv("WORMHOLE_DATA_DIR", t.TempDir())
	upstream := newAgentUpstreamHTTPServer(t)

	seedConfig := config.Default()
	seedConfig.NoTunnel, seedConfig.Policy = true, "full"
	serverConfig := agentUpstreamConfig(upstream.URL)
	seedConfig.MCPServers["community"] = serverConfig
	seedShared := NewSharedServices("test")
	seedRuntime := newRuntimeWithSharedForTest(t, seedShared, "seed", seedConfig)
	seedRuntime.Close()
	if err := seedShared.Close(); err != nil {
		t.Fatal(err)
	}

	serverConfig.StartupMode = config.MCPStartupModeBackground
	backgroundConfig := seedConfig
	backgroundConfig.MCPServers = map[string]config.MCPServerConfig{"community": serverConfig}
	backgroundShared := NewSharedServices("test")
	defer backgroundShared.Close()
	var factoryCalls atomic.Int64
	backgroundShared.upstreamFactory = func(context.Context, string, config.MCPServerConfig, string, string) (*upstreammcp.Client, error) {
		factoryCalls.Add(1)
		return nil, errors.New("background startup unexpectedly invoked eager factory")
	}
	backgroundRuntime := newRuntimeWithSharedForTest(t, backgroundShared, "background", backgroundConfig)
	defer backgroundRuntime.Close()
	moduleValue, _ := backgroundRuntime.Module("mcp_community")
	module := moduleValue.(*upstreamMCPModule)
	deadline := time.Now().Add(2 * time.Second)
	for module.client.Deferred() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if module.client.Deferred() || factoryCalls.Load() != 0 {
		t.Fatalf("background connection did not complete from cache: deferred=%t factory_calls=%d", module.client.Deferred(), factoryCalls.Load())
	}
	if _, err := backgroundRuntime.Handle(context.Background(), "community__read_data", map[string]any{"query": "background"}); err != nil {
		t.Fatalf("background-connected upstream call failed: %v", err)
	}
}

func TestSharedServicesShareUpstreamClientAcrossPolicyContracts(t *testing.T) {
	upstream := newAgentUpstreamHTTPServer(t)
	shared := NewSharedServices("test")
	defer shared.Close()

	base := config.Default()
	base.NoTunnel, base.Policy = true, "full"
	base.MCPServers["community"] = agentUpstreamConfig(upstream.URL)
	first := newRuntimeWithSharedForTest(t, shared, "first", base)
	defer first.Close()

	other := base
	server := other.MCPServers["community"]
	server.Policy.AlwaysApproveTools = nil
	server.Policy.ApprovalTools = []string{"write.data"}
	other.MCPServers = map[string]config.MCPServerConfig{"community": server}
	second := newRuntimeWithSharedForTest(t, shared, "second", other)
	defer second.Close()

	firstModuleValue, _ := first.Module("mcp_community")
	secondModuleValue, _ := second.Module("mcp_community")
	firstModule := firstModuleValue.(*upstreamMCPModule)
	secondModule := secondModuleValue.(*upstreamMCPModule)
	if firstModule == secondModule {
		t.Fatal("different policy contracts reused the same module")
	}
	if firstModule.client != secondModule.client {
		t.Fatal("policy-only differences prevented upstream client reuse")
	}
	stats := shared.Stats()["upstream_mcp"].(map[string]any)
	if stats["clients"] != 1 || stats["contracts"] != 2 {
		t.Fatalf("unexpected policy pooling stats: %#v", stats)
	}
}

func TestUpstreamClientResourceKeyIncludesResolvedCWDNotPolicy(t *testing.T) {
	cfg := agentUpstreamConfig("http://127.0.0.1:1234/mcp")
	first := upstreamClientResourceKey("community", cfg, "/workspace/one")
	second := upstreamClientResourceKey("community", cfg, "/workspace/two")
	if first == second {
		t.Fatal("different resolved cwd values produced the same upstream client key")
	}
	cfg.Policy.Default = "read-only"
	third := upstreamClientResourceKey("community", cfg, "/workspace/one")
	if first != third {
		t.Fatal("policy-only difference changed the upstream connection key")
	}
	cfg.CWD = "."
	fourth := upstreamClientResourceKey("community", cfg, "/workspace/one")
	cfg.CWD = "/workspace/one"
	fifth := upstreamClientResourceKey("community", cfg, "/workspace/one")
	if fourth != fifth {
		t.Fatal("equivalent resolved cwd values produced different upstream client keys")
	}
}

func TestSharedServicesCloseReturnsProviderErrorOnce(t *testing.T) {
	shared := NewSharedServices("test")
	provider := &sharedTestMemoryProvider{closeErr: errors.New("close marker")}
	shared.memoryFactory = func(config.MemoryConfig) (memory.Provider, error) { return provider, nil }
	cfg := config.Default()
	cfg.NoTunnel = true
	cfg.Memory.Enabled = true
	cfg.Memory.Provider = "shared-test"
	runtime := newRuntimeWithSharedForTest(t, shared, "default", cfg)
	runtime.Close()
	if err := shared.Close(); err == nil || !errors.Is(err, provider.closeErr) {
		t.Fatalf("shared close error = %v", err)
	}
	if err := shared.Close(); err == nil || !errors.Is(err, provider.closeErr) {
		t.Fatalf("repeated shared close error = %v", err)
	}
	if provider.closed.Load() != 1 {
		t.Fatalf("provider close count = %d, want 1", provider.closed.Load())
	}
}

func newRuntimeWithSharedForTest(t *testing.T, shared *SharedServices, id string, cfg config.Config) *Runtime {
	t.Helper()
	cfg.Workspace = t.TempDir()
	runtime, err := NewWorkspaceContextWithSharedServices(
		context.Background(), id, t.TempDir(), cfg, "test", "pro", id+"-config", shared, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
