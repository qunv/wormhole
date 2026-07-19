package upstreammcp

import (
	"strings"
	"testing"
)

func TestComposeWindowsBatchCommand(t *testing.T) {
	got, err := composeWindowsBatchCommand(`C:\Program Files\nodejs\npx.cmd`, []string{"-y", "@scope/server", `C:\My Project`})
	if err != nil {
		t.Fatal(err)
	}
	want := `""C:\Program Files\nodejs\npx.cmd" "-y" "@scope/server" "C:\My Project""`
	if got != want {
		t.Fatalf("batch command line = %q, want %q", got, want)
	}
}

func TestComposeWindowsBatchCommandRejectsExpansionCharacters(t *testing.T) {
	for _, value := range []string{`%PATH%`, `value!`, `bad"quote`, "line\nbreak"} {
		if _, err := composeWindowsBatchCommand(`C:\node\npx.cmd`, []string{value}); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("unsafe batch token %q was not rejected: %v", value, err)
		}
	}
}
