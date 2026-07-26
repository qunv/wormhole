// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package maintenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"codebridge/internal/patch"
)

const (
	DefaultCacheMaxAge             = 7 * 24 * time.Hour
	DefaultApprovalMaxAge          = 30 * 24 * time.Hour
	DefaultStartupScanLimit        = 100
	DefaultWorkspaceScanEntryLimit = 100_000
	DefaultActionLimit             = 200
)

var ErrStateScanLimit = errors.New("workspace state scan entry limit reached")

type StateGCOptions struct {
	Now                    time.Time
	DryRun                 bool
	MaxWorkspaceDirs       int
	CacheMaxAge            time.Duration
	ApprovalMaxAge         time.Duration
	MaxEntriesPerWorkspace int
	ActionLimit            int
}

type StateGCAction struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Bytes  int64  `json:"bytes,omitempty"`
}

type StateGCReport struct {
	DryRun               bool            `json:"dry_run"`
	DataDirs             []string        `json:"data_dirs"`
	ScannedWorkspaceDirs int             `json:"scanned_workspace_dirs"`
	RemovedWorkspaceDirs int             `json:"removed_workspace_dirs"`
	RemovedCacheFiles    int             `json:"removed_cache_files"`
	RemovedBackupDirs    int             `json:"removed_backup_dirs"`
	RemovedOrphanBackups int             `json:"removed_orphan_backups"`
	RemovedApprovalFiles int             `json:"removed_approval_files"`
	BytesFreed           int64           `json:"bytes_freed"`
	ScanLimitReached     bool            `json:"scan_limit_reached"`
	ActionsTruncated     bool            `json:"actions_truncated"`
	Actions              []StateGCAction `json:"actions,omitempty"`
	Warnings             []string        `json:"warnings,omitempty"`
}

type indexMetadata struct {
	TS   time.Time `json:"ts"`
	Root string    `json:"root"`
}

type approvalMetadata struct {
	Status     string `json:"status"`
	Created    string `json:"created"`
	ExpiresAt  string `json:"expires_at"`
	DeniedAt   string `json:"denied_at"`
	ConsumedAt string `json:"consumed_at"`
}

type approvalRemoval struct {
	Path  string
	Bytes int64
}

func normalizeStateGCOptions(options StateGCOptions) StateGCOptions {
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	if options.CacheMaxAge <= 0 {
		options.CacheMaxAge = DefaultCacheMaxAge
	}
	if options.ApprovalMaxAge <= 0 {
		options.ApprovalMaxAge = DefaultApprovalMaxAge
	}
	if options.MaxEntriesPerWorkspace <= 0 {
		options.MaxEntriesPerWorkspace = DefaultWorkspaceScanEntryLimit
	}
	if options.ActionLimit <= 0 {
		options.ActionLimit = DefaultActionLimit
	}
	return options
}

// GCState cleans regenerable and bounded state while preserving durable notes,
// tasks, checkpoints, decisions, and any unknown files. Data directories may
// include the primary state root and named-workspace instance roots.
func GCState(dataDirs []string, options StateGCOptions) (StateGCReport, error) {
	options = normalizeStateGCOptions(options)
	report := StateGCReport{DryRun: options.DryRun}
	unique := uniqueExistingPaths(dataDirs)
	report.DataDirs = append(report.DataDirs, unique...)

	for _, dataDir := range unique {
		workspaceRoot := filepath.Join(dataDir, "workspaces")
		entries, err := os.ReadDir(workspaceRoot)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("read %s: %v", workspaceRoot, err))
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if options.MaxWorkspaceDirs > 0 && report.ScannedWorkspaceDirs >= options.MaxWorkspaceDirs {
				report.ScanLimitReached = true
				return report, nil
			}
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			report.ScannedWorkspaceDirs++
			workspaceDir := filepath.Join(workspaceRoot, entry.Name())
			if err := gcWorkspaceDir(workspaceDir, options, &report); err != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("gc %s: %v", workspaceDir, err))
			}
		}
	}
	return report, nil
}

func gcWorkspaceDir(workspaceDir string, options StateGCOptions, report *StateGCReport) error {
	remainingEntries := options.MaxEntriesPerWorkspace
	backupReport, err := patch.PruneBackups(workspaceDir, patch.BackupPruneOptions{
		Now: options.Now, DryRun: options.DryRun,
	})
	if err != nil {
		return fmt.Errorf("prune backups: %w", err)
	}
	removedPaths := make(map[string]struct{}, len(backupReport.RemovedPaths)+1)
	for _, path := range backupReport.RemovedPaths {
		removedPaths[filepath.Clean(path)] = struct{}{}
		addAction(report, options, StateGCAction{Kind: "backup", Path: path, Reason: "orphaned or outside retention quota"})
	}
	report.RemovedBackupDirs += backupReport.Removed
	report.RemovedOrphanBackups += backupReport.Orphans
	report.BytesFreed += backupReport.BytesFreed
	if backupReport.HistoryRemoved {
		removedPaths[filepath.Clean(backupReport.HistoryPath)] = struct{}{}
		addAction(report, options, StateGCAction{Kind: "history", Path: backupReport.HistoryPath, Reason: "no retained backup batches"})
	}

	approvalRemovals, err := staleApprovals(filepath.Join(workspaceDir, "approvals"), options, &remainingEntries)
	if err != nil {
		return err
	}
	for _, removal := range approvalRemovals {
		removedPaths[filepath.Clean(removal.Path)] = struct{}{}
		report.RemovedApprovalFiles++
		report.BytesFreed += removal.Bytes
		addAction(report, options, StateGCAction{Kind: "approval", Path: removal.Path, Reason: "terminal approval retention expired", Bytes: removal.Bytes})
		if !options.DryRun {
			if err := os.Remove(removal.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}

	indexPath := filepath.Join(workspaceDir, "index.json")
	removeIndex, indexBytes, reason, err := staleIndex(indexPath, options)
	if err != nil {
		return err
	}
	if removeIndex {
		removedPaths[filepath.Clean(indexPath)] = struct{}{}
		report.RemovedCacheFiles++
		report.BytesFreed += indexBytes
		addAction(report, options, StateGCAction{Kind: "cache", Path: indexPath, Reason: reason, Bytes: indexBytes})
		if !options.DryRun {
			if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}

	empty, bytes, err := workspaceEmptyAfterRemovals(workspaceDir, removedPaths, &remainingEntries)
	if err != nil {
		return err
	}
	if !empty {
		if !options.DryRun {
			_ = removeIfEmpty(filepath.Join(workspaceDir, "backups"))
			_ = removeIfEmpty(filepath.Join(workspaceDir, "approvals"))
		}
		return nil
	}

	report.RemovedWorkspaceDirs++
	// bytes contains only directory-entry payload not already counted above.
	report.BytesFreed += bytes
	addAction(report, options, StateGCAction{Kind: "workspace", Path: workspaceDir, Reason: "contains no durable state", Bytes: bytes})
	if !options.DryRun {
		return os.RemoveAll(workspaceDir)
	}
	return nil
}

func staleApprovals(approvalsDir string, options StateGCOptions, remainingEntries *int) ([]approvalRemoval, error) {
	entries, err := os.ReadDir(approvalsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removals []approvalRemoval
	for _, entry := range entries {
		if remainingEntries != nil {
			if *remainingEntries <= 0 {
				return nil, ErrStateScanLimit
			}
			(*remainingEntries)--
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(approvalsDir, entry.Name())
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		metadata := approvalMetadata{}
		if err := json.Unmarshal(raw, &metadata); err != nil {
			continue
		}
		terminal := metadata.Status == "denied" || metadata.Status == "consumed" || metadata.Status == "expired"
		expiresAt := parseStateTime(metadata.ExpiresAt)
		if !terminal && !expiresAt.IsZero() && !expiresAt.After(options.Now) {
			terminal = true
		}
		if !terminal {
			continue
		}
		terminalAt := firstStateTime(metadata.ConsumedAt, metadata.DeniedAt, metadata.ExpiresAt, metadata.Created)
		if terminalAt.IsZero() || options.Now.Sub(terminalAt) <= options.ApprovalMaxAge {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil, infoErr
		}
		removals = append(removals, approvalRemoval{Path: path, Bytes: info.Size()})
	}
	return removals, nil
}

func firstStateTime(values ...string) time.Time {
	for _, value := range values {
		if parsed := parseStateTime(value); !parsed.IsZero() {
			return parsed
		}
	}
	return time.Time{}
}

func parseStateTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func staleIndex(path string, options StateGCOptions) (bool, int64, string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, 0, "", nil
	}
	if err != nil {
		return false, 0, "", err
	}
	if !info.Mode().IsRegular() {
		return false, 0, "", nil
	}
	metadata := indexMetadata{}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		return false, 0, "", readErr
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		// Unknown or corrupt state is preserved rather than guessed about.
		return false, 0, "", nil
	}
	timestamp := metadata.TS
	if timestamp.IsZero() {
		timestamp = info.ModTime()
	}
	if strings.TrimSpace(metadata.Root) != "" {
		if _, rootErr := os.Stat(metadata.Root); errors.Is(rootErr, os.ErrNotExist) {
			return true, info.Size(), "workspace root no longer exists", nil
		}
	}
	if options.Now.Sub(timestamp) > options.CacheMaxAge {
		return true, info.Size(), "repository index cache expired", nil
	}
	return false, 0, "", nil
}

func workspaceEmptyAfterRemovals(workspaceDir string, removed map[string]struct{}, remainingEntries *int) (bool, int64, error) {
	remaining := false
	var bytes int64
	err := filepath.WalkDir(workspaceDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if remainingEntries != nil {
			if *remainingEntries <= 0 {
				return ErrStateScanLimit
			}
			(*remainingEntries)--
		}
		if path == workspaceDir {
			return nil
		}
		clean := filepath.Clean(path)
		for removedPath := range removed {
			if clean == removedPath || isWithin(clean, removedPath) {
				if entry.IsDir() && clean == removedPath {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if entry.IsDir() {
			return nil
		}
		remaining = true
		if info, infoErr := entry.Info(); infoErr == nil {
			bytes += info.Size()
		}
		return nil
	})
	return !remaining, bytes, err
}

func isWithin(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func removeIfEmpty(path string) error {
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

func uniqueExistingPaths(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			absolute = filepath.Clean(path)
		}
		absolute = filepath.Clean(absolute)
		if _, exists := seen[absolute]; exists {
			continue
		}
		seen[absolute] = struct{}{}
		out = append(out, absolute)
	}
	sort.Strings(out)
	return out
}

func addAction(report *StateGCReport, options StateGCOptions, action StateGCAction) {
	if len(report.Actions) >= options.ActionLimit {
		report.ActionsTruncated = true
		return
	}
	report.Actions = append(report.Actions, action)
}
