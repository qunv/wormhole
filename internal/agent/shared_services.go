// Codebridge
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
	"sync"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/memory"
	memoryfactory "codebridge/internal/memory/factory"
	"codebridge/internal/upstreammcp"
)

// SharedServices owns daemon-wide resources that are safe to reuse across
// workspace runtimes. Runtime-local state, approvals, patches, processes and
// workspace managers remain outside this type.
type SharedServices struct {
	version string

	mu               sync.Mutex
	closed           bool
	memoryProviders  map[string]memory.Provider
	memoryRecorders  map[string]*memory.Recorder
	upstreamClients  map[string]*upstreammcp.Client
	upstreamModules  map[string]*upstreamMCPModule
	memoryAcquires   uint64
	memoryReuses     uint64
	recorderAcquires uint64
	recorderReuses   uint64
	upstreamAcquires uint64
	upstreamReuses   uint64
	contractAcquires uint64
	contractReuses   uint64
	closeOnce        sync.Once
	closeErr         error
	memoryFactory    func(config.MemoryConfig) (memory.Provider, error)
	upstreamFactory  func(context.Context, string, config.MCPServerConfig, string, string) (*upstreammcp.Client, error)
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
}

func NewSharedServices(version string) *SharedServices {
	return &SharedServices{
		version:         version,
		memoryProviders: map[string]memory.Provider{},
		memoryRecorders: map[string]*memory.Recorder{},
		upstreamClients: map[string]*upstreammcp.Client{},
		upstreamModules: map[string]*upstreamMCPModule{},
		memoryFactory:   memoryfactory.New,
		upstreamFactory: upstreammcp.New,
	}
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

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return sharedUpstreamLease{}, errors.New("shared services are closed")
	}
	client := s.upstreamClients[clientKey]
	clientReused := client != nil
	s.mu.Unlock()

	if client == nil {
		created, createErr := s.upstreamFactory(ctx, serverName, cfg, s.version, cwd)
		if createErr != nil {
			return sharedUpstreamLease{}, createErr
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = created.Close()
			return sharedUpstreamLease{}, errors.New("shared services are closed")
		}
		if existing := s.upstreamClients[clientKey]; existing != nil {
			client = existing
			clientReused = true
			_ = created.Close()
		} else {
			client = created
			s.upstreamClients[clientKey] = client
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	s.upstreamAcquires++
	if clientReused {
		s.upstreamReuses++
	}
	s.mu.Unlock()

	contractKey := upstreamContractResourceKey(clientKey, cfg)
	s.mu.Lock()
	if module := s.upstreamModules[contractKey]; module != nil {
		s.contractAcquires++
		s.contractReuses++
		s.mu.Unlock()
		return sharedUpstreamLease{Module: module, ClientReused: clientReused, ContractReused: true}, nil
	}
	s.mu.Unlock()

	module, err := newUpstreamMCPModuleFromClient(serverName, cfg, client)
	if err != nil {
		return sharedUpstreamLease{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return sharedUpstreamLease{}, errors.New("shared services are closed")
	}
	contractReused := false
	if existing := s.upstreamModules[contractKey]; existing != nil {
		module = existing
		contractReused = true
		s.contractReuses++
	} else {
		s.upstreamModules[contractKey] = module
	}
	s.contractAcquires++
	s.mu.Unlock()
	return sharedUpstreamLease{Module: module, ClientReused: clientReused, ContractReused: contractReused}, nil
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
		"upstream_mcp": map[string]any{
			"clients": len(s.upstreamClients), "contracts": len(s.upstreamModules),
			"client_acquires": s.upstreamAcquires, "client_reuses": s.upstreamReuses,
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
		clients := make([]*upstreammcp.Client, 0, len(s.upstreamClients))
		for _, client := range s.upstreamClients {
			clients = append(clients, client)
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

		var errs []error
		for _, client := range clients {
			if err := client.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close shared upstream MCP client: %w", err))
			}
		}
		for _, recorder := range recorders {
			recorder.Close()
		}
		for _, provider := range providers {
			if err := provider.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close shared memory provider %q: %w", provider.Name(), err))
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
		"provider":   providerKey,
		"queue_size": cfg.QueueSize, "delivery_timeout_ms": cfg.DeliveryTimeoutMS,
		"retry_max_attempts": cfg.RetryMaxAttempts, "retry_backoff_ms": cfg.RetryBackoffMS,
	})
}

func upstreamClientResourceKey(serverName string, cfg config.MCPServerConfig, cwd string) string {
	connection := cfg
	connection.Enabled = nil
	connection.Required = false
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
