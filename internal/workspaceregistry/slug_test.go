package workspaceregistry

import (
	"path/filepath"
	"testing"
)

func TestSlugID(t *testing.T) {
	cases := map[string]string{
		"Loyalty API":                          "loyalty-api",
		"  Repo_Name--Feature  ":               "repo-name-feature",
		"---":                                  "workspace",
		"123 Service":                          "123-service",
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789": "abcdefghijklmnopqrstuvwxyz012345",
	}
	for input, want := range cases {
		if got := SlugID(input); got != want {
			t.Fatalf("SlugID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIDFromPathUsesFolderName(t *testing.T) {
	path := filepath.Join("home", "user", "CodeBridge Repo")
	if got := IDFromPath(path); got != "codebridge-repo" {
		t.Fatalf("IDFromPath(%q) = %q", path, got)
	}
}
