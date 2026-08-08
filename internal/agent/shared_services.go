// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"wormhole/internal/config"
	"wormhole/internal/memory"
	memoryfactory "wormhole/internal/memory/factory"
	"wormhole/internal/state"
	"wormhole/internal/upstreammcp"
)

// SharedServices owns daemon-wide resources that are safe to reuse across
// workspace runtimes. Runtime-local state, approvals, patches, processes and
// workspace managers remain outside this type.
type SharedServices struct {
	version string

	mu                        sync.Mutex
	closed                    bool
	memoryProviders           map[string]memory.Provider
	memoryRecorders           map[string]*memory.Recorder
	auditWriters              map[string]*state.AuditWriter
	upstreamClients           map[string]*upstreammcp.Client
	upstreamModules           map[string]*upstreamMCPModule
	upstreamPending           map[string]*sharedUpstreamPending
	upstreamFailures          map[string]sharedUpstreamFailure
	upstreamBackgroundStarted map[string]bool
	backgroundCtx             context.Context
	backgroundCancel          context.CancelFunc
	backgroundWG              sync.WaitGroup
	memoryAcquires            uint64
	memoryReuses              uint64
	recorderAcquires          uint64
	recorderReuses            uint64
	auditAcquires             uint64
	auditReuses               uint64
	upstreamAcquires          uint64
	upstreamReuses            uint64
	contractAcquires          uint64
	contractReuses            uint64
	closeOnce                 sync.Once
	closeErr                  error
	memoryFactory             func(config.MemoryConfig) (memory.Provider, error)
	upstreamFactory           func(context.Context, string, config.MCPServerConfig, string, string) (*upstreammcp.Client, error)
}

type sharedMemoryLease struct {
	Provider       memory.Provider
	Recorder       *memory.Recorder
	ProviderReused bool
	RecorderReused bool
}

type sharedUpstreamLease struct {
	Module         *upstreamMCPModule
	ClientReused   bool
	ContractReused bool
	StartupMode    string
	Deferred       bool
	Bootstrapped   bool
}

type sharedUpstreamPending struct {
	done chan struct{}
}

type sharedUpstreamFailure struct {
	err     error
	retryAt time.Time
}

func NewSharedServices(version string) *SharedServices {
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	return &SharedServices{
		version:                   version,
		memoryProviders:           map[string]memory.Provider{},
		memoryRecorders:           map[string]*memory.Recorder{},
		auditWriters:              map[string]*state.AuditWriter{},
		upstreamClients:           map[string]*upstreammcp.Client{},
		upstreamModules:           map[string]*upstreamMCPModule{},
		upstreamPending:           map[string]*sharedUpstreamPending{},
		upstreamFailures:          map[string]sharedUpstreamFailure{},
		upstreamBackgroundStarted: map[string]bool{},
		backgroundCtx:             backgroundCtx,
		backgroundCancel:          backgroundCancel,
		memoryFactory:             memoryfactory.New,
		upstreamFactory:           upstreammcp.New,
	}
}

func (s *SharedServices) acquireAudit(path string) (*state.AuditWriter, error) {
	if s == nil {
		return nil, errors.New("shared services are required")
	}
	key := path
	if absolute, err := filepath.Abs(path); err == nil {
		key = absolute
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("shared services are closed")
	}
	if writer := s.auditWriters[key]; writer != nil {
		s.auditAcquires++
		s.auditReuses++
		return writer, nil
	}
	writer := state.NewAuditWriter(key, state.AuditWriterConfig{})
	s.auditWriters[key] = writer
	s.auditAcquires++
	return writer, nil
}

func (s *SharedServices) acquireMemory(cfg config.MemoryConfig) (sharedMemoryLease, error) {
	if s == nil {
		return sharedMemoryLease{}, errors.New("shared services are required")
	}
	providerKey := memoryProviderResourceKey(cfg)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return sharedMemoryLease{}, errors.New("shared services are closed")
	}
	if provider := s.memoryProviders[providerKey]; provider != nil {
		s.memoryAcquires++
		s.memoryReuses++
		s.mu.Unlock()
		return s.acquireRecorder(providerKey, provider, cfg, true)
	}
	s.mu.Unlock()

	provider, err := s.memoryFactory(cfg)
	if err != nil {
		return sharedMemoryLease{}, err
	}
	provider = wrapSharedMemoryProvider(provider)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = provider.Close()
		return sharedMemoryLease{}, errors.New("shared services are closed")
	}
	providerReused := false
	if existing := s.memoryProviders[providerKey]; existing != nil {
		providerReused = true
		_ = provider.Close()
		provider = existing
	} else {
		s.memoryProviders[providerKey] = provider
	}
	s.memoryAcquires++
	if providerReused {
		s.memoryReuses++
	}
	s.mu.Unlock()
	return s.acquireRecorder(providerKey, provider, cfg, providerReused)
}

func (s *SharedServices) acquireRecorder(providerKey string, provider memory.Provider, cfg config.MemoryConfig, providerReused bool) (sharedMemoryLease, error) {
	lease := sharedMemoryLease{Provider: provider, ProviderReused: providerReused}
	if !cfg.Enabled || cfg.CaptureMode == "off" || !provider.Capabilities().Observe {
		return lease, nil
	}
	recorderKey := memoryRecorderResourceKey(providerKey, cfg)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return sharedMemoryLease{}, errors.New("shared services are closed")
	}
	if recorder := s.memoryRecorders[recorderKey]; recorder != nil {
		s.recorderAcquires++
		s.recorderReuses++
		s.mu.Unlock()
		lease.Recorder = recorder
		lease.RecorderReused = true
		return lease, nil
	}
	s.mu.Unlock()

	recorder := memory.NewRecorderWithConfig(provider, memory.RecorderConfig{
		QueueSize:       cfg.QueueSize,
		Workers:         cfg.DeliveryWorkers,
		DeliveryTimeout: time.Duration(cfg.DeliveryTimeoutMS) * time.Millisecond,
		MaxAttempts:     cfg.RetryMaxAttempts,
		RetryBackoff:    time.Duration(cfg.RetryBackoffMS) * time.Millisecond,
	})

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		recorder.Close()
		return sharedMemoryLease{}, errors.New("shared services are closed")
	}
	if existing := s.memoryRecorders[recorderKey]; existing != nil {
		recorder.Close()
		recorder = existing
		lease.RecorderReused = true
		s.recorderReuses++
	} else {
		s.memoryRecorders[recorderKey] = recorder
	}
	s.recorderAcquires++
	s.mu.Unlock()
	lease.Recorder = recorder
	return lease, nil
}

func (s *SharedServices) acquireUpstream(ctx context.Context, runtime *Runtime, serverName string, cfg config.MCPServerConfig) (sharedUpstreamLease, error) {
	if s == nil {
		return sharedUpstreamLease{}, errors.New("shared services are required")
	}
	cwd, err := resolveUpstreamCWD(runtime, serverName, cfg)
	if err != nil {
		return sharedUpstreamLease{}, err
	}
	clientKey := upstreamClientResourceKey(serverName, cfg, cwd)
	startupMode := cfg.EffectiveStartupMode()
	catalogCached := false
	bootstrapped := false

	var client *upstreammcp.Client
	var clientReused bool
	if startupMode != config.MCPStartupModeEager {
		catalog, catalogErr := upstreammcp.LoadToolCatalog(clientKey)
		if catalogErr == nil {
			catalogErr = validateUpstreamMCPToolCatalog(serverName, cfg, catalog.Tools)
		}
		if catalogErr == nil {
			catalogCached = true
			client, clientReused, err = s.acquireUpstreamClientWith(
				ctx, cfg, clientKey, false,
				func() (*upstreammcp.Client, error) {
					return upstreammcp.NewDeferred(serverName, cfg, s.version, cwd, catalog.Tools)
				},
			)
		} else {
			// The first deferred startup must discover a typed tool contract once.
			// Later starts can register it without opening the transport.
			bootstrapped = true
			client, clientReused, err = s.acquireEagerUpstreamClient(ctx, serverName, cfg, cwd, clientKey)
		}
	} else {
		client, clientReused, err = s.acquireEagerUpstreamClient(ctx, serverName, cfg, cwd, clientKey)
	}
	if err != nil {
		return sharedUpstreamLease{}, err
	}
	if err := client.SetToolCatalogKey(clientKey); err != nil {
		return sharedUpstreamLease{}, err
	}
	deferred := catalogCached && client.Deferred()

	contractKey := upstreamContractResourceKey(clientKey, cfg)
	s.mu.Lock()
	module := s.upstreamModules[contractKey]
	contractReused := module != nil
	if contractReused {
		s.contractAcquires++
		s.contractReuses++
	}
	s.mu.Unlock()

	if module == nil {
		module, err = newUpstreamMCPModuleFromClient(serverName, cfg, client)
		if err != nil {
			return sharedUpstreamLease{}, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return sharedUpstreamLease{}, errors.New("shared services are closed")
		}
		if existing := s.upstreamModules[contractKey]; existing != nil {
			module = existing
			contractReused = true
			s.contractReuses++
		} else {
			s.upstreamModules[contractKey] = module
		}
		s.contractAcquires++
		s.mu.Unlock()
	}

	if startupMode == config.MCPStartupModeBackground && catalogCached {
		s.startUpstreamBackground(clientKey, client)
	}
	return sharedUpstreamLease{
		Module: module, ClientReused: clientReused, ContractReused: contractReused,
		StartupMode: startupMode, Deferred: deferred, Bootstrapped: bootstrapped,
	}, nil
}

type upstreamClientCreator func() (*upstreammcp.Client, error)

func (s *SharedServices) acquireEagerUpstreamClient(ctx context.Context, serverName string, cfg config.MCPServerConfig, cwd, clientKey string) (*upstreammcp.Client, bool, error) {
	client, reused, err := s.acquireUpstreamClientWith(
		ctx, cfg, clientKey, true,
		func() (*upstreammcp.Client, error) {
			return s.upstreamFactory(ctx, serverName, cfg, s.version, cwd)
		},
	)
	if err != nil {
		return nil, false, err
	}
	// An eager runtime may race with a cached deferred runtime for the same
	// connection key. Ensure the pooled client satisfies eager semantics.
	if err := client.EnsureConnected(ctx, true); err != nil {
		return nil, false, err
	}
	_ = upstreammcp.SaveToolCatalog(clientKey, client.Tools())
	return client, reused, nil
}

func (s *SharedServices) acquireUpstreamClientWith(ctx context.Context, cfg config.MCPServerConfig, clientKey string, cacheFailures bool, create upstreamClientCreator) (*upstreammcp.Client, bool, error) {
	for {
		now := time.Now()
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, false, errors.New("shared services are closed")
		}
		if client := s.upstreamClients[clientKey]; client != nil {
			s.upstreamAcquires++
			s.upstreamReuses++
			s.mu.Unlock()
			return client, true, nil
		}
		if failure, ok := s.upstreamFailures[clientKey]; ok {
			if cacheFailures && now.Before(failure.retryAt) {
				s.mu.Unlock()
				return nil, false, failure.err
			}
			if !now.Before(failure.retryAt) {
				delete(s.upstreamFailures, clientKey)
			}
		}
		if pending := s.upstreamPending[clientKey]; pending != nil {
			done := pending.done
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
		}
		pending := &sharedUpstreamPending{done: make(chan struct{})}
		s.upstreamPending[clientKey] = pending
		s.mu.Unlock()

		created, createErr := create()
		s.mu.Lock()
		delete(s.upstreamPending, clientKey)
		if s.closed {
			close(pending.done)
			s.mu.Unlock()
			if created != nil {
				_ = created.Close()
			}
			return nil, false, errors.New("shared services are closed")
		}
		if createErr != nil {
			if cacheFailures && ctx.Err() == nil {
				cooldown := time.Duration(cfg.FailureCooldownMS) * time.Millisecond
				if cooldown <= 0 {
					cooldown = time.Duration(config.DefaultMCPFailureCooldownMS) * time.Millisecond
				}
				s.upstreamFailures[clientKey] = sharedUpstreamFailure{err: createErr, retryAt: time.Now().Add(cooldown)}
			}
			close(pending.done)
			s.mu.Unlock()
			if created != nil {
				_ = created.Close()
			}
			return nil, false, createErr
		}
		delete(s.upstreamFailures, clientKey)
		client := created
		clientReused := false
		closeCreated := false
		if existing := s.upstreamClients[clientKey]; existing != nil {
			client = existing
			clientReused = true
			closeCreated = true
		} else {
			s.upstreamClients[clientKey] = client
		}
		s.upstreamAcquires++
		if clientReused {
			s.upstreamReuses++
		}
		close(pending.done)
		s.mu.Unlock()
		if closeCreated {
			_ = created.Close()
		}
		return client, clientReused, nil
	}
}

func (s *SharedServices) startUpstreamBackground(clientKey string, client *upstreammcp.Client) {
	s.mu.Lock()
	if s.closed || s.upstreamBackgroundStarted[clientKey] {
		s.mu.Unlock()
		return
	}
	s.upstreamBackgroundStarted[clientKey] = true
	ctx := s.backgroundCtx
	s.backgroundWG.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.backgroundWG.Done()
		if err := client.EnsureConnected(ctx, true); err == nil {
			_ = upstreammcp.SaveToolCatalog(clientKey, client.Tools())
		}
	}()
}

func (s *SharedServices) Stats() map[string]any {
	if s == nil {
		return map[string]any{"enabled": false}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"enabled": true,
		"closed":  s.closed,
		"memory": map[string]any{
			"providers": len(s.memoryProviders), "recorders": len(s.memoryRecorders),
			"acquires": s.memoryAcquires, "reuses": s.memoryReuses,
			"recorder_acquires": s.recorderAcquires, "recorder_reuses": s.recorderReuses,
		},
		"audit": map[string]any{
			"writers": len(s.auditWriters), "acquires": s.auditAcquires, "reuses": s.auditReuses,
		},
		"upstream_mcp": map[string]any{
			"clients": len(s.upstreamClients), "contracts": len(s.upstreamModules),
			"pending": len(s.upstreamPending), "failure_cache": len(s.upstreamFailures),
			"background_started": len(s.upstreamBackgroundStarted),
			"client_acquires":    s.upstreamAcquires, "client_reuses": s.upstreamReuses,
			"contract_acquires": s.contractAcquires, "contract_reuses": s.contractReuses,
		},
	}
}

func (s *SharedServices) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		if s.backgroundCancel != nil {
			s.backgroundCancel()
		}
		clients := make([]*upstreammcp.Client, 0, len(s.upstreamClients))
		for _, client := range s.upstreamClients {
			clients = append(clients, client)
		}
		auditWriters := make([]*state.AuditWriter, 0, len(s.auditWriters))
		for _, writer := range s.auditWriters {
			auditWriters = append(auditWriters, writer)
		}
		recorders := make([]*memory.Recorder, 0, len(s.memoryRecorders))
		for _, recorder := range s.memoryRecorders {
			recorders = append(recorders, recorder)
		}
		providers := make([]memory.Provider, 0, len(s.memoryProviders))
		for _, provider := range s.memoryProviders {
			providers = append(providers, provider)
		}
		s.mu.Unlock()
		s.backgroundWG.Wait()

		var errs []error
		for _, writer := range auditWriters {
			if err := writer.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close shared audit writer: %w", err))
			}
		}
		if len(recorders) > 0 {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			recorderErrors := make(chan error, len(recorders))
			var recorderWG sync.WaitGroup
			for _, recorder := range recorders {
				recorderWG.Add(1)
				go func(recorder *memory.Recorder) {
					defer recorderWG.Done()
					if err := recorder.CloseContext(closeCtx); err != nil {
						recorderErrors <- fmt.Errorf("close shared memory recorder: %w", err)
					}
				}(recorder)
			}
			recorderWG.Wait()
			cancel()
			close(recorderErrors)
			for err := range recorderErrors {
				errs = append(errs, err)
			}
		}
		for _, provider := range providers {
			if err := provider.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close shared memory provider %q: %w", provider.Name(), err))
			}
		}
		for _, client := range clients {
			if err := client.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close shared upstream MCP client: %w", err))
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

func memoryProviderResourceKey(cfg config.MemoryConfig) string {
	if !cfg.Enabled || cfg.Provider == "" || cfg.Provider == "none" {
		return resourceDigest(map[string]any{"provider": "none", "enabled": false})
	}
	secretFingerprint := ""
	if cfg.SecretEnv != "" {
		secretFingerprint = secretValueFingerprint(os.Getenv(cfg.SecretEnv))
	}
	return resourceDigest(map[string]any{
		"provider":           cfg.Provider,
		"endpoint":           cfg.Endpoint,
		"timeout_ms":         cfg.TimeoutMS,
		"options":            cfg.Options,
		"secret_env":         cfg.SecretEnv,
		"secret_fingerprint": secretFingerprint,
	})
}

func memoryRecorderResourceKey(providerKey string, cfg config.MemoryConfig) string {
	return resourceDigest(map[string]any{
		"provider": providerKey, "queue_size": cfg.QueueSize,
		"delivery_workers": cfg.DeliveryWorkers, "delivery_timeout_ms": cfg.DeliveryTimeoutMS,
		"retry_max_attempts": cfg.RetryMaxAttempts, "retry_backoff_ms": cfg.RetryBackoffMS,
	})
}

func upstreamClientResourceKey(serverName string, cfg config.MCPServerConfig, cwd string) string {
	connection := cfg
	connection.Enabled = nil
	connection.Required = false
	connection.StartupMode = ""
	connection.WorkspaceIDs = nil
	connection.AllowedTools = nil
	connection.DeniedTools = nil
	connection.CWD = ""
	connection.Policy = config.MCPServerPolicyConfig{}
	fingerprints := config.MCPServerSecretFingerprints(map[string]config.MCPServerConfig{serverName: cfg})
	return resourceDigest(map[string]any{
		"server": serverName, "config": connection, "cwd": cwd,
		"secret_fingerprints": fingerprints[serverName],
	})
}

func upstreamContractResourceKey(clientKey string, cfg config.MCPServerConfig) string {
	return resourceDigest(map[string]any{
		"client": clientKey, "allowed_tools": cfg.AllowedTools, "denied_tools": cfg.DeniedTools,
		"policy": cfg.Policy,
	})
}

func resourceDigest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

func secretValueFingerprint(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
