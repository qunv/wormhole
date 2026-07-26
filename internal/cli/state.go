// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"codebridge/internal/config"
	"codebridge/internal/maintenance"
	"codebridge/internal/workspaceregistry"
)

func (a App) stateCommand(cfg config.Config, opts options) error {
	if len(opts.Rest) == 0 || opts.Rest[0] != "gc" {
		return errors.New("usage: codebridge state gc [--dry-run] [--json] [--force]")
	}
	if !opts.DryRun && readHealth(cfg.Port) != nil && !opts.Force {
		return errors.New("Codebridge is running; use --dry-run, stop it first, or pass --force")
	}
	report, err := maintenance.GCState(stateDataDirs(), maintenance.StateGCOptions{DryRun: opts.DryRun})
	if err != nil {
		return err
	}
	if opts.JSON {
		raw, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Stdout, string(raw))
		return nil
	}
	mode := "removed"
	if opts.DryRun {
		mode = "would remove"
	}
	fmt.Fprintf(a.Stdout, "State GC %s: scanned=%d workspaces=%d cache_files=%d backup_dirs=%d orphan_backups=%d approvals=%d bytes=%d\n",
		mode, report.ScannedWorkspaceDirs, report.RemovedWorkspaceDirs, report.RemovedCacheFiles,
		report.RemovedBackupDirs, report.RemovedOrphanBackups, report.RemovedApprovalFiles, report.BytesFreed,
	)
	for _, action := range report.Actions {
		fmt.Fprintf(a.Stdout, "  %-9s %s (%s)\n", action.Kind, action.Path, action.Reason)
	}
	if report.ActionsTruncated {
		fmt.Fprintln(a.Stdout, "  ... action list truncated")
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(a.Stderr, "WARN state gc: %s\n", warning)
	}
	return nil
}

func (a App) startupStateGC(reporter func(stage, message string)) {
	report, err := maintenance.GCState(stateDataDirs(), maintenance.StateGCOptions{
		MaxWorkspaceDirs: maintenance.DefaultStartupScanLimit,
	})
	if err != nil {
		reporter("state", "gc failed: "+err.Error())
		return
	}
	if len(report.Warnings) > 0 {
		reporter("state", fmt.Sprintf("gc completed with %d warning(s)", len(report.Warnings)))
	}
	if report.RemovedWorkspaceDirs+report.RemovedCacheFiles+report.RemovedBackupDirs+report.RemovedApprovalFiles > 0 {
		reporter("state", fmt.Sprintf(
			"gc scanned=%d removed_workspaces=%d cache_files=%d backup_dirs=%d approvals=%d bytes=%d",
			report.ScannedWorkspaceDirs, report.RemovedWorkspaceDirs, report.RemovedCacheFiles,
			report.RemovedBackupDirs, report.RemovedApprovalFiles, report.BytesFreed,
		))
	}
}

func stateDataDirs() []string {
	paths := []string{config.AppDataDir()}
	instancesRoot := filepath.Join(config.AppDataDir(), "instances")
	if entries, err := os.ReadDir(instancesRoot); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				paths = append(paths, filepath.Join(instancesRoot, entry.Name()))
			}
		}
	}
	registry, err := workspaceregistry.Load()
	if err == nil {
		for _, id := range workspaceregistry.SortedIDs(registry) {
			if dataDir := strings.TrimSpace(registry.Workspaces[id].DataDir); dataDir != "" {
				paths = append(paths, dataDir)
			}
		}
	}
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			absolute = filepath.Clean(path)
		}
		absolute = filepath.Clean(absolute)
		if _, exists := seen[absolute]; exists {
			continue
		}
		seen[absolute] = struct{}{}
		unique = append(unique, absolute)
	}
	sort.Strings(unique)
	return unique
}
