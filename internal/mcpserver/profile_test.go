package mcpserver

import (
	"testing"

	"codebridge/internal/agent"
	"codebridge/internal/config"
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
