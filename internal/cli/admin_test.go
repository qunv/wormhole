// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"codebridge/internal/adminauth"
	"codebridge/internal/config"
)

func TestAdminSetPasswordCreatesLocalCredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEBRIDGE_HOME", home)
	var output bytes.Buffer
	app := App{
		Name: "Codebridge", Version: "test", Tier: "test",
		Stdout: &output, Stderr: &output,
		Stdin: strings.NewReader("correct horse battery staple\ncorrect horse battery staple\n"),
	}
	if err := app.Run(context.Background(), []string{"admin", "set-password", "local-admin"}); err != nil {
		t.Fatal(err)
	}
	credential, err := adminauth.LoadCredentials(config.AdminAuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if credential.Username != "local-admin" || !adminauth.VerifyPassword(credential, "local-admin", "correct horse battery staple") {
		t.Fatalf("unexpected admin credential: %#v", credential)
	}
	if strings.Contains(string(mustReadFile(t, config.AdminAuthPath())), "correct horse battery staple") {
		t.Fatal("CLI persisted the plaintext password")
	}
	if !strings.Contains(output.String(), "Existing browser sessions are invalidated") {
		t.Fatalf("CLI output did not explain session invalidation: %s", output.String())
	}
}

func TestAdminSetPasswordRejectsMismatchedConfirmation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEBRIDGE_HOME", home)
	app := App{
		Name: "Codebridge", Version: "test", Tier: "test",
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		Stdin: strings.NewReader("first password value\nsecond password value\n"),
	}
	if err := app.Run(context.Background(), []string{"admin", "set-password"}); err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("mismatched password error = %v", err)
	}
	if _, err := os.Stat(config.AdminAuthPath()); !os.IsNotExist(err) {
		t.Fatalf("credential file exists after mismatch: %v", err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
