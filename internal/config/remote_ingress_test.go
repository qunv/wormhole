// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"strings"
	"testing"
)

func TestRemoteIngressNormalizationAndValidation(t *testing.T) {
	cfg := Default()
	cfg.RemoteIngresses = map[string]RemoteIngressConfig{
		"Notion-Agent": {LocalPort: 8133, PublicURL: "https://wormhole.example.com/mcp"},
	}
	prepared, err := Prepare(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg = prepared
	ingress, ok := cfg.RemoteIngresses["notion-agent"]
	if !ok {
		t.Fatalf("normalized ingress missing: %#v", cfg.RemoteIngresses)
	}
	if ingress.Provider != "external" || ingress.ToolProfile != "remote-read" || ingress.Binary != "" {
		t.Fatalf("unexpected defaults: %#v", ingress)
	}
	if ingress.AuthTokenEnv != "WORMHOLE_REMOTE_NOTION_AGENT_AUTH_TOKEN" || ingress.ProviderTokenEnv != "" {
		t.Fatalf("unexpected secret refs: %#v", ingress)
	}
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("valid remote ingress rejected: %v", err)
	}

	collision := Default()
	collision.RemoteIngresses = map[string]RemoteIngressConfig{
		"Notion": {LocalPort: 8133},
		"notion": {LocalPort: 8134},
	}
	if _, err := Prepare(collision); err == nil || !strings.Contains(err.Error(), "normalize to the same value") {
		t.Fatalf("case-colliding ingress names were accepted: %v", err)
	}
}

func TestExternalRemoteIngressDoesNotRequireManagedProviderFields(t *testing.T) {
	cfg := Default()
	cfg.RemoteIngresses = map[string]RemoteIngressConfig{
		"notion": {
			Provider: "external", LocalPort: 8133, ToolProfile: "remote-read",
			AuthTokenEnv: "REMOTE_AUTH", PublicURL: "https://wormhole.example.com/mcp",
		},
	}
	if _, err := Prepare(cfg); err != nil {
		t.Fatalf("external remote ingress rejected: %v", err)
	}

	cfg.RemoteIngresses["notion"] = RemoteIngressConfig{
		Provider: "external", LocalPort: 8133, ToolProfile: "remote-read",
		AuthTokenEnv: "REMOTE_AUTH", ProviderTokenEnv: "SHOULD_NOT_BE_USED",
	}
	if _, err := Prepare(cfg); err == nil || !strings.Contains(err.Error(), "only supported for cloudflare") {
		t.Fatalf("external provider accepted managed provider secret: %v", err)
	}
}

func TestRemoteIngressValidationFailsClosed(t *testing.T) {
	base := Default()
	base.RemoteIngresses = map[string]RemoteIngressConfig{
		"notion": {
			Provider: "cloudflare", LocalPort: 8133, ToolProfile: "fast",
			AuthTokenEnv: "REMOTE_AUTH", ProviderTokenEnv: "REMOTE_PROVIDER",
			Binary: "cloudflared", PublicURL: "https://wormhole.example.com/mcp",
		},
	}

	tests := map[string]func(*Config){
		"main port reused": func(cfg *Config) {
			ingress := cfg.RemoteIngresses["notion"]
			ingress.LocalPort = cfg.Port
			cfg.RemoteIngresses["notion"] = ingress
		},
		"public URL not https": func(cfg *Config) {
			ingress := cfg.RemoteIngresses["notion"]
			ingress.PublicURL = "http://wormhole.example.com/mcp"
			cfg.RemoteIngresses["notion"] = ingress
		},
		"public URL wrong path": func(cfg *Config) {
			ingress := cfg.RemoteIngresses["notion"]
			ingress.PublicURL = "https://wormhole.example.com/admin"
			cfg.RemoteIngresses["notion"] = ingress
		},
		"shared secret ref": func(cfg *Config) {
			ingress := cfg.RemoteIngresses["notion"]
			ingress.ProviderTokenEnv = ingress.AuthTokenEnv
			cfg.RemoteIngresses["notion"] = ingress
		},
		"unknown profile": func(cfg *Config) {
			ingress := cfg.RemoteIngresses["notion"]
			ingress.ToolProfile = "missing"
			cfg.RemoteIngresses["notion"] = ingress
		},
		"duplicate auth ref": func(cfg *Config) {
			cfg.RemoteIngresses["other"] = RemoteIngressConfig{
				Provider: "cloudflare", LocalPort: 8134, ToolProfile: "fast",
				AuthTokenEnv: "REMOTE_AUTH", ProviderTokenEnv: "OTHER_PROVIDER", Binary: "cloudflared",
			}
		},
		"duplicate provider ref": func(cfg *Config) {
			cfg.RemoteIngresses["other"] = RemoteIngressConfig{
				Provider: "cloudflare", LocalPort: 8134, ToolProfile: "fast",
				AuthTokenEnv: "OTHER_AUTH", ProviderTokenEnv: "REMOTE_PROVIDER", Binary: "cloudflared",
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := base
			cfg.RemoteIngresses = map[string]RemoteIngressConfig{"notion": base.RemoteIngresses["notion"]}
			mutate(&cfg)
			if err := cfg.Validate(false); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestConfigIDTracksRemoteIngressSecretsWithoutEmbeddingThem(t *testing.T) {
	cfg := Default()
	cfg.RemoteIngresses = map[string]RemoteIngressConfig{
		"notion": {
			Provider: "cloudflare", LocalPort: 8133, ToolProfile: "fast",
			AuthTokenEnv: "REMOTE_AUTH", ProviderTokenEnv: "REMOTE_PROVIDER", Binary: "cloudflared",
		},
	}
	t.Setenv("REMOTE_AUTH", "first-auth-secret")
	t.Setenv("REMOTE_PROVIDER", "provider-secret")
	first := cfg.ConfigIDWithInputs(IdentityInputs{BinaryHash: "binary", WidgetHash: "widget"})
	t.Setenv("REMOTE_AUTH", "second-auth-secret")
	second := cfg.ConfigIDWithInputs(IdentityInputs{BinaryHash: "binary", WidgetHash: "widget"})
	if first == second {
		t.Fatal("ConfigID did not change when remote MCP bearer changed")
	}
	for _, secret := range []string{"first-auth-secret", "second-auth-secret", "provider-secret"} {
		if strings.Contains(first, secret) || strings.Contains(second, secret) {
			t.Fatalf("ConfigID exposed secret %q", secret)
		}
	}
}
