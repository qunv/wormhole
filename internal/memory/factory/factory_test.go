package factory

import (
	"testing"

	"codebridge/internal/config"
	"codebridge/internal/memory"
	"codebridge/internal/memory/noop"
)

func TestRegistryContainsBuiltInProviders(t *testing.T) {
	names := Names()
	seen := map[string]bool{}
	for _, name := range names {
		seen[name] = true
	}
	for _, want := range []string{"none", "agentmemory"} {
		if !seen[want] {
			t.Fatalf("provider registry missing %q: %v", want, names)
		}
	}
}

func TestRegisterCustomProvider(t *testing.T) {
	Register("test-provider", func(config.MemoryConfig) (memory.Provider, error) {
		return noop.New(), nil
	})
	cfg := config.Default().Memory
	cfg.Enabled = true
	cfg.Provider = "test-provider"
	provider, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	if provider.Name() != "none" {
		t.Fatalf("custom constructor returned provider %q", provider.Name())
	}
}
