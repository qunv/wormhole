// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"wormhole/internal/adminauth"
	"wormhole/internal/agent"
	"wormhole/internal/config"
	"wormhole/internal/workspaceregistry"
)

func TestAdminRequiresConfiguredLoginAndSupportsLogout(t *testing.T) {
	handler := newAdminHandler(t, nil)
	if _, err := adminauth.SetCredentials(config.AdminAuthPath(), "admin", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	handler.auth = adminauth.NewManager(config.AdminAuthPath())

	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, localRequest(http.MethodGet, apiPrefix+"/auth/status", nil))
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"configured":true`) || !strings.Contains(statusResponse.Body.String(), `"authenticated":false`) {
		t.Fatalf("auth status = %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	csrf := csrfCookie(t, statusResponse.Result())

	protectedResponse := httptest.NewRecorder()
	handler.ServeHTTP(protectedResponse, localRequest(http.MethodGet, apiPrefix+"/config", nil))
	if protectedResponse.Code != http.StatusUnauthorized || !strings.Contains(protectedResponse.Body.String(), "authentication_required") {
		t.Fatalf("unauthenticated config = %d %s", protectedResponse.Code, protectedResponse.Body.String())
	}

	loginRequest := localRequest(http.MethodPost, apiPrefix+"/auth/login", strings.NewReader(`{"username":"admin","password":"correct horse battery staple"}`))
	loginRequest.Header.Set("Origin", "http://127.0.0.1:8789")
	loginRequest.Header.Set("X-Wormhole-CSRF", csrf.Value)
	loginRequest.AddCookie(csrf)
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK || !strings.Contains(loginResponse.Body.String(), `"authenticated":true`) {
		t.Fatalf("login = %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	sessionCookie := cookieByName(t, loginResponse.Result(), sessionCookieName)
	if !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode || sessionCookie.Value == "" {
		t.Fatalf("unexpected session cookie: %#v", sessionCookie)
	}

	protectedRequest := localRequest(http.MethodGet, apiPrefix+"/config", nil)
	protectedRequest.AddCookie(sessionCookie)
	protectedResponse = httptest.NewRecorder()
	handler.ServeHTTP(protectedResponse, protectedRequest)
	if protectedResponse.Code != http.StatusOK {
		t.Fatalf("authenticated config = %d %s", protectedResponse.Code, protectedResponse.Body.String())
	}

	logoutRequest := localRequest(http.MethodPost, apiPrefix+"/auth/logout", strings.NewReader(`{}`))
	logoutRequest.Header.Set("Origin", "http://127.0.0.1:8789")
	logoutRequest.Header.Set("X-Wormhole-CSRF", csrf.Value)
	logoutRequest.AddCookie(csrf)
	logoutRequest.AddCookie(sessionCookie)
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusOK {
		t.Fatalf("logout = %d %s", logoutResponse.Code, logoutResponse.Body.String())
	}

	protectedRequest = localRequest(http.MethodGet, apiPrefix+"/config", nil)
	protectedRequest.AddCookie(sessionCookie)
	protectedResponse = httptest.NewRecorder()
	handler.ServeHTTP(protectedResponse, protectedRequest)
	if protectedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session status = %d body=%s", protectedResponse.Code, protectedResponse.Body.String())
	}
}

func TestAdminMissingCredentialsCanBeInitializedOnceFromLoopbackUI(t *testing.T) {
	handler := newAdminHandler(t, nil)
	handler.auth = adminauth.NewManager(config.AdminAuthPath())

	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, localRequest(http.MethodGet, apiPrefix+"/auth/status", nil))
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"configured":false`) {
		t.Fatalf("missing auth status = %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	csrf := csrfCookie(t, statusResponse.Result())

	setupRequest := localRequest(http.MethodPost, apiPrefix+"/auth/setup", strings.NewReader(`{"username":"admin","password":"correct horse battery staple"}`))
	setupRequest.Header.Set("Origin", "http://127.0.0.1:8789")
	setupRequest.Header.Set("X-Wormhole-CSRF", csrf.Value)
	setupRequest.AddCookie(csrf)
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setupRequest)
	if setupResponse.Code != http.StatusCreated || !strings.Contains(setupResponse.Body.String(), `"authenticated":true`) {
		t.Fatalf("admin setup = %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	sessionCookie := cookieByName(t, setupResponse.Result(), sessionCookieName)
	if !sessionCookie.HttpOnly || sessionCookie.Value == "" {
		t.Fatalf("unexpected setup session cookie: %#v", sessionCookie)
	}
	credential, err := adminauth.LoadCredentials(config.AdminAuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if !adminauth.VerifyPassword(credential, "admin", "correct horse battery staple") {
		t.Fatal("created browser credential did not verify")
	}

	protectedRequest := localRequest(http.MethodGet, apiPrefix+"/config", nil)
	protectedRequest.AddCookie(sessionCookie)
	protectedResponse := httptest.NewRecorder()
	handler.ServeHTTP(protectedResponse, protectedRequest)
	if protectedResponse.Code != http.StatusOK {
		t.Fatalf("setup session config = %d %s", protectedResponse.Code, protectedResponse.Body.String())
	}

	secondRequest := localRequest(http.MethodPost, apiPrefix+"/auth/setup", strings.NewReader(`{"username":"other","password":"replacement password"}`))
	secondRequest.Header.Set("Origin", "http://127.0.0.1:8789")
	secondRequest.Header.Set("X-Wormhole-CSRF", csrf.Value)
	secondRequest.AddCookie(csrf)
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, secondRequest)
	if secondResponse.Code != http.StatusConflict || !strings.Contains(secondResponse.Body.String(), "admin_already_configured") {
		t.Fatalf("second admin setup = %d %s", secondResponse.Code, secondResponse.Body.String())
	}
	credential, err = adminauth.LoadCredentials(config.AdminAuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if !adminauth.VerifyPassword(credential, "admin", "correct horse battery staple") || adminauth.VerifyPassword(credential, "other", "replacement password") {
		t.Fatal("second browser setup replaced the initial account")
	}
}

func TestAdminWorkspaceBrowserIsDirectoryOnlyAndHomeConfined(t *testing.T) {
	home := t.TempDir()
	setAdminUserHome(t, home)
	visible := filepath.Join(home, "visible")
	hidden := filepath.Join(home, ".hidden")
	if err := os.MkdirAll(visible, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "not-a-directory.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := newAdminHandler(t, nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, apiPrefix+"/workspaces/browse", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("browse home = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"name":"visible"`) || strings.Contains(body, ".hidden") || strings.Contains(body, "not-a-directory.txt") {
		t.Fatalf("unexpected directory browser response: %s", body)
	}

	outside := filepath.Dir(home)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, apiPrefix+"/workspaces/browse?path="+url.QueryEscape(outside), nil))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "browse_outside_home") {
		t.Fatalf("browse outside home = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminCanRegisterAndRemoveWorkspaceWithRevisionSafety(t *testing.T) {
	handler := newAdminHandler(t, nil)
	workspace := t.TempDir()

	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, localRequest(http.MethodGet, apiPrefix+"/workspaces", nil))
	if listResponse.Code != http.StatusOK {
		t.Fatalf("workspace list = %d %s", listResponse.Code, listResponse.Body.String())
	}
	cookie := csrfCookie(t, listResponse.Result())
	var list struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}

	createBody, _ := json.Marshal(map[string]string{"id": "api", "workspace": workspace})
	createRequest := localRequest(http.MethodPost, apiPrefix+"/workspaces", strings.NewReader(string(createBody)))
	secureAdminWrite(createRequest, cookie, list.Revision)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("workspace create = %d %s", createResponse.Code, createResponse.Body.String())
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry, exists := registry.Workspaces["api"]
	if !exists || entry.Workspace != workspace || !entry.Enabled {
		t.Fatalf("workspace was not registered: %#v", registry.Workspaces)
	}
	if _, err := os.Stat(entry.ConfigPath); err != nil {
		t.Fatalf("workspace override was not created: %v", err)
	}

	listResponse = httptest.NewRecorder()
	handler.ServeHTTP(listResponse, localRequest(http.MethodGet, apiPrefix+"/workspaces", nil))
	if err := json.Unmarshal(listResponse.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	removeRequest := localRequest(http.MethodDelete, apiPrefix+"/workspaces/api", nil)
	secureAdminWrite(removeRequest, csrfCookie(t, listResponse.Result()), list.Revision)
	removeResponse := httptest.NewRecorder()
	handler.ServeHTTP(removeResponse, removeRequest)
	if removeResponse.Code != http.StatusOK {
		t.Fatalf("workspace remove = %d %s", removeResponse.Code, removeResponse.Body.String())
	}
	registry, err = workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := registry.Workspaces["api"]; exists {
		t.Fatalf("workspace remains registered: %#v", registry.Workspaces)
	}
	if _, err := os.Stat(entry.ConfigPath); err != nil {
		t.Fatalf("workspace override should be preserved by default: %v", err)
	}
	if !strings.Contains(removeResponse.Body.String(), `"statePreserved":true`) {
		t.Fatalf("remove response did not document preserved state: %s", removeResponse.Body.String())
	}

	listResponse = httptest.NewRecorder()
	handler.ServeHTTP(listResponse, localRequest(http.MethodGet, apiPrefix+"/workspaces", nil))
	if err := json.Unmarshal(listResponse.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	createRequest = localRequest(http.MethodPost, apiPrefix+"/workspaces", strings.NewReader(string(createBody)))
	secureAdminWrite(createRequest, csrfCookie(t, listResponse.Result()), list.Revision)
	createResponse = httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("workspace re-register = %d %s", createResponse.Code, createResponse.Body.String())
	}

	listResponse = httptest.NewRecorder()
	handler.ServeHTTP(listResponse, localRequest(http.MethodGet, apiPrefix+"/workspaces", nil))
	if err := json.Unmarshal(listResponse.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	removeRequest = localRequest(http.MethodDelete, apiPrefix+"/workspaces/api?deleteConfig=true", nil)
	secureAdminWrite(removeRequest, csrfCookie(t, listResponse.Result()), list.Revision)
	removeResponse = httptest.NewRecorder()
	handler.ServeHTTP(removeResponse, removeRequest)
	if removeResponse.Code != http.StatusOK {
		t.Fatalf("workspace remove with config = %d %s", removeResponse.Code, removeResponse.Body.String())
	}
	if _, err := os.Stat(entry.ConfigPath); !os.IsNotExist(err) {
		t.Fatalf("workspace override was not deleted: %v", err)
	}
}

func TestAdminWorkspaceCreateRejectsPrimaryRoot(t *testing.T) {
	handler := newAdminHandler(t, nil)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, localRequest(http.MethodGet, apiPrefix+"/workspaces", nil))
	cookie := csrfCookie(t, listResponse.Result())
	var list struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"id": "duplicate", "workspace": handler.Runtime.Workspace.Primary})
	request := localRequest(http.MethodPost, apiPrefix+"/workspaces", strings.NewReader(string(body)))
	secureAdminWrite(request, cookie, list.Revision)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "workspace_is_primary") {
		t.Fatalf("primary workspace registration = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminRejectsRemoteClientsAndUntrustedHosts(t *testing.T) {
	handler := newAdminHandler(t, nil)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8789/admin/", nil)
	request.RemoteAddr = "192.0.2.10:31337"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "loopback_only") {
		t.Fatalf("remote request = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "http://evil.example/admin/", nil)
	request.RemoteAddr = "127.0.0.1:31337"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "invalid_host") {
		t.Fatalf("untrusted host request = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminAssetsSetSecurityHeadersAndCSRFCookie(t *testing.T) {
	handler := newAdminHandler(t, nil)
	request := localRequest(http.MethodGet, "/admin/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("asset status = %d body=%s", response.Code, response.Body.String())
	}
	for header, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := response.Header().Get(header); !strings.Contains(got, want) {
			t.Fatalf("%s = %q, want substring %q", header, got, want)
		}
	}
	cookie := csrfCookie(t, response.Result())
	if !validCSRFToken(cookie.Value) || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected CSRF cookie: %#v", cookie)
	}
	if !strings.Contains(response.Body.String(), `<div id="root"></div>`) {
		t.Fatalf("admin asset did not contain application root: %s", response.Body.String())
	}
}

func TestAdminConfigFallsBackToActiveRuntimeWhenFileIsMissing(t *testing.T) {
	t.Setenv("WORMHOLE_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.NoTunnel = true
	cfg.Policy = "strict"
	runtime, err := agent.NewWorkspaceContextWithReporter(
		context.Background(), "default", config.AppDataDir(), cfg, "test", "pro", "config-id", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	if _, err := os.Stat(config.ConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("expected no persisted config, got %v", err)
	}

	response := httptest.NewRecorder()
	handler := New(runtime, nil)
	handler.auth = nil
	handler.ServeHTTP(response, localRequest(http.MethodGet, apiPrefix+"/config", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("fallback config status = %d body=%s", response.Code, response.Body.String())
	}
	var snapshot configSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Config.Workspace != cfg.Workspace || snapshot.Config.Policy != "strict" {
		t.Fatalf("fallback config did not use active runtime: %#v", snapshot.Config)
	}
}

func TestAdminConfigWritesRequireSameOriginCSRFAndRevision(t *testing.T) {
	handler := newAdminHandler(t, func(cfg *config.Config) {
		cfg.Host = "0.0.0.0"
		cfg.AuthToken = "runtime-only-bearer"
		cfg.ApprovalToken = "runtime-only-approval"
	})

	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, localRequest(http.MethodGet, apiPrefix+"/config", nil))
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get config status = %d body=%s", getResponse.Code, getResponse.Body.String())
	}
	cookie := csrfCookie(t, getResponse.Result())
	if strings.Contains(getResponse.Body.String(), "runtime-only") {
		t.Fatal("config API exposed a runtime-only token")
	}
	var snapshot configSnapshot
	if err := json.Unmarshal(getResponse.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Config.Policy = "strict"
	body, _ := json.Marshal(snapshot.Config)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodPut, apiPrefix+"/config", strings.NewReader(string(body))))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "origin_rejected") {
		t.Fatalf("write without origin = %d %s", response.Code, response.Body.String())
	}

	request := localRequest(http.MethodPut, apiPrefix+"/config", strings.NewReader(string(body)))
	request.Header.Set("Origin", "http://127.0.0.1:8789")
	request.AddCookie(cookie)
	request.Header.Set("X-Wormhole-CSRF", cookie.Value)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionFailed || !strings.Contains(response.Body.String(), "revision_conflict") {
		t.Fatalf("write without revision = %d %s", response.Code, response.Body.String())
	}

	request = localRequest(http.MethodPut, apiPrefix+"/config", strings.NewReader(string(body)))
	request.Header.Set("Origin", "http://127.0.0.1:8789")
	request.Header.Set("If-Match", quoteETag(snapshot.Revision))
	request.Header.Set("X-Wormhole-CSRF", cookie.Value)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid config write = %d %s", response.Code, response.Body.String())
	}
	persisted, err := config.LoadFileForEditing(
		config.ConfigPath(), "runtime-only-bearer", "runtime-only-approval",
	)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Policy != "strict" || persisted.AuthToken != "" || persisted.ApprovalToken != "" {
		t.Fatalf("unexpected persisted config: policy=%q auth=%q approval=%q", persisted.Policy, persisted.AuthToken, persisted.ApprovalToken)
	}

	request = localRequest(http.MethodPut, apiPrefix+"/config", strings.NewReader(string(body)))
	request.Header.Set("Origin", "http://127.0.0.1:8789")
	request.Header.Set("If-Match", quoteETag(snapshot.Revision))
	request.Header.Set("X-Wormhole-CSRF", cookie.Value)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale revision status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestAdminWorkspaceOverridesRejectOwnedFieldsAndUseRevisions(t *testing.T) {
	handler := newAdminHandler(t, nil)
	base, err := config.LoadFile(config.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	id := "api"
	entry := workspaceregistry.Registration{
		ID: id, Workspace: t.TempDir(), ConfigPath: workspaceregistry.ConfigPath(id),
		DataDir: workspaceregistry.DataDir(id), Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := config.SaveOverrideFile(entry.ConfigPath, base, map[string]any{"extraRoots": []any{}}); err != nil {
		t.Fatal(err)
	}
	registry := workspaceregistry.Empty()
	registry.Workspaces[id] = entry
	if err := workspaceregistry.Save(registry); err != nil {
		t.Fatal(err)
	}

	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, localRequest(http.MethodGet, apiPrefix+"/workspaces/"+id, nil))
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get workspace = %d %s", getResponse.Code, getResponse.Body.String())
	}
	cookie := csrfCookie(t, getResponse.Result())
	var payload struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(getResponse.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}

	request := localRequest(http.MethodPut, apiPrefix+"/workspaces/"+id, strings.NewReader(`{"workspace":"/tmp/escape"}`))
	request.Header.Set("Origin", "http://127.0.0.1:8789")
	request.Header.Set("X-Wormhole-CSRF", cookie.Value)
	request.Header.Set("If-Match", quoteETag(payload.Revision))
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "workspace_owned_field") {
		t.Fatalf("owned field write = %d %s", response.Code, response.Body.String())
	}

	request = localRequest(http.MethodPut, apiPrefix+"/workspaces/"+id, strings.NewReader(`{"policy":"strict","extraRoots":[]}`))
	request.Header.Set("Origin", "http://127.0.0.1:8789")
	request.Header.Set("X-Wormhole-CSRF", cookie.Value)
	request.Header.Set("If-Match", quoteETag(payload.Revision))
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid workspace write = %d %s", response.Code, response.Body.String())
	}
	override, err := config.ReadOverrideFile(entry.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if override["policy"] != "strict" {
		t.Fatalf("workspace policy was not persisted: %#v", override)
	}
}

func TestAdminSecretsAreWriteOnlyAndReferenceScoped(t *testing.T) {
	const secretName = "WORMHOLE_TEST_MEMORY_SECRET"
	const secretValue = "super-secret-value"
	const externalValue = "external-secret-value"
	t.Setenv(secretName, externalValue)
	handler := newAdminHandler(t, func(cfg *config.Config) {
		cfg.Memory.SecretEnv = secretName
	})

	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, localRequest(http.MethodGet, apiPrefix+"/secrets", nil))
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), secretName) {
		t.Fatalf("get secrets = %d %s", getResponse.Code, getResponse.Body.String())
	}
	if !strings.Contains(getResponse.Body.String(), `"source":"environment"`) || strings.Contains(getResponse.Body.String(), externalValue) {
		t.Fatalf("external secret state was incorrect or leaked: %s", getResponse.Body.String())
	}
	cookie := csrfCookie(t, getResponse.Result())

	request := localRequest(http.MethodPut, apiPrefix+"/secrets/NOT_REFERENCED", strings.NewReader(`{"value":"x"}`))
	request.Header.Set("Origin", "http://127.0.0.1:8789")
	request.Header.Set("X-Wormhole-CSRF", cookie.Value)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unreferenced secret status = %d body=%s", response.Code, response.Body.String())
	}

	request = localRequest(http.MethodPut, apiPrefix+"/secrets/"+secretName, strings.NewReader(`{"value":"`+secretValue+`"}`))
	request.Header.Set("Origin", "http://127.0.0.1:8789")
	request.Header.Set("X-Wormhole-CSRF", cookie.Value)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), secretValue) {
		t.Fatalf("secret write = %d %s", response.Code, response.Body.String())
	}
	raw, err := os.ReadFile(config.DotEnvPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), secretValue) {
		t.Fatalf("secret was not stored in dotenv: %s", raw)
	}

	getResponse = httptest.NewRecorder()
	handler.ServeHTTP(getResponse, localRequest(http.MethodGet, apiPrefix+"/secrets", nil))
	if strings.Contains(getResponse.Body.String(), secretValue) || !strings.Contains(getResponse.Body.String(), `"configured":true`) || !strings.Contains(getResponse.Body.String(), `"source":"dotenv"`) {
		t.Fatalf("secret presence response leaked or omitted state: %s", getResponse.Body.String())
	}

	request = localRequest(http.MethodDelete, apiPrefix+"/secrets/"+secretName, nil)
	request.Header.Set("Origin", "http://127.0.0.1:8789")
	request.Header.Set("X-Wormhole-CSRF", cookie.Value)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("secret delete = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(config.DotEnvPath()); !os.IsNotExist(err) {
		t.Fatalf("dotenv still exists after deleting its only value: %v", err)
	}
}

func TestAdminWorkspaceOverrideProvenanceAndCompactionPreview(t *testing.T) {
	handler := newAdminHandler(t, func(cfg *config.Config) { cfg.Audit = true })
	workspace := t.TempDir()
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, localRequest(http.MethodGet, apiPrefix+"/workspaces", nil))
	var list struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	createBody, _ := json.Marshal(map[string]string{"id": "api", "workspace": workspace})
	createRequest := localRequest(http.MethodPost, apiPrefix+"/workspaces", strings.NewReader(string(createBody)))
	secureAdminWrite(createRequest, csrfCookie(t, listResponse.Result()), list.Revision)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("workspace create = %d %s", createResponse.Code, createResponse.Body.String())
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Workspaces["api"]
	raw := []byte("{\n  \"schemaVersion\": 1,\n  \"audit\": true,\n  \"maxProcesses\": 7\n}\n")
	if err := os.WriteFile(entry.ConfigPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, apiPrefix+"/workspaces/api", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("workspace config = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{`"path":"maxProcesses"`, `"redundantPaths":["audit"]`, `"compactedOverride":{"maxProcesses":7`, `"inheritedTopLevel"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("workspace provenance missing %s: %s", want, body)
		}
	}
}

func TestAdminUpstreamStatusAndInvalidRefresh(t *testing.T) {
	handler := newAdminHandler(t, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, apiPrefix+"/upstream", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"servers":[]`) {
		t.Fatalf("upstream status = %d %s", response.Code, response.Body.String())
	}
	csrf := csrfCookie(t, response.Result())
	request := localRequest(http.MethodPost, apiPrefix+"/upstream/missing/server/refresh", strings.NewReader(`{}`))
	request.Header.Set("Origin", "http://127.0.0.1:8789")
	request.Header.Set("X-Wormhole-CSRF", csrf.Value)
	request.AddCookie(csrf)
	refresh := httptest.NewRecorder()
	handler.ServeHTTP(refresh, request)
	if refresh.Code != http.StatusNotFound || !strings.Contains(refresh.Body.String(), "workspace_not_found") {
		t.Fatalf("invalid upstream refresh = %d %s", refresh.Code, refresh.Body.String())
	}
}

func TestAdminDiagnosticsBundleIsDownloadableAndRedacted(t *testing.T) {
	runtimeSecret := "diagnostic-runtime-" + t.Name()
	authToken := "diagnostic-auth-" + t.Name()
	t.Setenv("CONTROL_PLANE_API_KEY", runtimeSecret)
	handler := newAdminHandler(t, func(cfg *config.Config) {
		cfg.AuthToken = authToken
		cfg.RuntimeKeyEnv = "CONTROL_PLANE_API_KEY"
		cfg.Audit = true
	})
	if err := os.MkdirAll(filepath.Dir(config.ServerLogPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ServerLogPath(), []byte("runtime-values "+authToken+" "+runtimeSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Runtime.Handle(context.Background(), "workspace_info", nil); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, apiPrefix+"/diagnostics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("diagnostics = %d %s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("diagnostics content type = %q", contentType)
	}
	if disposition := response.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, "wormhole-diagnostics-") {
		t.Fatalf("diagnostics disposition = %q", disposition)
	}
	body := response.Body.String()
	for _, secret := range []string{runtimeSecret, authToken} {
		if strings.Contains(body, secret) {
			t.Fatalf("diagnostics leaked %q: %s", secret, body)
		}
	}
	for _, want := range []string{`"diagnosticVersion": 1`, `"sessionRouter"`, `"sharedResources"`, `"logs"`, `"secrets"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("diagnostics missing %s: %s", want, body)
		}
	}
}

func TestAdminRestartSchedulesDetachedLifecycleHelperOnce(t *testing.T) {
	handler := newAdminHandler(t, nil)
	calls := 0
	oldPort := 0
	handler.scheduleRestart = func(port int) error {
		calls++
		oldPort = port
		return nil
	}

	bootstrap := httptest.NewRecorder()
	handler.ServeHTTP(bootstrap, localRequest(http.MethodGet, apiPrefix+"/bootstrap", nil))
	csrf := csrfCookie(t, bootstrap.Result())
	requestRestart := func() *httptest.ResponseRecorder {
		request := localRequest(http.MethodPost, apiPrefix+"/lifecycle/restart", strings.NewReader(`{}`))
		request.Header.Set("Origin", "http://127.0.0.1:8789")
		request.Header.Set("X-Wormhole-CSRF", csrf.Value)
		request.AddCookie(csrf)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	first := requestRestart()
	if first.Code != http.StatusAccepted || !strings.Contains(first.Body.String(), `"accepted":true`) || strings.Contains(first.Body.String(), `"alreadyPending":true`) {
		t.Fatalf("first restart = %d %s", first.Code, first.Body.String())
	}
	second := requestRestart()
	if second.Code != http.StatusAccepted || !strings.Contains(second.Body.String(), `"alreadyPending":true`) {
		t.Fatalf("duplicate restart = %d %s", second.Code, second.Body.String())
	}
	if calls != 1 || oldPort != handler.Runtime.Config.Port {
		t.Fatalf("restart scheduler calls=%d oldPort=%d", calls, oldPort)
	}
}

func TestAdminOperationsApprovalsAndAuditExplorer(t *testing.T) {
	handler := newAdminHandler(t, func(cfg *config.Config) {
		cfg.Audit = true
		cfg.AuditArgs = true
	})

	if _, err := handler.Runtime.Handle(context.Background(), "workspace_info", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := handler.Runtime.FlushAudit(context.Background()); err != nil {
		t.Fatal(err)
	}
	approval, err := handler.Runtime.Approvals.Request([]string{"delete_path:important.txt"}, "Admin approval test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	operationsResponse := httptest.NewRecorder()
	handler.ServeHTTP(operationsResponse, localRequest(http.MethodGet, apiPrefix+"/operations", nil))
	if operationsResponse.Code != http.StatusOK {
		t.Fatalf("operations = %d %s", operationsResponse.Code, operationsResponse.Body.String())
	}
	operationsBody := operationsResponse.Body.String()
	for _, want := range []string{`"sessionRouter"`, `"sharedResources"`, `"workspaces"`, `"metrics"`, `"modules"`, `"startupWarnings":[]`} {
		if !strings.Contains(operationsBody, want) {
			t.Fatalf("operations response missing %s: %s", want, operationsBody)
		}
	}
	if strings.Contains(operationsBody, `"startupWarnings":null`) {
		t.Fatalf("operations response returned nullable startupWarnings: %s", operationsBody)
	}

	approvalsResponse := httptest.NewRecorder()
	handler.ServeHTTP(approvalsResponse, localRequest(http.MethodGet, apiPrefix+"/approvals?status=pending", nil))
	if approvalsResponse.Code != http.StatusOK || !strings.Contains(approvalsResponse.Body.String(), approval.ID) || !strings.Contains(approvalsResponse.Body.String(), `"workspaceId":"default"`) {
		t.Fatalf("pending approvals = %d %s", approvalsResponse.Code, approvalsResponse.Body.String())
	}
	csrf := csrfCookie(t, approvalsResponse.Result())
	decisionRequest := localRequest(http.MethodPost, apiPrefix+"/approvals/default/"+approval.ID, strings.NewReader(`{"decision":"approved"}`))
	decisionRequest.Header.Set("Origin", "http://127.0.0.1:8789")
	decisionRequest.Header.Set("X-Wormhole-CSRF", csrf.Value)
	decisionRequest.AddCookie(csrf)
	decisionResponse := httptest.NewRecorder()
	handler.ServeHTTP(decisionResponse, decisionRequest)
	if decisionResponse.Code != http.StatusOK || !strings.Contains(decisionResponse.Body.String(), `"status":"approved"`) {
		t.Fatalf("approval decision = %d %s", decisionResponse.Code, decisionResponse.Body.String())
	}
	if err := handler.Runtime.Approvals.Consume("delete_path:important.txt"); err != nil {
		t.Fatalf("Admin-approved action was not consumable: %v", err)
	}

	auditResponse := httptest.NewRecorder()
	handler.ServeHTTP(auditResponse, localRequest(http.MethodGet, apiPrefix+"/audit?workspace=default&tool=workspace_info&status=succeeded&limit=10", nil))
	if auditResponse.Code != http.StatusOK || !strings.Contains(auditResponse.Body.String(), `"tool":"workspace_info"`) || !strings.Contains(auditResponse.Body.String(), `"workspaceId":"default"`) {
		t.Fatalf("audit explorer = %d %s", auditResponse.Code, auditResponse.Body.String())
	}
	if strings.Contains(auditResponse.Body.String(), "workspace_binding") {
		t.Fatalf("audit explorer exposed a workspace binding: %s", auditResponse.Body.String())
	}
}

func TestAdminToolCatalogExposesSelectableGroupsAndUnfilteredTools(t *testing.T) {
	handler := newAdminHandler(t, func(cfg *config.Config) {
		cfg.Tools.AllowedGroups = []string{"repo"}
		cfg.Tools.DeniedTools = []string{"read_file"}
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, apiPrefix+"/tools/catalog", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("get tool catalog = %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		WorkspaceCount int `json:"workspaceCount"`
		Groups         []struct {
			Name      string `json:"name"`
			ToolCount int    `json:"toolCount"`
		} `json:"groups"`
		Tools []struct {
			Name         string   `json:"name"`
			Groups       []string `json:"groups"`
			WorkspaceIDs []string `json:"workspaceIds"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WorkspaceCount != 1 || len(payload.Groups) == 0 || len(payload.Tools) == 0 {
		t.Fatalf("unexpected tool catalog payload: %#v", payload)
	}
	groups := map[string]int{}
	for _, group := range payload.Groups {
		groups[group.Name] = group.ToolCount
	}
	if groups["filesystem"] == 0 || groups["repo"] == 0 {
		t.Fatalf("expected filesystem and repo groups: %#v", groups)
	}
	var readFileGroups []string
	var readFileWorkspaces []string
	for _, tool := range payload.Tools {
		if tool.Name == "read_file" {
			readFileGroups = tool.Groups
			readFileWorkspaces = tool.WorkspaceIDs
			break
		}
	}
	if len(readFileGroups) != 1 || readFileGroups[0] != "filesystem" {
		t.Fatalf("denied tool missing from unfiltered catalog: groups=%v", readFileGroups)
	}
	if len(readFileWorkspaces) != 1 || readFileWorkspaces[0] != "default" {
		t.Fatalf("unexpected read_file workspaces: %v", readFileWorkspaces)
	}
}

func TestAdminProfilesExposeEffectiveFastAndFullToolContracts(t *testing.T) {
	handler := newAdminHandler(t, func(cfg *config.Config) {
		cfg.NoTunnel = false
		cfg.ToolProfiles = map[string]config.ToolProfileConfig{
			"review": {Name: "Review", AllowedTools: []string{"read_file"}, OutputMode: "structured"},
		}
		cfg.Tunnels = map[string]config.TunnelConfig{
			"fast":   {TunnelID: "tunnel_fast", Mode: "fast", Profile: "wormhole-fast", RuntimeKeyEnv: "FAST_KEY"},
			"full":   {TunnelID: "tunnel_full", Mode: "full", Profile: "wormhole-full", RuntimeKeyEnv: "FULL_KEY"},
			"review": {TunnelID: "tunnel_review", Mode: "full", ToolProfile: "review", Profile: "wormhole-review", RuntimeKeyEnv: "REVIEW_KEY"},
		}
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, apiPrefix+"/profiles", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("get profiles = %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		WorkspaceCount int `json:"workspaceCount"`
		Profiles       []struct {
			ID           string `json:"id"`
			Endpoint     string `json:"endpoint"`
			BuiltIn      bool   `json:"builtIn"`
			OutputMode   string `json:"outputMode"`
			ContractHash string `json:"contractHash"`
			Tools        []struct {
				Name         string   `json:"name"`
				Scope        string   `json:"scope"`
				WorkspaceIDs []string `json:"workspaceIds"`
			} `json:"tools"`
			Tunnels []struct {
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
			} `json:"tunnels"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WorkspaceCount != 1 || len(payload.Profiles) != 3 {
		t.Fatalf("unexpected profile payload: %#v", payload)
	}
	profiles := map[string]struct {
		Endpoint       string
		BuiltIn        bool
		OutputMode     string
		ContractHash   string
		Tools          map[string]string
		ToolWorkspaces map[string][]string
		Tunnels        map[string]bool
	}{}
	for _, profile := range payload.Profiles {
		item := struct {
			Endpoint       string
			BuiltIn        bool
			OutputMode     string
			ContractHash   string
			Tools          map[string]string
			ToolWorkspaces map[string][]string
			Tunnels        map[string]bool
		}{profile.Endpoint, profile.BuiltIn, profile.OutputMode, profile.ContractHash, map[string]string{}, map[string][]string{}, map[string]bool{}}
		for _, tool := range profile.Tools {
			item.Tools[tool.Name] = tool.Scope
			item.ToolWorkspaces[tool.Name] = tool.WorkspaceIDs
		}
		for _, tunnel := range profile.Tunnels {
			item.Tunnels[tunnel.Name] = tunnel.Enabled
		}
		profiles[profile.ID] = item
	}
	fast := profiles["fast"]
	if fast.Endpoint != "/mcp/session/fast" || len(fast.Tools) != 15 || fast.Tools["workspace_select"] != "session" || fast.Tools["read_file"] != "workspace" {
		t.Fatalf("unexpected fast profile: %#v", fast)
	}
	if len(fast.ToolWorkspaces["read_file"]) != 1 || fast.ToolWorkspaces["read_file"][0] != "default" || !fast.Tunnels["fast"] {
		t.Fatalf("unexpected fast availability/tunnels: %#v", fast)
	}
	if _, exposed := fast.Tools["memory_search"]; exposed {
		t.Fatal("fast admin profile unexpectedly exposed memory_search")
	}
	full := profiles["full"]
	if full.Endpoint != "/mcp/session" || len(full.Tools) <= len(fast.Tools) || !full.Tunnels["full"] || !full.BuiltIn {
		t.Fatalf("unexpected full profile: %#v", full)
	}
	review := profiles["review"]
	if review.Endpoint != "/mcp/session/profiles/review" || review.BuiltIn || review.OutputMode != "structured" || len(review.Tools) != 5 || review.Tools["read_file"] != "workspace" || !review.Tunnels["review"] || !strings.HasPrefix(review.ContractHash, "sha256:") {
		t.Fatalf("unexpected custom review profile: %#v", review)
	}
}

func TestAdminSecretsIncludeNamedTunnelRuntimeKeys(t *testing.T) {
	handler := newAdminHandler(t, func(cfg *config.Config) {
		cfg.NoTunnel = false
		cfg.TunnelID = ""
		cfg.Tunnels = map[string]config.TunnelConfig{
			"fast": {TunnelID: "tunnel_fast", Mode: "fast", Profile: "fast", RuntimeKeyEnv: "FAST_TUNNEL_KEY"},
			"full": {TunnelID: "tunnel_full", Mode: "full", Profile: "full", RuntimeKeyEnv: "FULL_TUNNEL_KEY"},
		}
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, apiPrefix+"/secrets", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("get named tunnel secrets = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{"FAST_TUNNEL_KEY", "FULL_TUNNEL_KEY", "tunnels.fast.runtimeKeyEnv", "tunnels.full.runtimeKeyEnv"} {
		if !strings.Contains(body, want) {
			t.Fatalf("named tunnel secret reference %q missing: %s", want, body)
		}
	}
	if strings.Contains(body, `"name":"CONTROL_PLANE_API_KEY"`) {
		t.Fatalf("ignored legacy runtime key was exposed with explicit tunnels: %s", body)
	}
}

func newAdminHandler(t *testing.T, mutate func(*config.Config)) *Handler {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WORMHOLE_HOME", home)
	workspace := t.TempDir()
	cfg := config.Default()
	cfg.Workspace = workspace
	cfg.NoTunnel = true
	if mutate != nil {
		mutate(&cfg)
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewWorkspaceContextWithReporter(
		context.Background(), "default", config.AppDataDir(), cfg, "test", "pro", "config-id", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	handler := New(runtime, nil)
	handler.auth = nil
	return handler
}

func localRequest(method, target string, body *strings.Reader) *http.Request {
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, "http://127.0.0.1:8789"+target, nil)
	} else {
		request = httptest.NewRequest(method, "http://127.0.0.1:8789"+target, body)
	}
	request.RemoteAddr = "127.0.0.1:31337"
	return request
}

func csrfCookie(t *testing.T, response *http.Response) *http.Cookie {
	t.Helper()
	return cookieByName(t, response, csrfCookieName)
}

func cookieByName(t *testing.T, response *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %s was not set", name)
	return nil
}

func secureAdminWrite(request *http.Request, cookie *http.Cookie, revision string) {
	request.Header.Set("Origin", "http://127.0.0.1:8789")
	request.Header.Set("X-Wormhole-CSRF", cookie.Value)
	request.Header.Set("If-Match", quoteETag(revision))
	request.AddCookie(cookie)
}

func setAdminUserHome(t *testing.T, home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		return
	}
	t.Setenv("HOME", home)
}
