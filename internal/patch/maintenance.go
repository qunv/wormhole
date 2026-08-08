// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package patch

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	DefaultBackupMaxBatches  = 50
	DefaultBackupMaxAge      = 30 * 24 * time.Hour
	DefaultBackupMaxBytes    = int64(256 << 20)
	DefaultBackupBatchBytes  = int64(128 << 20)
	DefaultBackupScanEntries = 100_000
)

var ErrBackupScanLimit = errors.New("backup scan entry limit reached")

type BackupPruneOptions struct {
	Now              time.Time
	DryRun           bool
	MaxBatches       int
	MaxAge           time.Duration
	MaxBytes         int64
	MaxScanEntries   int
	ProtectedBatchID string
}

type BackupPruneReport struct {
	HistoryEntries int      `json:"history_entries"`
	Retained       int      `json:"retained"`
	Removed        int      `json:"removed"`
	Orphans        int      `json:"orphans"`
	HistoryRemoved bool     `json:"history_removed,omitempty"`
	HistoryPath    string   `json:"history_path,omitempty"`
	BytesFreed     int64    `json:"bytes_freed"`
	RemovedPaths   []string `json:"removed_paths,omitempty"`
}

type backupCandidate struct {
	batch backupBatch
	index int
	path  string
	ts    time.Time
	size  int64
}

func normalizeBackupPruneOptions(options BackupPruneOptions) BackupPruneOptions {
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	if options.MaxBatches <= 0 {
		options.MaxBatches = DefaultBackupMaxBatches
	}
	if options.MaxAge <= 0 {
		options.MaxAge = DefaultBackupMaxAge
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultBackupMaxBytes
	}
	if options.MaxScanEntries <= 0 {
		options.MaxScanEntries = DefaultBackupScanEntries
	}
	return options
}

// PruneBackups removes backup directories not referenced by patch history and
// applies count, age, and byte retention to referenced batches. Paths outside
// the workspace backup directory are never followed or deleted.
func PruneBackups(workspaceDir string, options BackupPruneOptions) (BackupPruneReport, error) {
	options = normalizeBackupPruneOptions(options)
	historyPath := filepath.Join(workspaceDir, "patch-history.json")
	backupsDir := filepath.Join(workspaceDir, "backups")

	historyInfo, historyStatErr := os.Stat(historyPath)
	historyExists := historyStatErr == nil
	if historyStatErr != nil && !errors.Is(historyStatErr, os.ErrNotExist) {
		return BackupPruneReport{}, historyStatErr
	}
	history, err := readBackupHistory(historyPath)
	if err != nil {
		return BackupPruneReport{}, err
	}
	remainingEntries := options.MaxScanEntries
	retained, removed, err := selectBackupHistory(history, backupsDir, options, &remainingEntries)
	if err != nil {
		return BackupPruneReport{}, err
	}
	report := BackupPruneReport{HistoryEntries: len(history), Retained: len(retained)}
	if historyExists && len(retained) == 0 {
		report.HistoryRemoved = true
		report.HistoryPath = historyPath
		report.BytesFreed += historyInfo.Size()
	}

	retainedPaths := make(map[string]struct{}, len(retained))
	for _, batch := range retained {
		if path, ok := safeBatchPath(backupsDir, batch); ok {
			retainedPaths[path] = struct{}{}
		}
	}
	removedPaths := map[string]int64{}
	for path, size := range removed {
		removedPaths[path] = size
	}

	entries, readErr := os.ReadDir(backupsDir)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return BackupPruneReport{}, readErr
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(backupsDir, entry.Name())
		if _, keep := retainedPaths[path]; keep {
			continue
		}
		if _, already := removedPaths[path]; already {
			continue
		}
		size, sizeErr := pathSize(path, &remainingEntries)
		if sizeErr != nil {
			return BackupPruneReport{}, sizeErr
		}
		removedPaths[path] = size
		report.Orphans++
	}

	paths := make([]string, 0, len(removedPaths))
	for path := range removedPaths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		report.BytesFreed += removedPaths[path]
		report.RemovedPaths = append(report.RemovedPaths, path)
	}
	report.Removed = len(paths)
	if options.DryRun {
		return report, nil
	}

	if len(retained) == 0 {
		if err := os.Remove(historyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return BackupPruneReport{}, err
		}
	} else if backupHistoryChanged(history, retained) {
		if err := writeBackupHistory(historyPath, retained); err != nil {
			return BackupPruneReport{}, err
		}
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return BackupPruneReport{}, err
		}
	}
	_ = removeDirIfEmpty(backupsDir)
	return report, nil
}

func readBackupHistory(path string) ([]backupBatch, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var history []backupBatch
	if err := json.Unmarshal(raw, &history); err != nil {
		return nil, err
	}
	return history, nil
}

func writeBackupHistory(path string, history []backupBatch) error {
	raw, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(raw, '\n'), 0o600)
}

func selectBackupHistory(history []backupBatch, backupsDir string, options BackupPruneOptions, remainingEntries *int) ([]backupBatch, map[string]int64, error) {
	options = normalizeBackupPruneOptions(options)
	candidates := make([]backupCandidate, 0, len(history))
	normalized := make(map[int]backupBatch, len(history))
	removed := map[string]int64{}
	for index, batch := range history {
		path, ok := safeBatchPath(backupsDir, batch)
		if !ok {
			continue
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		size, err := pathSize(path, remainingEntries)
		if err != nil {
			return nil, nil, err
		}
		ts, _ := time.Parse(time.RFC3339Nano, batch.TS)
		if ts.IsZero() {
			ts = info.ModTime()
		}
		batch.BatchDir = path
		normalized[index] = batch
		candidates = append(candidates, backupCandidate{batch: batch, index: index, path: path, ts: ts, size: size})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].ts.Equal(candidates[j].ts) {
			return candidates[i].index > candidates[j].index
		}
		return candidates[i].ts.After(candidates[j].ts)
	})
	keep := make(map[int]struct{}, len(candidates))
	var retainedBytes int64
	retainedCount := 0
	for _, candidate := range candidates {
		protected := candidate.batch.ID != "" && candidate.batch.ID == options.ProtectedBatchID
		withinAge := !candidate.ts.IsZero() && options.Now.Sub(candidate.ts) <= options.MaxAge
		withinCount := retainedCount < options.MaxBatches
		withinBytes := retainedBytes+candidate.size <= options.MaxBytes
		if protected || (withinAge && withinCount && withinBytes) {
			keep[candidate.index] = struct{}{}
			retainedCount++
			retainedBytes += candidate.size
			continue
		}
		removed[candidate.path] = candidate.size
	}

	retained := make([]backupBatch, 0, len(keep))
	for index := range history {
		if _, ok := keep[index]; ok {
			retained = append(retained, normalized[index])
		}
	}
	return retained, removed, nil
}

func safeBatchPath(backupsDir string, batch backupBatch) (string, bool) {
	absoluteBackups, err := filepath.Abs(backupsDir)
	if err != nil {
		return "", false
	}
	absoluteBackups = filepath.Clean(absoluteBackups)
	if safeBatchID(batch.ID) {
		rebased := filepath.Join(absoluteBackups, batch.ID)
		if info, statErr := os.Lstat(rebased); statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return rebased, true
		}
	}
	if batch.BatchDir == "" {
		return "", false
	}
	absoluteCandidate, err := filepath.Abs(batch.BatchDir)
	if err != nil {
		return "", false
	}
	absoluteCandidate = filepath.Clean(absoluteCandidate)
	relative, err := filepath.Rel(absoluteBackups, absoluteCandidate)
	if err != nil || relative == "." || filepath.Dir(relative) != "." || relative == ".." {
		return "", false
	}
	return absoluteCandidate, true
}

func safeBatchID(id string) bool {
	return id != "" && id != "." && id != ".." && filepath.Base(id) == id
}

func backupHistoryChanged(before, after []backupBatch) bool {
	if len(before) != len(after) {
		return true
	}
	for index := range before {
		if before[index].ID != after[index].ID || filepath.Clean(before[index].BatchDir) != filepath.Clean(after[index].BatchDir) {
			return true
		}
	}
	return false
}

func pathSize(path string, remainingEntries *int) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if remainingEntries != nil {
			if *remainingEntries <= 0 {
				return ErrBackupScanLimit
			}
			(*remainingEntries)--
		}
		if entry.Type().IsRegular() {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func removeUnreferencedBackupDirs(backupsDir string, history []backupBatch) {
	retained := make(map[string]struct{}, len(history))
	for _, batch := range history {
		if path, ok := safeBatchPath(backupsDir, batch); ok {
			retained[path] = struct{}{}
		}
	}
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(backupsDir, entry.Name())
		if _, ok := retained[path]; !ok {
			_ = os.RemoveAll(path)
		}
	}
	_ = removeDirIfEmpty(backupsDir)
}

func removeDirIfEmpty(path string) error {
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return os.Remove(path)
	}
	return nil
}
