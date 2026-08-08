package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfileFilePrefersWormholeAndFallsBackToAgent(t *testing.T) {
	root := t.TempDir()
	canonical := workspaceProfilePath(root)
	legacy := legacyWorkspaceProfilePath(root)
	for _, path := range []string{canonical, legacy} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(legacy, []byte(`{"description":"legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte(`{"description":"canonical"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	profile := loadProfileFile(root)
	if profile["description"] != "canonical" {
		t.Fatalf("canonical profile was not preferred: %#v", profile)
	}
	if got := activeWorkspaceProfilePath(root); got != canonical {
		t.Fatalf("active profile path = %q, want %q", got, canonical)
	}

	if err := os.Remove(canonical); err != nil {
		t.Fatal(err)
	}
	profile = loadProfileFile(root)
	if profile["description"] != "legacy" {
		t.Fatalf("legacy profile fallback failed: %#v", profile)
	}
	if got := activeWorkspaceProfilePath(root); got != legacy {
		t.Fatalf("legacy active profile path = %q, want %q", got, legacy)
	}
}

func TestMissingProfileReportsCanonicalPath(t *testing.T) {
	root := t.TempDir()
	if got, want := activeWorkspaceProfilePath(root), filepath.Join(root, ".wormhole", "profile.json"); got != want {
		t.Fatalf("active profile path = %q, want %q", got, want)
	}
}
