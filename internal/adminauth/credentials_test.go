// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package adminauth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCredentialsAreOwnerOnlyHashedAndVerifiable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-auth.json")
	credential, err := setCredentials(path, "admin", "correct horse battery staple", 20_000)
	if err != nil {
		t.Fatal(err)
	}
	if credential.PasswordHash == "" || credential.Salt == "" || credential.CredentialVersion == "" {
		t.Fatalf("credential is incomplete: %#v", credential)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "correct horse battery staple") {
		t.Fatal("credential file contains the plaintext password")
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("credential permissions = %o, want owner-only", info.Mode().Perm())
	}
	loaded, err := LoadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(loaded, "admin", "correct horse battery staple") {
		t.Fatal("valid credentials were rejected")
	}
	if VerifyPassword(loaded, "other", "correct horse battery staple") || VerifyPassword(loaded, "admin", "wrong password") {
		t.Fatal("invalid credentials were accepted")
	}
}

func TestInitialCredentialsNeverReplaceExistingAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-auth.json")
	if _, err := SetInitialCredentials(path, "admin", "first password value"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetInitialCredentials(path, "other", "second password value"); !errors.Is(err, ErrAlreadyConfigured) {
		t.Fatalf("second initialization error = %v", err)
	}
	credential, err := LoadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(credential, "admin", "first password value") {
		t.Fatal("initial account was replaced")
	}
	if VerifyPassword(credential, "other", "second password value") {
		t.Fatal("replacement account was accepted")
	}
}

func TestCredentialResetInvalidatesExistingSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-auth.json")
	if _, err := setCredentials(path, "admin", "first password value", 20_000); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(path)
	token, _, _, err := manager.Login("admin", "first password value")
	if err != nil {
		t.Fatal(err)
	}
	if valid, _, err := manager.Validate(token); err != nil || !valid {
		t.Fatalf("new session is invalid: valid=%t err=%v", valid, err)
	}
	if _, err := setCredentials(path, "admin", "second password value", 20_000); err != nil {
		t.Fatal(err)
	}
	if valid, _, err := manager.Validate(token); err != nil || valid {
		t.Fatalf("reset session remained valid: valid=%t err=%v", valid, err)
	}
}

func TestLoginFailuresAreRateLimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-auth.json")
	if _, err := setCredentials(path, "admin", "correct password value", 10_000); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(path)
	now := time.Now().UTC()
	manager.now = func() time.Time { return now }
	for attempt := 0; attempt < maxFailures; attempt++ {
		if _, _, _, err := manager.Login("admin", "incorrect password"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d error = %v", attempt+1, err)
		}
	}
	if _, _, retryAfter, err := manager.Login("admin", "correct password value"); !errors.Is(err, ErrRateLimited) || retryAfter <= 0 {
		t.Fatalf("rate limit error=%v retry=%s", err, retryAfter)
	}
	now = now.Add(failureBlockPeriod + time.Second)
	if _, _, _, err := manager.Login("admin", "correct password value"); err != nil {
		t.Fatalf("valid login after block failed: %v", err)
	}
}

func TestPasswordMinimumLengthIsEightCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-auth.json")
	if _, err := setCredentials(path, "admin", "12345678", 10_000); err != nil {
		t.Fatalf("8-character password was rejected: %v", err)
	}
	if _, err := setCredentials(path, "admin", "1234567", 10_000); err == nil || !strings.Contains(err.Error(), "at least 8") {
		t.Fatalf("7-character password error = %v", err)
	}
}

func TestMissingCredentialFileIsNotConfigured(t *testing.T) {
	_, err := LoadCredentials(filepath.Join(t.TempDir(), "missing.json"))
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("missing credential error = %v", err)
	}
}
