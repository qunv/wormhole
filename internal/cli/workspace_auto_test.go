package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wormhole/internal/config"
	"wormhole/internal/workspaceregistry"
)

func TestEnsureAutoWorkspaceCreatesSlugFromFolderName(t *testing.T) {
	configureWorkspaceTestPaths(t)
	base := t.TempDir()
	root := filepath.Join(base, "Loyalty API.Service")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}

	entry, created, enabled, err := app.ensureAutoWorkspace(cfg, root, options{})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "loyalty-api-service" || !created || !enabled || !entry.Enabled {
		t.Fatalf("unexpected auto registration: entry=%#v created=%t enabled=%t", entry, created, enabled)
	}
	if !sameWorkspacePath(entry.Workspace, root) {
		t.Fatalf("registered root = %q, want %q", entry.Workspace, root)
	}
	if _, err := os.Stat(entry.ConfigPath); err != nil {
		t.Fatalf("workspace config was not created: %v", err)
	}
}

func TestEnsureAutoWorkspaceReusesPathAndReenablesEntry(t *testing.T) {
	configureWorkspaceTestPaths(t)
	root := filepath.Join(t.TempDir(), "orders-api")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	if err := app.workspaceAdd(cfg, options{Rest: []string{"add", "custom-orders", root}}); err != nil {
		t.Fatal(err)
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Workspaces["custom-orders"]
	entry.Enabled = false
	registry.Workspaces[entry.ID] = entry
	if err := workspaceregistry.Save(registry); err != nil {
		t.Fatal(err)
	}

	updated, created, enabled, err := app.ensureAutoWorkspace(cfg, filepath.Join(root, "."), options{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != "custom-orders" || created || !enabled || !updated.Enabled {
		t.Fatalf("unexpected reused registration: entry=%#v created=%t enabled=%t", updated, created, enabled)
	}
}

func TestEnsureAutoWorkspaceAddsStableHashOnNameCollision(t *testing.T) {
	configureWorkspaceTestPaths(t)
	first := filepath.Join(t.TempDir(), "service-api")
	second := filepath.Join(t.TempDir(), "service-api")
	for _, root := range []string{first, second} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	one, _, _, err := app.ensureAutoWorkspace(cfg, first, options{})
	if err != nil {
		t.Fatal(err)
	}
	two, _, _, err := app.ensureAutoWorkspace(cfg, second, options{})
	if err != nil {
		t.Fatal(err)
	}
	if one.ID != "service-api" {
		t.Fatalf("first ID = %q", one.ID)
	}
	if two.ID == one.ID || !strings.HasPrefix(two.ID, "service-api-") || len(two.ID) > 32 {
		t.Fatalf("collision ID = %q", two.ID)
	}
	again, created, enabled, err := app.ensureAutoWorkspace(cfg, second, options{})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != two.ID || created || enabled {
		t.Fatalf("collision ID was not stable: first=%q again=%q created=%t enabled=%t", two.ID, again.ID, created, enabled)
	}
}

func TestEnsureAutoWorkspaceUsesPrimaryFolderNameWithoutRegistration(t *testing.T) {
	configureWorkspaceTestPaths(t)
	root := filepath.Join(t.TempDir(), "default-repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Workspace = root
	app := App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}

	entry, created, enabled, err := app.ensureAutoWorkspace(cfg, root, options{})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "default-repo" || created || enabled {
		t.Fatalf("primary workspace was treated as named: %#v created=%t enabled=%t", entry, created, enabled)
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Workspaces) != 0 {
		t.Fatalf("primary workspace leaked into registry: %#v", registry.Workspaces)
	}
}

func TestPrintAutoWorkspaceShowsEndpoint(t *testing.T) {
	var output bytes.Buffer
	app := App{Stdout: &output}
	entry := workspaceregistry.Registration{ID: "orders-api", Workspace: "orders", ConfigPath: "config.json", Enabled: true}
	app.printAutoWorkspace(entry, true, true, 8789)
	text := output.String()
	if !strings.Contains(text, "auto-registered orders-api") || !strings.Contains(text, "/mcp/workspaces/orders-api") {
		t.Fatalf("unexpected auto workspace output: %s", text)
	}
}
