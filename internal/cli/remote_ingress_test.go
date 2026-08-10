// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"wormhole/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRemoteIngressCommandKeepsSecretsOutOfArgv(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMOTE_AUTH", "mcp-bearer-secret")
	t.Setenv("REMOTE_PROVIDER", "cloudflare-tunnel-secret")
	ingress := config.NamedRemoteIngress{Name: "notion", Config: config.RemoteIngressConfig{
		Provider: "cloudflare", LocalPort: 18133, ToolProfile: "fast", Binary: executable,
		AuthTokenEnv: "REMOTE_AUTH", ProviderTokenEnv: "REMOTE_PROVIDER",
	}}
	cmd, err := remoteIngressCommand(ingress)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(cmd.Args, " ")
	for _, secret := range []string{"mcp-bearer-secret", "cloudflare-tunnel-secret"} {
		if strings.Contains(argv, secret) {
			t.Fatalf("remote ingress command argv exposed secret %q: %s", secret, argv)
		}
	}
	if !strings.Contains(argv, "tunnel --no-autoupdate run") {
		t.Fatalf("unexpected cloudflared command: %s", argv)
	}
	providerInEnv := false
	for _, value := range cmd.Env {
		if value == "TUNNEL_TOKEN=cloudflare-tunnel-secret" {
			providerInEnv = true
		}
	}
	if !providerInEnv {
		t.Fatal("cloudflare tunnel token was not injected through TUNNEL_TOKEN")
	}
}

func TestExternalRemoteIngressHasNoManagedProcess(t *testing.T) {
	ingress := config.NamedRemoteIngress{Name: "notion", Config: config.RemoteIngressConfig{
		Provider: "external", LocalPort: 18133, ToolProfile: "remote-read", AuthTokenEnv: "REMOTE_AUTH",
	}}
	cfg := config.Default()
	cfg.RemoteIngresses = map[string]config.RemoteIngressConfig{"notion": ingress.Config}
	if desired := desiredRemoteIngressMap(cfg); len(desired) != 0 {
		t.Fatalf("external ingress created managed process desire: %#v", desired)
	}
	if _, err := remoteIngressCommand(ingress); err == nil || !strings.Contains(err.Error(), "no managed child process") {
		t.Fatalf("unexpected external provider command result: %v", err)
	}
}

func TestRemoteIngressChildUsesDedicatedLogAndValidLabel(t *testing.T) {
	t.Setenv("WORMHOLE_DATA_DIR", t.TempDir())
	label := remoteIngressLabel("notion")
	if !validChildLabel(label) {
		t.Fatalf("remote ingress child label rejected: %s", label)
	}
	if validChildLabel("remote-ingress-../bad") {
		t.Fatal("unsafe remote ingress child label accepted")
	}
	if got, want := childLogPath(label), config.RemoteIngressLogPathFor("notion"); got != want {
		t.Fatalf("remote ingress log path = %s, want %s", got, want)
	}
}

func TestProbeRemoteIngressAuthenticatesAndNegotiatesCurrentMCP(t *testing.T) {
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "probe-test", Version: "1"}, nil)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true,
	})
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+os.Getenv("REMOTE_PROBE_AUTH") {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(writer, request)
	}))
	defer httpServer.Close()
	_, rawPort, err := net.SplitHostPort(strings.TrimPrefix(httpServer.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMOTE_PROBE_AUTH", "bearer-test-value")
	ingress := config.NamedRemoteIngress{Name: "probe", Config: config.RemoteIngressConfig{
		Provider: "external", LocalPort: port, ToolProfile: "remote-read", AuthTokenEnv: "REMOTE_PROBE_AUTH",
	}}
	result, err := probeRemoteIngress(context.Background(), ingress)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProtocolVersion != "2026-07-28" || result.ToolCount != 0 {
		t.Fatalf("unexpected probe result: %#v", result)
	}
}

func TestRemoteIngressCommandRequiresBothSecrets(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ingress := config.NamedRemoteIngress{Name: "notion", Config: config.RemoteIngressConfig{
		Provider: "cloudflare", LocalPort: 18133, ToolProfile: "fast", Binary: executable,
		AuthTokenEnv: "REMOTE_AUTH_MISSING", ProviderTokenEnv: "REMOTE_PROVIDER_MISSING",
	}}
	if _, err := remoteIngressCommand(ingress); err == nil || !strings.Contains(err.Error(), "provider token") {
		t.Fatalf("unexpected missing provider error: %v", err)
	}
	t.Setenv("REMOTE_PROVIDER_MISSING", "provider")
	if _, err := remoteIngressCommand(ingress); err == nil || !strings.Contains(err.Error(), "MCP bearer") {
		t.Fatalf("unexpected missing bearer error: %v", err)
	}
}
