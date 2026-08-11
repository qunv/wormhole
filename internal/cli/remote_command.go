// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"wormhole/internal/config"
)

type remoteIngressListItem struct {
	Name                    string `json:"name"`
	Enabled                 bool   `json:"enabled"`
	Provider                string `json:"provider"`
	Mode                    string `json:"mode"`
	WorkspaceID             string `json:"workspaceId"`
	ToolProfile             string `json:"toolProfile"`
	LocalURL                string `json:"localUrl"`
	PublicURL               string `json:"publicUrl,omitempty"`
	AuthTokenEnv            string `json:"authTokenEnv"`
	AuthTokenFallbackEnv    string `json:"authTokenFallbackEnv,omitempty"`
	AuthConfigured          bool   `json:"authConfigured"`
	PrimaryAuthConfigured   bool   `json:"primaryAuthConfigured"`
	FallbackAuthConfigured  *bool  `json:"fallbackAuthConfigured,omitempty"`
	ProviderTokenEnv        string `json:"providerTokenEnv,omitempty"`
	ProviderTokenConfigured *bool  `json:"providerTokenConfigured,omitempty"`
}

type remoteEndpointVerification struct {
	URL             string `json:"url"`
	OK              bool   `json:"ok"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	ToolCount       int    `json:"toolCount,omitempty"`
	ContractHash    string `json:"contractHash,omitempty"`
	Error           string `json:"error,omitempty"`
}

type remoteIngressVerification struct {
	Name        string                     `json:"name"`
	Provider    string                     `json:"provider"`
	Mode        string                     `json:"mode"`
	WorkspaceID string                     `json:"workspaceId"`
	ToolProfile string                     `json:"toolProfile"`
	Local       remoteEndpointVerification `json:"local"`
	Public      remoteEndpointVerification `json:"public"`
	Matches     bool                       `json:"matches"`
	OK          bool                       `json:"ok"`
	Issue       string                     `json:"issue,omitempty"`
}

func (a App) remoteCommand(ctx context.Context, cfg config.Config, opts options) error {
	sub := "list"
	if len(opts.Rest) > 0 {
		sub = strings.ToLower(strings.TrimSpace(opts.Rest[0]))
	}
	switch sub {
	case "list", "status":
		return a.remoteList(cfg, opts)
	case "verify", "check":
		if len(opts.Rest) < 2 || strings.TrimSpace(opts.Rest[1]) == "" {
			return errors.New("usage: wormhole remote verify <name> [--json]")
		}
		return a.remoteVerify(ctx, cfg, strings.TrimSpace(opts.Rest[1]), opts)
	default:
		return fmt.Errorf("unknown remote command %q; use list or verify", sub)
	}
}

func (a App) remoteList(cfg config.Config, opts options) error {
	ingresses := cfg.EffectiveRemoteIngresses()
	items := make([]remoteIngressListItem, 0, len(ingresses))
	for _, ingress := range ingresses {
		mode := ingress.Config.EffectiveMode()
		workspaceID := remoteIngressWorkspaceLabel(ingress.Config)
		_, primaryConfigured, fallbackConfigured := remoteIngressAuthToken(ingress.Config)
		item := remoteIngressListItem{
			Name: ingress.Name, Enabled: ingress.Config.IsEnabled(), Provider: ingress.Config.Provider, Mode: mode,
			WorkspaceID: workspaceID, ToolProfile: ingress.Config.ToolProfile,
			LocalURL: fmt.Sprintf("http://127.0.0.1:%d/mcp", ingress.Config.LocalPort), PublicURL: ingress.Config.PublicURL,
			AuthTokenEnv: ingress.Config.AuthTokenEnv, AuthTokenFallbackEnv: ingress.Config.AuthTokenFallbackEnv,
			AuthConfigured: primaryConfigured || fallbackConfigured, PrimaryAuthConfigured: primaryConfigured,
			ProviderTokenEnv: ingress.Config.ProviderTokenEnv,
		}
		if ingress.Config.AuthTokenFallbackEnv != "" {
			item.FallbackAuthConfigured = &fallbackConfigured
		}
		if ingress.Config.ProviderTokenEnv != "" {
			configured := strings.TrimSpace(os.Getenv(ingress.Config.ProviderTokenEnv)) != ""
			item.ProviderTokenConfigured = &configured
		}
		items = append(items, item)
	}
	if opts.JSON {
		raw, _ := json.MarshalIndent(map[string]any{"remoteIngresses": items}, "", "  ")
		fmt.Fprintln(a.Stdout, string(raw))
		return nil
	}
	if len(items) == 0 {
		fmt.Fprintln(a.Stdout, "No remote MCP ingresses configured.")
		return nil
	}
	for _, item := range items {
		state := "enabled"
		if !item.Enabled {
			state = "disabled"
		}
		public := item.PublicURL
		if public == "" {
			public = "not-recorded"
		}
		fmt.Fprintf(a.Stdout, "%s %-8s provider=%s mode=%s workspace=%s profile=%s local=%s public=%s auth=%s\n",
			item.Name, state, item.Provider, item.Mode, item.WorkspaceID, item.ToolProfile, item.LocalURL, public, ternary(item.AuthConfigured, "set", "missing"))
	}
	return nil
}

func (a App) remoteVerify(ctx context.Context, cfg config.Config, name string, opts options) error {
	ingress, ok := findRemoteIngress(cfg, name)
	if !ok {
		return fmt.Errorf("remote ingress %q is not configured", name)
	}
	mode := ingress.Config.EffectiveMode()
	workspaceID := remoteIngressWorkspaceLabel(ingress.Config)
	result := remoteIngressVerification{
		Name: ingress.Name, Provider: ingress.Config.Provider, Mode: mode,
		WorkspaceID: workspaceID, ToolProfile: ingress.Config.ToolProfile,
		Local:  remoteEndpointVerification{URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", ingress.Config.LocalPort)},
		Public: remoteEndpointVerification{URL: ingress.Config.PublicURL},
	}
	if !ingress.Config.IsEnabled() {
		result.Issue = "remote ingress is disabled"
		return a.writeRemoteVerification(result, opts)
	}
	token, _, _ := remoteIngressAuthToken(ingress.Config)
	if token == "" {
		result.Issue = "MCP bearer secrets are missing"
		result.Local.Error = result.Issue
		result.Public.Error = result.Issue
		return a.writeRemoteVerification(result, opts)
	}

	localCtx, cancelLocal := context.WithTimeout(ctx, 4*time.Second)
	localProbe, localErr := probeRemoteIngress(localCtx, ingress)
	cancelLocal()
	applyRemoteProbe(&result.Local, localProbe, localErr)

	if strings.TrimSpace(ingress.Config.PublicURL) == "" {
		result.Public.Error = "publicUrl is not configured"
	} else {
		publicCtx, cancelPublic := context.WithTimeout(ctx, 10*time.Second)
		publicProbe, publicErr := probeRemoteMCPURL(publicCtx, ingress.Config.PublicURL, token)
		cancelPublic()
		applyRemoteProbe(&result.Public, publicProbe, publicErr)
	}

	if result.Local.OK && result.Public.OK {
		result.Matches = result.Local.ProtocolVersion == result.Public.ProtocolVersion && result.Local.ContractHash == result.Public.ContractHash
	}
	result.OK = result.Local.OK && result.Public.OK && result.Matches
	switch {
	case !result.Local.OK:
		result.Issue = "local remote MCP ingress verification failed"
	case !result.Public.OK:
		result.Issue = "public remote MCP endpoint verification failed"
	case !result.Matches:
		result.Issue = "public MCP contract does not match the local ingress"
	}
	return a.writeRemoteVerification(result, opts)
}

func applyRemoteProbe(target *remoteEndpointVerification, probe remoteIngressProbeResult, err error) {
	if err != nil {
		target.Error = boundedRemoteVerifyError(err)
		return
	}
	target.OK = true
	target.ProtocolVersion = probe.ProtocolVersion
	target.ToolCount = probe.ToolCount
	target.ContractHash = probe.ContractHash
}

func remoteIngressWorkspaceLabel(cfg config.RemoteIngressConfig) string {
	if cfg.EffectiveMode() == "session" {
		return "dynamic"
	}
	if workspaceID := strings.TrimSpace(cfg.WorkspaceID); workspaceID != "" {
		return workspaceID
	}
	return "primary"
}

func (a App) writeRemoteVerification(result remoteIngressVerification, opts options) error {
	if opts.JSON {
		raw, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Stdout, string(raw))
	} else {
		fmt.Fprintf(a.Stdout, "Remote ingress: %s (%s, mode=%s, workspace=%s, profile=%s)\n", result.Name, result.Provider, result.Mode, result.WorkspaceID, result.ToolProfile)
		printRemoteEndpointVerification(a.Stdout, "Local", result.Local)
		printRemoteEndpointVerification(a.Stdout, "Public", result.Public)
		if result.Local.OK && result.Public.OK {
			fmt.Fprintf(a.Stdout, "Contract: %s\n", ternary(result.Matches, "MATCH", "MISMATCH"))
		}
	}
	if result.OK {
		return nil
	}
	if result.Issue == "" {
		result.Issue = "remote MCP verification failed"
	}
	return errors.New(result.Issue)
}

func printRemoteEndpointVerification(writer interface{ Write([]byte) (int, error) }, label string, result remoteEndpointVerification) {
	if result.OK {
		fmt.Fprintf(writer, "%s: OK protocol=%s tools=%d hash=%s url=%s\n", label, result.ProtocolVersion, result.ToolCount, result.ContractHash, result.URL)
		return
	}
	detail := result.Error
	if detail == "" {
		detail = "not verified"
	}
	fmt.Fprintf(writer, "%s: FAIL %s url=%s\n", label, detail, result.URL)
}

func boundedRemoteVerifyError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	const limit = 1200
	if len(text) > limit {
		text = text[:limit] + "…"
	}
	return text
}

func findRemoteIngress(cfg config.Config, name string) (config.NamedRemoteIngress, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, ingress := range cfg.EffectiveRemoteIngresses() {
		if ingress.Name == name {
			return ingress, true
		}
	}
	return config.NamedRemoteIngress{}, false
}
