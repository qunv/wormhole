package mcpserver

import (
	"testing"

	"wormhole/internal/agent"
	"wormhole/internal/config"
)

func TestFastProfileExposesCompactCodingTools(t *testing.T) {
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.Audit = false
	cfg.Memory.Enabled = false
	runtime, err := agent.New(cfg, "test", "pro", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	if got := ProfileToolCount(runtime, ToolProfileFast); got != 11 {
		t.Fatalf("fast profile tool count = %d, want 11", got)
	}
	if got := ProfileToolCount(runtime, ToolProfileFull); got <= 11 {
		t.Fatalf("full profile tool count = %d, want more than 11", got)
	}
}

func TestSessionProfileToolsIncludeControlsAndWorkspaceAvailability(t *testing.T) {
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.Audit = false
	cfg.Memory.Enabled = false
	runtime, err := agent.New(cfg, "test", "pro", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	router := NewSessionRouter(runtime, nil)
	fast := router.ProfileTools(ToolProfileFast)
	if got, want := len(fast), ProfileToolCount(runtime, ToolProfileFast)+4; got != want {
		t.Fatalf("fast session profile tools = %d, want %d", got, want)
	}
	byName := map[string]ProfileToolInfo{}
	for _, tool := range fast {
		byName[tool.Name] = tool
	}
	for _, control := range []string{"workspace_select", "workspace_current", "workspace_list", "workspace_clear"} {
		if byName[control].Scope != "session" {
			t.Fatalf("control tool %q missing or has wrong scope: %#v", control, byName[control])
		}
	}
	read := byName["read_file"]
	if read.Scope != "workspace" || len(read.WorkspaceIDs) != 1 || read.WorkspaceIDs[0] != "default" {
		t.Fatalf("read_file availability = %#v", read)
	}
	if _, exposed := byName["memory_search"]; exposed {
		t.Fatal("fast profile unexpectedly exposed memory_search")
	}
}

func TestAllowedToolsRestrictsProfiles(t *testing.T) {
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.Audit = false
	cfg.Memory.Enabled = false
	cfg.Tools.AllowedTools = []string{"read_file", "git_status"}
	runtime, err := agent.New(cfg, "test", "pro", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	if got := ProfileToolCount(runtime, ToolProfileFull); got != 2 {
		t.Fatalf("restricted full profile count = %d, want 2", got)
	}
	if got := ProfileToolCount(runtime, ToolProfileFast); got != 2 {
		t.Fatalf("restricted fast profile count = %d, want 2", got)
	}
}
