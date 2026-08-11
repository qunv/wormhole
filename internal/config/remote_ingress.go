// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// RemoteIngressConfig publishes one fixed workspace/profile MCP contract
// through a dedicated loopback listener. Provider credentials and the MCP
// bearer credentials are referenced by environment variable and are never
// persisted in config.json. AuthTokenFallbackEnv is optional and exists only
// to overlap credentials during a staged rotation without a credential cutover gap.
type RemoteIngressConfig struct {
	Enabled              *bool  `json:"enabled,omitempty"`
	Provider             string `json:"provider,omitempty"`
	WorkspaceID          string `json:"workspaceId,omitempty"`
	ToolProfile          string `json:"toolProfile,omitempty"`
	LocalPort            int    `json:"localPort"`
	PublicURL            string `json:"publicUrl,omitempty"`
	AuthTokenEnv         string `json:"authTokenEnv"`
	AuthTokenFallbackEnv string `json:"authTokenFallbackEnv,omitempty"`
	ProviderTokenEnv     string `json:"providerTokenEnv,omitempty"`
	Binary               string `json:"binary,omitempty"`
}

func (c RemoteIngressConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

type NamedRemoteIngress struct {
	Name   string
	Config RemoteIngressConfig
}

var remoteIngressNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func RemoteIngressLogPathFor(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if !remoteIngressNamePattern.MatchString(name) {
		sum := sha256.Sum256([]byte(name))
		name = "invalid-" + hex.EncodeToString(sum[:4])
	}
	return filepath.Join(AppDataDir(), "remote-ingress-"+name+".log")
}

func normalizeRemoteIngresses(c *Config) {
	if len(c.RemoteIngresses) == 0 {
		return
	}
	normalized := make(map[string]RemoteIngressConfig, len(c.RemoteIngresses))
	for rawName, ingress := range c.RemoteIngresses {
		name := strings.ToLower(strings.TrimSpace(rawName))
		ingress.Provider = strings.ToLower(strings.TrimSpace(ingress.Provider))
		if ingress.Provider == "" {
			ingress.Provider = "external"
		}
		ingress.WorkspaceID = strings.ToLower(strings.TrimSpace(ingress.WorkspaceID))
		ingress.ToolProfile = strings.ToLower(strings.TrimSpace(ingress.ToolProfile))
		if ingress.ToolProfile == "" {
			ingress.ToolProfile = "remote-read"
		}
		ingress.PublicURL = strings.TrimSpace(ingress.PublicURL)
		ingress.AuthTokenEnv = strings.TrimSpace(ingress.AuthTokenEnv)
		if ingress.AuthTokenEnv == "" {
			ingress.AuthTokenEnv = derivedIngressEnv(name, "AUTH_TOKEN")
		}
		ingress.AuthTokenFallbackEnv = strings.TrimSpace(ingress.AuthTokenFallbackEnv)
		ingress.ProviderTokenEnv = strings.TrimSpace(ingress.ProviderTokenEnv)
		ingress.Binary = strings.TrimSpace(ingress.Binary)
		if ingress.Provider == "cloudflare" {
			if ingress.ProviderTokenEnv == "" {
				ingress.ProviderTokenEnv = derivedIngressEnv(name, "TUNNEL_TOKEN")
			}
			if ingress.Binary == "" {
				ingress.Binary = "cloudflared"
			}
		}
		normalized[name] = ingress
	}
	c.RemoteIngresses = normalized
}

func derivedIngressEnv(name, suffix string) string {
	name = strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
	return "WORMHOLE_REMOTE_" + name + "_" + suffix
}

func validateRemoteIngressMapKeys(ingresses map[string]RemoteIngressConfig) error {
	seen := map[string]string{}
	for rawName := range ingresses {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if !remoteIngressNamePattern.MatchString(name) {
			return fmt.Errorf("remoteIngresses name %q must match %s", rawName, remoteIngressNamePattern.String())
		}
		if previous := seen[name]; previous != "" {
			return fmt.Errorf("remote ingress names %q and %q normalize to the same value %q", previous, rawName, name)
		}
		seen[name] = rawName
	}
	return nil
}

func validateRemoteIngresses(c Config) error {
	if err := validateRemoteIngressMapKeys(c.RemoteIngresses); err != nil {
		return err
	}
	ports := map[int]string{}
	authRefs := map[string]string{}
	providerRefs := map[string]string{}
	for rawName, ingress := range c.RemoteIngresses {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if !ingress.IsEnabled() {
			continue
		}
		if ingress.Provider != "cloudflare" && ingress.Provider != "external" {
			return fmt.Errorf("remoteIngresses.%s.provider must be external or cloudflare", name)
		}
		if ingress.LocalPort < 1 || ingress.LocalPort > 65535 {
			return fmt.Errorf("remoteIngresses.%s.localPort must be between 1 and 65535", name)
		}
		if ingress.LocalPort == c.Port {
			return fmt.Errorf("remoteIngresses.%s.localPort must differ from the main MCP port", name)
		}
		if previous := ports[ingress.LocalPort]; previous != "" {
			return fmt.Errorf("remoteIngresses.%s.localPort duplicates remoteIngresses.%s.localPort", name, previous)
		}
		ports[ingress.LocalPort] = name
		if ingress.WorkspaceID != "" && !mcpWorkspaceIDPattern.MatchString(ingress.WorkspaceID) {
			return fmt.Errorf("remoteIngresses.%s.workspaceId must match [a-z0-9][a-z0-9_-]{0,31}", name)
		}
		profile := strings.ToLower(strings.TrimSpace(ingress.ToolProfile))
		if profile != "fast" && profile != "full" && profile != "remote-read" {
			if _, exists := c.ToolProfiles[profile]; !exists {
				return fmt.Errorf("remoteIngresses.%s.toolProfile references unknown tool profile %q", name, profile)
			}
		}
		if !envNamePattern.MatchString(ingress.AuthTokenEnv) {
			return fmt.Errorf("remoteIngresses.%s.authTokenEnv is not a valid environment variable name", name)
		}
		if ingress.AuthTokenFallbackEnv != "" {
			if !envNamePattern.MatchString(ingress.AuthTokenFallbackEnv) {
				return fmt.Errorf("remoteIngresses.%s.authTokenFallbackEnv is not a valid environment variable name", name)
			}
			if ingress.AuthTokenFallbackEnv == ingress.AuthTokenEnv {
				return fmt.Errorf("remoteIngresses.%s authTokenEnv and authTokenFallbackEnv must be different", name)
			}
		}
		if ingress.Provider == "cloudflare" {
			if !envNamePattern.MatchString(ingress.ProviderTokenEnv) {
				return fmt.Errorf("remoteIngresses.%s.providerTokenEnv is not a valid environment variable name", name)
			}
			if ingress.AuthTokenEnv == ingress.ProviderTokenEnv || ingress.AuthTokenFallbackEnv == ingress.ProviderTokenEnv {
				return fmt.Errorf("remoteIngresses.%s MCP auth token refs and providerTokenEnv must be different", name)
			}
		} else if ingress.ProviderTokenEnv != "" || ingress.Binary != "" {
			return fmt.Errorf("remoteIngresses.%s providerTokenEnv and binary are only supported for cloudflare provider", name)
		}
		for field, envName := range map[string]string{"authTokenEnv": ingress.AuthTokenEnv, "authTokenFallbackEnv": ingress.AuthTokenFallbackEnv} {
			if envName == "" {
				continue
			}
			if previous := authRefs[envName]; previous != "" {
				return fmt.Errorf("remoteIngresses.%s.%s duplicates remote ingress auth ref %s", name, field, previous)
			}
			authRefs[envName] = name + "." + field
		}
		if ingress.ProviderTokenEnv != "" {
			if previous := providerRefs[ingress.ProviderTokenEnv]; previous != "" {
				return fmt.Errorf("remoteIngresses.%s.providerTokenEnv duplicates remoteIngresses.%s.providerTokenEnv", name, previous)
			}
			providerRefs[ingress.ProviderTokenEnv] = name
		}
		if ingress.Provider == "cloudflare" && (len(ingress.Binary) > 4096 || strings.IndexByte(ingress.Binary, 0) >= 0) {
			return fmt.Errorf("remoteIngresses.%s.binary is too long or contains a NUL byte", name)
		}
		if ingress.PublicURL != "" {
			parsed, err := url.Parse(ingress.PublicURL)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return fmt.Errorf("remoteIngresses.%s.publicUrl must be an https URL without credentials, query, or fragment", name)
			}
			if parsed.Path != "/mcp" {
				return fmt.Errorf("remoteIngresses.%s.publicUrl path must be /mcp", name)
			}
		}
	}
	return nil
}

func (c Config) EffectiveRemoteIngresses() []NamedRemoteIngress {
	names := make([]string, 0, len(c.RemoteIngresses))
	for name := range c.RemoteIngresses {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]NamedRemoteIngress, 0, len(names))
	for _, name := range names {
		out = append(out, NamedRemoteIngress{Name: name, Config: c.RemoteIngresses[name]})
	}
	return out
}

func (c Config) EnabledRemoteIngresses() []NamedRemoteIngress {
	out := []NamedRemoteIngress{}
	for _, ingress := range c.EffectiveRemoteIngresses() {
		if ingress.Config.IsEnabled() {
			out = append(out, ingress)
		}
	}
	return out
}

// RemoteIngressSecretFingerprints returns one-way secret fingerprints used in
// daemon identity. It never returns the raw environment values.
func RemoteIngressSecretFingerprints(ingresses map[string]RemoteIngressConfig) map[string]map[string]string {
	out := map[string]map[string]string{}
	for name, ingress := range ingresses {
		values := map[string]string{}
		for label, envName := range map[string]string{"auth": ingress.AuthTokenEnv, "auth_fallback": ingress.AuthTokenFallbackEnv, "provider": ingress.ProviderTokenEnv} {
			if envName == "" {
				continue
			}
			if value := os.Getenv(envName); value != "" {
				sum := sha256.Sum256([]byte(value))
				values[label] = hex.EncodeToString(sum[:8])
			}
		}
		if len(values) > 0 {
			out[name] = values
		}
	}
	return out
}
