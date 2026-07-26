// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package maintenance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGCStateRemovesOnlyRegenerableOrEmptyWorkspaceState(t *testing.T) {
	dataDir := t.TempDir()
	workspaceRoot := filepath.Join(dataDir, "workspaces")
	emptyDir := filepath.Join(workspaceRoot, "empty")
	staleDir := filepath.Join(workspaceRoot, "stale")
	durableDir := filepath.Join(workspaceRoot, "durable")
	for _, dir := range []string{
		filepath.Join(emptyDir, "backups"), filepath.Join(emptyDir, "approvals"),
		filepath.Join(staleDir, "backups"), filepath.Join(staleDir, "approvals"),
		durableDir,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	missingRoot := filepath.Join(t.TempDir(), "removed")
	index := map[string]any{
		"ts":   time.Now().UTC().Format(time.RFC3339Nano),
		"root": missingRoot,
	}
	raw, _ := json.Marshal(index)
	if err := os.WriteFile(filepath.Join(staleDir, "index.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(durableDir, "notes.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dryRun, err := GCState([]string{dataDir}, StateGCOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.RemovedWorkspaceDirs != 2 || dryRun.RemovedCacheFiles != 1 {
		t.Fatalf("unexpected dry-run report: %#v", dryRun)
	}
	for _, path := range []string{emptyDir, staleDir, durableDir} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("dry-run changed %s: %v", path, err)
		}
	}

	report, err := GCState([]string{dataDir}, StateGCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.RemovedWorkspaceDirs != 2 || report.RemovedCacheFiles != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
	for _, path := range []string{emptyDir, staleDir} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("garbage state remained at %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(durableDir, "notes.json")); err != nil {
		t.Fatalf("durable state was removed: %v", err)
	}
}

func TestGCStatePrunesTerminalApprovalsAfterRetention(t *testing.T) {
	dataDir := t.TempDir()
	workspaceDir := filepath.Join(dataDir, "workspaces", "approval-only")
	approvalsDir := filepath.Join(workspaceDir, "approvals")
	if err := os.MkdirAll(approvalsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := map[string]any{
		"status": "consumed", "created": now.Add(-40 * 24 * time.Hour).Format(time.RFC3339Nano),
		"expires_at":  now.Add(-39 * 24 * time.Hour).Format(time.RFC3339Nano),
		"consumed_at": now.Add(-39 * 24 * time.Hour).Format(time.RFC3339Nano),
	}
	recent := map[string]any{
		"status": "consumed", "created": now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
		"expires_at":  now.Add(-time.Hour).Format(time.RFC3339Nano),
		"consumed_at": now.Add(-time.Hour).Format(time.RFC3339Nano),
	}
	for name, value := range map[string]any{"old.json": old, "recent.json": recent} {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(approvalsDir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := GCState([]string{dataDir}, StateGCOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if report.RemovedApprovalFiles != 1 || report.RemovedWorkspaceDirs != 0 {
		t.Fatalf("unexpected approval report: %#v", report)
	}
	if _, err := os.Stat(filepath.Join(approvalsDir, "old.json")); !os.IsNotExist(err) {
		t.Fatalf("expired terminal approval remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(approvalsDir, "recent.json")); err != nil {
		t.Fatalf("recent terminal approval was removed: %v", err)
	}
}

func TestGCStateHonorsWorkspaceScanLimit(t *testing.T) {
	dataDir := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.MkdirAll(filepath.Join(dataDir, "workspaces", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	report, err := GCState([]string{dataDir}, StateGCOptions{DryRun: true, MaxWorkspaceDirs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if report.ScannedWorkspaceDirs != 2 || !report.ScanLimitReached {
		t.Fatalf("scan limit not enforced: %#v", report)
	}
}
