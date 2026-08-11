// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wormhole/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRemoteVerifyMatchesLocalAndPublicContract(t *testing.T) {
	authValue := strings.Repeat("a", 24)
	local := newRemoteVerifyMCPServer(t, authValue, "repo.read")
	defer local.Close()
	public := newRemoteVerifyMCPServer(t, authValue, "repo.read")
	defer public.Close()
	t.Setenv("REMOTE_VERIFY_AUTH", authValue)

	cfg := remoteVerifyConfig(t, local.URL, public.URL+"/mcp")
	var stdout bytes.Buffer
	app := App{Stdout: &stdout, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.remoteVerify(context.Background(), cfg, "notion", options{JSON: true}); err != nil {
		t.Fatal(err)
	}
	var result remoteIngressVerification
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || !result.Matches || !result.Local.OK || !result.Public.OK {
		t.Fatalf("unexpected verification result: %#v", result)
	}
	if result.Local.ContractHash == "" || result.Local.ContractHash != result.Public.ContractHash {
		t.Fatalf("contract hash mismatch: local=%q public=%q", result.Local.ContractHash, result.Public.ContractHash)
	}
	if result.Local.ToolCount != 1 || result.Public.ToolCount != 1 {
		t.Fatalf("unexpected tool counts: %#v", result)
	}
}

func TestRemoteVerifyDetectsPublicContractMismatch(t *testing.T) {
	authValue := strings.Repeat("b", 24)
	local := newRemoteVerifyMCPServer(t, authValue, "repo.read")
	defer local.Close()
	public := newRemoteVerifyMCPServer(t, authValue, "different.read")
	defer public.Close()
	t.Setenv("REMOTE_VERIFY_AUTH", authValue)

	cfg := remoteVerifyConfig(t, local.URL, public.URL+"/mcp")
	var stdout bytes.Buffer
	app := App{Stdout: &stdout, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	err := app.remoteVerify(context.Background(), cfg, "notion", options{JSON: true})
	if err == nil || !strings.Contains(err.Error(), "contract") {
		t.Fatalf("unexpected mismatch error: %v", err)
	}
	var result remoteIngressVerification
	if jsonErr := json.Unmarshal(stdout.Bytes(), &result); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if result.OK || result.Matches || !result.Local.OK || !result.Public.OK {
		t.Fatalf("unexpected mismatch result: %#v", result)
	}
}

func TestRemoteVerifyReportsMissingPublicURLAndBearer(t *testing.T) {
	authValue := strings.Repeat("c", 24)
	local := newRemoteVerifyMCPServer(t, authValue, "repo.read")
	defer local.Close()

	cfg := remoteVerifyConfig(t, local.URL, "")
	t.Setenv("REMOTE_VERIFY_AUTH", authValue)
	var stdout bytes.Buffer
	app := App{Stdout: &stdout, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	err := app.remoteVerify(context.Background(), cfg, "notion", options{JSON: true})
	if err == nil || !strings.Contains(err.Error(), "public") {
		t.Fatalf("unexpected missing public URL error: %v", err)
	}
	var result remoteIngressVerification
	if jsonErr := json.Unmarshal(stdout.Bytes(), &result); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if !result.Local.OK || result.Public.OK || !strings.Contains(result.Public.Error, "publicUrl") {
		t.Fatalf("unexpected missing URL result: %#v", result)
	}

	stdout.Reset()
	result = remoteIngressVerification{}
	t.Setenv("REMOTE_VERIFY_AUTH", "")
	err = app.remoteVerify(context.Background(), cfg, "notion", options{JSON: true})
	if err == nil || !strings.Contains(err.Error(), "bearer") {
		t.Fatalf("unexpected missing bearer error: %v", err)
	}
	if jsonErr := json.Unmarshal(stdout.Bytes(), &result); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if result.Local.OK || result.Public.OK || result.Issue != "MCP bearer secret is missing" {
		t.Fatalf("unexpected missing bearer result: %#v", result)
	}
}

func TestPublicRemoteProbeDoesNotFollowRedirectsWithBearer(t *testing.T) {
	authValue := strings.Repeat("d", 24)
	var sinkHits atomic.Int64
	var sinkAuth atomic.Value
	sink := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sinkHits.Add(1)
		sinkAuth.Store(request.Header.Get("Authorization"))
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer sink.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, sink.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	ctx, cancel := context.WithTimeout(context.Background(), remoteVerifyTestTimeout)
	defer cancel()
	_, err := probeRemoteMCPURL(ctx, redirect.URL+"/mcp", authValue)
	if err == nil {
		t.Fatal("redirecting public endpoint unexpectedly verified")
	}
	if sinkHits.Load() != 0 {
		t.Fatalf("redirect target was reached %d time(s)", sinkHits.Load())
	}
	if value := sinkAuth.Load(); value != nil && value.(string) != "" {
		t.Fatalf("bearer reached redirect target: %q", value)
	}
}

func TestRemoteListReportsPresenceWithoutSecretValue(t *testing.T) {
	authValue := strings.Repeat("e", 24)
	t.Setenv("REMOTE_VERIFY_AUTH", authValue)
	cfg := config.Default()
	cfg.RemoteIngresses = map[string]config.RemoteIngressConfig{
		"notion": {
			Provider: "external", LocalPort: 18133, ToolProfile: "remote-read",
			PublicURL: "https://wormhole.example/mcp", AuthTokenEnv: "REMOTE_VERIFY_AUTH",
		},
	}
	var stdout bytes.Buffer
	app := App{Stdout: &stdout, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.remoteList(cfg, options{JSON: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), authValue) {
		t.Fatal("remote list exposed bearer value")
	}
	if !strings.Contains(stdout.String(), `"authConfigured": true`) || !strings.Contains(stdout.String(), "REMOTE_VERIFY_AUTH") {
		t.Fatalf("remote list omitted secret presence/reference: %s", stdout.String())
	}
}

const remoteVerifyTestTimeout = 2 * time.Second

func newRemoteVerifyMCPServer(t *testing.T, authValue, toolName string) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "remote-verify-test", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{
		Name: toolName, Title: toolName,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true,
	})
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/mcp" {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+authValue {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(writer, request)
	}))
}

func remoteVerifyConfig(t *testing.T, localURL, publicURL string) config.Config {
	t.Helper()
	parsed, err := url.Parse(localURL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.RemoteIngresses = map[string]config.RemoteIngressConfig{
		"notion": {
			Provider: "external", LocalPort: port, WorkspaceID: "", ToolProfile: "remote-read",
			PublicURL: publicURL, AuthTokenEnv: "REMOTE_VERIFY_AUTH",
		},
	}
	return cfg
}
