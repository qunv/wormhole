// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import "testing"

func TestNormalizeGitOrigin(t *testing.T) {
	tests := map[string]string{
		"https://github.com/Owner/Repo.git":            "git:github.com/owner/repo",
		"git@github.com:Owner/Repo.git":                "git:github.com/owner/repo",
		"ssh://git@github.com/Owner/Repo.git":          "git:github.com/owner/repo",
		"https://user:token@github.com/Owner/Repo.git": "git:github.com/owner/repo",
	}
	for input, want := range tests {
		if got := NormalizeGitOrigin(input); got != want {
			t.Fatalf("NormalizeGitOrigin(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveProjectPathHash(t *testing.T) {
	first := ResolveProject(t.TempDir(), "path-hash")
	if len(first) <= len("workspace:") || first[:len("workspace:")] != "workspace:" {
		t.Fatalf("project = %q", first)
	}
}
