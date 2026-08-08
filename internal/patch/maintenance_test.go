// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package patch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupPathBudgetStopsBeforeExceedingDiskQuota(t *testing.T) {
	source := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(source, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "backup.txt")
	budget := &backupBudget{remainingBytes: 4, remainingEntries: 10}
	if _, err := backupPathContextWithBudget(t.Context(), source, destination, budget); err == nil {
		t.Fatal("expected backup quota failure")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("partial oversized backup remained: %v", err)
	}
}

func TestPruneBackupsRemovesOrphansAndAppliesQuota(t *testing.T) {
	workspaceDir := t.TempDir()
	backupsDir := filepath.Join(workspaceDir, "backups")
	if err := os.MkdirAll(backupsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var history []backupBatch
	for index, name := range []string{"old", "middle", "new"} {
		path := filepath.Join(backupsDir, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("1234567890"), 0o600); err != nil {
			t.Fatal(err)
		}
		history = append(history, backupBatch{
			ID: name, TS: now.Add(time.Duration(index-2) * time.Hour).Format(time.RFC3339Nano),
			BatchDir: path,
		})
	}
	orphan := filepath.Join(backupsDir, "orphan")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "file.txt"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupHistory(filepath.Join(workspaceDir, "patch-history.json"), history); err != nil {
		t.Fatal(err)
	}

	dryRun, err := PruneBackups(workspaceDir, BackupPruneOptions{
		DryRun: true, Now: now, MaxBatches: 2, MaxAge: 24 * time.Hour, MaxBytes: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Removed != 2 || dryRun.Orphans != 1 || dryRun.Retained != 2 {
		t.Fatalf("unexpected dry-run report: %#v", dryRun)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("dry-run removed orphan: %v", err)
	}

	report, err := PruneBackups(workspaceDir, BackupPruneOptions{
		Now: now, MaxBatches: 2, MaxAge: 24 * time.Hour, MaxBytes: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Removed != 2 || report.Orphans != 1 || report.Retained != 2 {
		t.Fatalf("unexpected report: %#v", report)
	}
	for _, path := range []string{filepath.Join(backupsDir, "old"), orphan} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("removed backup remains at %s: %v", path, err)
		}
	}
	retained, err := readBackupHistory(filepath.Join(workspaceDir, "patch-history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(retained) != 2 || retained[0].ID != "middle" || retained[1].ID != "new" {
		t.Fatalf("unexpected retained history: %#v", retained)
	}
}

func TestPruneBackupsRebasesMigratedAbsoluteBatchPath(t *testing.T) {
	workspaceDir := t.TempDir()
	backupsDir := filepath.Join(workspaceDir, "backups")
	batchDir := filepath.Join(backupsDir, "123")
	if err := os.MkdirAll(batchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batchDir, "file"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	history := []backupBatch{{
		ID: "123", TS: time.Now().UTC().Format(time.RFC3339Nano),
		BatchDir: filepath.Join(t.TempDir(), "old-state", "backups", "123"),
	}}
	historyPath := filepath.Join(workspaceDir, "patch-history.json")
	if err := writeBackupHistory(historyPath, history); err != nil {
		t.Fatal(err)
	}
	report, err := PruneBackups(workspaceDir, BackupPruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Removed != 0 || report.Retained != 1 {
		t.Fatalf("migrated backup was not retained: %#v", report)
	}
	updated, err := readBackupHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || filepath.Clean(updated[0].BatchDir) != filepath.Clean(batchDir) {
		t.Fatalf("batch path was not rebased: %#v", updated)
	}
}

func TestSelectBackupHistoryAlwaysProtectsNewBatch(t *testing.T) {
	workspaceDir := t.TempDir()
	backupsDir := filepath.Join(workspaceDir, "backups")
	if err := os.MkdirAll(backupsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var history []backupBatch
	for _, name := range []string{"old", "current"} {
		path := filepath.Join(backupsDir, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "file"), []byte("1234567890"), 0o600); err != nil {
			t.Fatal(err)
		}
		history = append(history, backupBatch{ID: name, TS: now.Format(time.RFC3339Nano), BatchDir: path})
	}
	remainingEntries := 100
	retained, _, err := selectBackupHistory(history, backupsDir, BackupPruneOptions{
		Now: now, MaxBatches: 1, MaxBytes: 10, MaxAge: time.Hour, ProtectedBatchID: "current",
	}, &remainingEntries)
	if err != nil {
		t.Fatal(err)
	}
	if len(retained) != 1 || retained[0].ID != "current" {
		t.Fatalf("protected batch was not retained: %#v", retained)
	}
}
