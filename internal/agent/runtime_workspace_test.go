package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"codebridge/internal/config"
)

func TestWorkspaceRuntimesIsolateStateAndAuditIdentity(t *testing.T) {
	defaultRuntime := newIsolatedRuntime(t, "default", t.TempDir(), t.TempDir(), "full")
	apiRuntime := newIsolatedRuntime(t, "api", t.TempDir(), t.TempDir(), "full")

	if _, err := defaultRuntime.HandleSession(context.Background(), "mcp:default", "save_note", map[string]any{
		"title": "default", "body": "default-only",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := apiRuntime.HandleSession(context.Background(), "workspace:api:mcp:chat", "save_note", map[string]any{
		"title": "api", "body": "api-only",
	}); err != nil {
		t.Fatal(err)
	}

	if defaultRuntime.Store.DataDir == apiRuntime.Store.DataDir || defaultRuntime.Store.NotesPath == apiRuntime.Store.NotesPath {
		t.Fatalf("workspace state paths overlap: default=%s api=%s", defaultRuntime.Store.NotesPath, apiRuntime.Store.NotesPath)
	}
	var defaultNotes, apiNotes []note
	if err := defaultRuntime.Store.ReadJSON(defaultRuntime.Store.NotesPath, &defaultNotes); err != nil {
		t.Fatal(err)
	}
	if err := apiRuntime.Store.ReadJSON(apiRuntime.Store.NotesPath, &apiNotes); err != nil {
		t.Fatal(err)
	}
	if len(defaultNotes) != 1 || defaultNotes[0].Body != "default-only" {
		t.Fatalf("unexpected default notes: %#v", defaultNotes)
	}
	if len(apiNotes) != 1 || apiNotes[0].Body != "api-only" {
		t.Fatalf("unexpected api notes: %#v", apiNotes)
	}

	raw, err := os.ReadFile(apiRuntime.Store.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(raw))
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatal(err)
	}
	if record["workspace_id"] != "api" || record["session_id"] != "workspace:api:mcp:chat" {
		t.Fatalf("audit identity = %#v", record)
	}
	if record["workspace"] != apiRuntime.Workspace.Primary {
		t.Fatalf("audit workspace = %#v, want %q", record["workspace"], apiRuntime.Workspace.Primary)
	}
}

func TestNamedWorkspaceScopesApprovalActions(t *testing.T) {
	runtime := newIsolatedRuntime(t, "api", t.TempDir(), t.TempDir(), "balanced")
	err := runtime.enforcePolicy("delete_path", map[string]any{"path": "obsolete.txt"})
	if err == nil {
		t.Fatal("delete_path unexpectedly bypassed approval")
	}
	want := `workspace:api:delete_path:obsolete.txt`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("approval action is not workspace-scoped: %v", err)
	}
}

func newIsolatedRuntime(t *testing.T, id, root, dataDir, policy string) *Runtime {
	t.Helper()
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = root, true, policy
	runtime, err := NewWorkspaceContextWithReporter(context.Background(), id, dataDir, cfg, "test", "pro", "config-"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return runtime
}
