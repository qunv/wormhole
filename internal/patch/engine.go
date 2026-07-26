// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package patch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"codebridge/internal/state"
	"codebridge/internal/workspace"
)

type Edit struct {
	OldText    string `json:"old_text"`
	NewText    string `json:"new_text"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

type Operation struct {
	Op        string `json:"op"`
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	RenameTo  string `json:"rename_to,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
	Edits     []Edit `json:"edits,omitempty"`
}

type backupFile struct {
	Path       string `json:"path"`
	BackupFile string `json:"backup_file,omitempty"`
	LinkTarget string `json:"link_target,omitempty"`
	HadContent bool   `json:"had_content"`
	Kind       string `json:"kind"`
	Mode       uint32 `json:"mode,omitempty"`
}

type backupBatch struct {
	ID       string       `json:"id"`
	TS       string       `json:"ts"`
	Tool     string       `json:"tool"`
	BatchDir string       `json:"batch_dir"`
	Files    []backupFile `json:"files"`
}

type backupBudget struct {
	remainingBytes   int64
	remainingEntries int
}

type Engine struct {
	Workspace *workspace.Manager
	Store     *state.Store

	mu sync.Mutex
}

// Backup creates one undo batch without mutating workspace content.
func (e *Engine) Backup(tool string, paths []string) error {
	return e.BackupContext(context.Background(), tool, paths)
}

func (e *Engine) BackupContext(ctx context.Context, tool string, paths []string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.backupLocked(ctx, tool, paths)
	return err
}

// Transaction serializes a backup and mutation as one operation. The callback
// must not call another Engine method. A failed callback is rolled back using
// the exact batch created for this transaction.
func (e *Engine) Transaction(tool string, paths []string, mutate func() error) error {
	return e.TransactionContext(context.Background(), tool, paths, mutate)
}

func (e *Engine) TransactionContext(ctx context.Context, tool string, paths []string, mutate func() error) error {
	if mutate == nil {
		return errors.New("mutation callback is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	batch, err := e.backupLocked(ctx, tool, paths)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		_, rollbackErr := e.undoBatchLocked(context.Background(), batch.ID)
		if rollbackErr != nil {
			return fmt.Errorf("%s canceled: %v; rollback batch %s failed: %w", tool, err, batch.ID, rollbackErr)
		}
		return err
	}
	if err := mutate(); err != nil {
		_, rollbackErr := e.undoBatchLocked(context.Background(), batch.ID)
		if rollbackErr != nil {
			return fmt.Errorf("%s failed: %v; rollback batch %s failed: %w", tool, err, batch.ID, rollbackErr)
		}
		return fmt.Errorf("%s failed and was rolled back: %w", tool, err)
	}
	return nil
}

func (e *Engine) backupLocked(ctx context.Context, tool string, paths []string) (backupBatch, error) {
	if e == nil || e.Workspace == nil || e.Store == nil {
		return backupBatch{}, errors.New("patch engine is not initialized")
	}
	resolved, err := e.resolveBackupPaths(ctx, paths)
	if err != nil {
		return backupBatch{}, err
	}
	batch := backupBatch{
		ID: fmt.Sprintf("%d", time.Now().UnixNano()), TS: time.Now().UTC().Format(time.RFC3339Nano),
		Tool: tool,
	}
	batch.BatchDir = filepath.Join(e.Store.BackupsDir, batch.ID)
	if err := os.MkdirAll(batch.BatchDir, 0o700); err != nil {
		return backupBatch{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(batch.BatchDir)
			_ = removeDirIfEmpty(e.Store.BackupsDir)
			_ = removeDirIfEmpty(e.Store.WorkspaceDir)
		}
	}()

	budget := &backupBudget{
		remainingBytes: DefaultBackupBatchBytes, remainingEntries: DefaultBackupScanEntries,
	}
	for index, target := range resolved {
		if err := ctx.Err(); err != nil {
			return backupBatch{}, err
		}
		item, err := backupPathContextWithBudget(ctx, target, filepath.Join(batch.BatchDir, fmt.Sprintf("%d-%s", index, filepath.Base(target))), budget)
		if err != nil {
			return backupBatch{}, fmt.Errorf("backup %s: %w", e.Workspace.Relative(target), err)
		}
		batch.Files = append(batch.Files, item)
	}

	batchScanEntries := DefaultBackupScanEntries
	batchBytes, err := pathSize(batch.BatchDir, &batchScanEntries)
	if err != nil {
		return backupBatch{}, err
	}
	if batchBytes > DefaultBackupBatchBytes {
		return backupBatch{}, fmt.Errorf("backup batch is %d bytes; maximum is %d bytes", batchBytes, DefaultBackupBatchBytes)
	}

	history, err := e.readHistoryLocked()
	if err != nil {
		return backupBatch{}, err
	}
	history = append(history, batch)
	retentionOptions := normalizeBackupPruneOptions(BackupPruneOptions{
		Now: time.Now().UTC(), ProtectedBatchID: batch.ID,
	})
	retentionScanEntries := retentionOptions.MaxScanEntries
	retained, expired, err := selectBackupHistory(history, e.Store.BackupsDir, retentionOptions, &retentionScanEntries)
	if err != nil {
		return backupBatch{}, err
	}
	if len(retained) == 0 || retained[len(retained)-1].ID != batch.ID {
		return backupBatch{}, errors.New("new backup batch could not be retained within the configured quota")
	}
	if err := e.Store.WriteJSON(e.Store.PatchHistory, retained); err != nil {
		return backupBatch{}, err
	}
	cleanup = false
	for path := range expired {
		_ = os.RemoveAll(path)
	}
	removeUnreferencedBackupDirs(e.Store.BackupsDir, retained)
	return batch, nil
}

func (e *Engine) resolveBackupPaths(ctx context.Context, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, errors.New("at least one backup path is required")
	}
	seen := map[string]bool{}
	resolved := make([]string, 0, len(paths))
	for _, candidate := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.TrimSpace(candidate) == "" {
			return nil, errors.New("backup path must not be empty")
		}
		target, err := e.Workspace.Resolve(candidate)
		if err != nil {
			return nil, err
		}
		key := filepath.Clean(target)
		if seen[key] {
			continue
		}
		seen[key] = true
		resolved = append(resolved, target)
	}
	return resolved, nil
}

func backupPath(target, destination string) (backupFile, error) {
	return backupPathContext(context.Background(), target, destination)
}

func backupPathContext(ctx context.Context, target, destination string) (backupFile, error) {
	return backupPathContextWithBudget(ctx, target, destination, nil)
}

func backupPathContextWithBudget(ctx context.Context, target, destination string, budget *backupBudget) (backupFile, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return backupFile{}, err
	}
	item := backupFile{Path: target, Kind: "missing"}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return item, nil
	}
	if err != nil {
		return backupFile{}, err
	}
	item.HadContent = true
	item.Mode = uint32(info.Mode().Perm())
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		if err := consumeBackupEntry(budget); err != nil {
			return backupFile{}, err
		}
		item.Kind = "symlink"
		item.LinkTarget, err = os.Readlink(target)
	case info.IsDir():
		item.Kind = "directory"
		item.BackupFile = destination
		err = copyTreeBackupContext(ctx, target, destination, budget)
	case info.Mode().IsRegular():
		if err := consumeBackupEntry(budget); err != nil {
			return backupFile{}, err
		}
		item.Kind = "file"
		item.BackupFile = destination
		err = copyBackupFileContext(ctx, target, destination, info.Mode(), budget)
	default:
		err = fmt.Errorf("unsupported file type %s", info.Mode())
	}
	return item, err
}

func consumeBackupEntry(budget *backupBudget) error {
	if budget == nil {
		return nil
	}
	if budget.remainingEntries <= 0 {
		return ErrBackupScanLimit
	}
	budget.remainingEntries--
	return nil
}

func copyBackupFileContext(ctx context.Context, source, destination string, mode fs.FileMode, budget *backupBudget) error {
	if budget == nil {
		return copyFileContext(ctx, source, destination, mode)
	}
	written, err := copyFileContextLimited(ctx, source, destination, mode, budget.remainingBytes)
	if err != nil {
		return err
	}
	budget.remainingBytes -= written
	return nil
}

func copyTreeBackupContext(ctx context.Context, source, destination string, budget *backupBudget) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if err := consumeBackupEntry(budget); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.IsDir():
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyBackupFileContext(ctx, path, target, info.Mode(), budget)
		default:
			return fmt.Errorf("unsupported file type %s", info.Mode())
		}
	})
}

func (e *Engine) ApplyOperations(operations []Operation, dryRun bool) (map[string]any, error) {
	return e.ApplyOperationsContext(context.Background(), operations, dryRun)
}

func (e *Engine) ApplyOperationsContext(ctx context.Context, operations []Operation, dryRun bool) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(operations) == 0 {
		return nil, errors.New("operations must not be empty")
	}
	if err := e.validateOperations(ctx, operations); err != nil {
		return nil, err
	}
	var batch backupBatch
	if !dryRun {
		paths := make([]string, 0, len(operations)*2)
		for _, op := range operations {
			paths = append(paths, op.Path)
			if op.RenameTo != "" {
				paths = append(paths, op.RenameTo)
			}
		}
		var err error
		batch, err = e.backupLocked(ctx, "apply_patch_ops", paths)
		if err != nil {
			return nil, err
		}
	}

	results := make([]map[string]any, 0, len(operations))
	for _, op := range operations {
		if err := ctx.Err(); err != nil {
			if dryRun {
				return nil, err
			}
			rollback, rollbackErr := e.undoBatchLocked(context.Background(), batch.ID)
			if rollbackErr != nil {
				return nil, fmt.Errorf("apply canceled: %v; rollback batch %s failed: %w", err, batch.ID, rollbackErr)
			}
			return failedPatchResult("operations", results, rollback), err
		}
		result, err := e.applyOne(ctx, op, dryRun)
		if err == nil {
			results = append(results, result)
			continue
		}
		results = append(results, map[string]any{"op": op.Op, "path": op.Path, "ok": false, "conflict": err.Error()})
		if dryRun {
			continue
		}
		rollback, rollbackErr := e.undoBatchLocked(context.Background(), batch.ID)
		if rollbackErr != nil {
			return nil, fmt.Errorf("apply failed: %v; rollback batch %s failed: %w", err, batch.ID, rollbackErr)
		}
		return failedPatchResult("operations", results, rollback), nil
	}
	return patchResult("operations", results), nil
}

func (e *Engine) validateOperations(ctx context.Context, operations []Operation) error {
	for index, op := range operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.TrimSpace(op.Path) == "" {
			return fmt.Errorf("operations[%d].path is required", index)
		}
		target, err := e.Workspace.Resolve(op.Path)
		if err != nil {
			return fmt.Errorf("operations[%d]: %w", index, err)
		}
		switch op.Op {
		case "create", "update":
		case "delete":
			if e.Workspace.IsRoot(target) {
				return errors.New("refusing to delete a configured root")
			}
		case "rename":
			if e.Workspace.IsRoot(target) {
				return errors.New("refusing to rename a configured root")
			}
			if strings.TrimSpace(op.RenameTo) == "" {
				return errors.New("rename requires rename_to")
			}
			if _, err := e.Workspace.Resolve(op.RenameTo); err != nil {
				return fmt.Errorf("operations[%d].rename_to: %w", index, err)
			}
		default:
			return fmt.Errorf("unknown operation: %s", op.Op)
		}
	}
	return nil
}

func (e *Engine) applyOne(ctx context.Context, op Operation, dryRun bool) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := e.Workspace.Resolve(op.Path)
	if err != nil {
		return nil, err
	}
	switch op.Op {
	case "create":
		if !dryRun {
			if err := WriteFileAtomic(target, []byte(op.Content), 0o644); err != nil {
				return nil, err
			}
		}
		return map[string]any{"op": op.Op, "path": e.Workspace.Relative(target), "ok": true, "bytes": len(op.Content)}, nil
	case "update":
		raw, err := readFileContext(ctx, target)
		if err != nil {
			return nil, err
		}
		content := string(raw)
		count := 0
		for _, edit := range op.Edits {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if edit.OldText == "" || !strings.Contains(content, edit.OldText) {
				return nil, fmt.Errorf("old_text not found in %s", op.Path)
			}
			if edit.ReplaceAll {
				count += strings.Count(content, edit.OldText)
				content = strings.ReplaceAll(content, edit.OldText, edit.NewText)
			} else {
				count++
				content = strings.Replace(content, edit.OldText, edit.NewText, 1)
			}
		}
		if !dryRun {
			if err := WriteFileAtomic(target, []byte(content), 0o644); err != nil {
				return nil, err
			}
		}
		return map[string]any{"op": op.Op, "path": e.Workspace.Relative(target), "ok": true, "replacements": count}, nil
	case "delete":
		info, err := os.Stat(target)
		if err != nil {
			return nil, err
		}
		if info.IsDir() && !op.Recursive {
			return nil, errors.New("path is a directory; pass recursive=true")
		}
		if !dryRun {
			if err := os.RemoveAll(target); err != nil {
				return nil, err
			}
		}
		return map[string]any{"op": op.Op, "path": e.Workspace.Relative(target), "ok": true}, nil
	case "rename":
		destination, err := e.Workspace.Resolve(op.RenameTo)
		if err != nil {
			return nil, err
		}
		if !dryRun {
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				return nil, err
			}
			if err := os.Rename(target, destination); err != nil {
				return nil, err
			}
		}
		return map[string]any{"op": op.Op, "path": e.Workspace.Relative(target), "to": e.Workspace.Relative(destination), "ok": true}, nil
	default:
		return nil, fmt.Errorf("unknown operation: %s", op.Op)
	}
}

type diffFile struct {
	Minus string
	Plus  string
	Hunks []diffHunk
}

type diffHunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Before   []string
	After    []string
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func ParseUnifiedDiff(text string) ([]diffFile, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var files []diffFile
	var current *diffFile
	var hunk *diffHunk
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if strings.HasPrefix(line, "--- ") {
			if index+1 >= len(lines) || !strings.HasPrefix(lines[index+1], "+++ ") {
				return nil, errors.New("diff file header requires consecutive --- and +++ lines")
			}
			files = append(files, diffFile{
				Minus: cleanDiffPath(strings.TrimPrefix(line, "--- ")),
				Plus:  cleanDiffPath(strings.TrimPrefix(lines[index+1], "+++ ")),
			})
			current = &files[len(files)-1]
			hunk = nil
			index++
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			parsed, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			current.Hunks = append(current.Hunks, parsed)
			hunk = &current.Hunks[len(current.Hunks)-1]
			continue
		}
		if hunk == nil || line == `\ No newline at end of file` || line == "" {
			continue
		}
		switch line[0] {
		case ' ':
			hunk.Before = append(hunk.Before, line[1:])
			hunk.After = append(hunk.After, line[1:])
		case '-':
			hunk.Before = append(hunk.Before, line[1:])
		case '+':
			hunk.After = append(hunk.After, line[1:])
		default:
			return nil, fmt.Errorf("invalid diff hunk line %q", line)
		}
		if len(hunk.Before) > hunk.OldCount || len(hunk.After) > hunk.NewCount {
			return nil, fmt.Errorf("hunk count mismatch for %s", diffPath(*current))
		}
		if len(hunk.Before) == hunk.OldCount && len(hunk.After) == hunk.NewCount {
			hunk = nil
		}
	}
	if len(files) == 0 {
		return nil, errors.New("no file sections found in diff (need ---/+++ headers)")
	}
	for _, file := range files {
		for _, hunk := range file.Hunks {
			if len(hunk.Before) != hunk.OldCount || len(hunk.After) != hunk.NewCount {
				return nil, fmt.Errorf("hunk count mismatch for %s", diffPath(file))
			}
		}
	}
	return files, nil
}

func parseHunkHeader(line string) (diffHunk, error) {
	match := hunkHeader.FindStringSubmatch(line)
	if len(match) == 0 {
		return diffHunk{}, fmt.Errorf("invalid unified diff hunk header %q", line)
	}
	oldStart, _ := strconv.Atoi(match[1])
	newStart, _ := strconv.Atoi(match[3])
	oldCount, newCount := 1, 1
	if match[2] != "" {
		oldCount, _ = strconv.Atoi(match[2])
	}
	if match[4] != "" {
		newCount, _ = strconv.Atoi(match[4])
	}
	return diffHunk{OldStart: oldStart, OldCount: oldCount, NewStart: newStart, NewCount: newCount}, nil
}

func cleanDiffPath(value string) string {
	value = strings.TrimSpace(strings.Trim(value, `"'`))
	value = strings.TrimPrefix(value, "a/")
	value = strings.TrimPrefix(value, "b/")
	return value
}

func diffPath(file diffFile) string {
	if file.Minus == "/dev/null" {
		return file.Plus
	}
	return file.Minus
}

func (e *Engine) ApplyDiff(text string, dryRun bool) (map[string]any, error) {
	return e.ApplyDiffContext(context.Background(), text, dryRun)
}

func (e *Engine) ApplyDiffContext(ctx context.Context, text string, dryRun bool) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, err := ParseUnifiedDiff(text)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	paths, err := e.validateDiffPaths(ctx, files)
	if err != nil {
		return nil, err
	}
	var batch backupBatch
	if !dryRun {
		batch, err = e.backupLocked(ctx, "apply_patch_diff", paths)
		if err != nil {
			return nil, err
		}
	}

	results := make([]map[string]any, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			if dryRun {
				return nil, err
			}
			rollback, rollbackErr := e.undoBatchLocked(context.Background(), batch.ID)
			if rollbackErr != nil {
				return nil, fmt.Errorf("patch canceled: %v; rollback batch %s failed: %w", err, batch.ID, rollbackErr)
			}
			return failedPatchResult("diff", results, rollback), err
		}
		result, applyErr := e.applyDiffFile(ctx, file, dryRun)
		if applyErr == nil {
			results = append(results, result)
			continue
		}
		results = append(results, map[string]any{
			"path": diffPath(file), "action": diffAction(file), "ok": false, "conflict": applyErr.Error(),
		})
		if dryRun {
			continue
		}
		rollback, rollbackErr := e.undoBatchLocked(context.Background(), batch.ID)
		if rollbackErr != nil {
			return nil, fmt.Errorf("patch failed: %v; rollback batch %s failed: %w", applyErr, batch.ID, rollbackErr)
		}
		return failedPatchResult("diff", results, rollback), nil
	}
	return patchResult("diff", results), nil
}

func (e *Engine) validateDiffPaths(ctx context.Context, files []diffFile) ([]string, error) {
	paths := make([]string, 0, len(files)*2)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if file.Minus == "" || file.Plus == "" {
			return nil, errors.New("diff paths must not be empty")
		}
		if file.Minus != "/dev/null" {
			target, err := e.Workspace.Resolve(file.Minus)
			if err != nil {
				return nil, err
			}
			if file.Plus == "/dev/null" && e.Workspace.IsRoot(target) {
				return nil, errors.New("refusing to delete a configured root")
			}
			paths = append(paths, file.Minus)
		}
		if file.Plus != "/dev/null" {
			if _, err := e.Workspace.Resolve(file.Plus); err != nil {
				return nil, err
			}
			paths = append(paths, file.Plus)
		}
	}
	return paths, nil
}

func diffAction(file diffFile) string {
	switch {
	case file.Minus == "/dev/null":
		return "create"
	case file.Plus == "/dev/null":
		return "delete"
	case file.Minus != file.Plus:
		return "rename"
	default:
		return "update"
	}
}

func (e *Engine) applyDiffFile(ctx context.Context, file diffFile, dryRun bool) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	action := diffAction(file)
	switch action {
	case "create":
		target, err := e.Workspace.Resolve(file.Plus)
		if err != nil {
			return nil, err
		}
		content, err := applyHunksContext(ctx, "", file.Hunks, true)
		if err != nil {
			return nil, err
		}
		if !dryRun {
			if err := WriteFileAtomic(target, []byte(content), 0o644); err != nil {
				return nil, err
			}
		}
		return map[string]any{"path": file.Plus, "action": action, "ok": true, "preview_chars": len(content)}, nil
	case "delete":
		target, err := e.Workspace.Resolve(file.Minus)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(target); err != nil {
			return nil, err
		}
		if !dryRun {
			if err := os.RemoveAll(target); err != nil {
				return nil, err
			}
		}
		return map[string]any{"path": file.Minus, "action": action, "ok": true}, nil
	case "update", "rename":
		source, err := e.Workspace.Resolve(file.Minus)
		if err != nil {
			return nil, err
		}
		sourceInfo, err := os.Stat(source)
		if err != nil {
			return nil, err
		}
		if !sourceInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("path is not a regular file: %s", file.Minus)
		}
		raw, err := readFileContext(ctx, source)
		if err != nil {
			return nil, err
		}
		content, err := applyHunksContext(ctx, string(raw), file.Hunks, false)
		if err != nil {
			return nil, err
		}
		destination := source
		if action == "rename" {
			destination, err = e.Workspace.Resolve(file.Plus)
			if err != nil {
				return nil, err
			}
		}
		if !dryRun {
			if action == "rename" {
				err = atomicWriteFile(destination, []byte(content), sourceInfo.Mode().Perm())
			} else {
				err = WriteFileAtomic(destination, []byte(content), 0o644)
			}
			if err != nil {
				return nil, err
			}
			if action == "rename" {
				if err := os.RemoveAll(source); err != nil {
					return nil, err
				}
			}
		}
		result := map[string]any{"path": file.Minus, "action": action, "ok": true}
		if action == "rename" {
			result["to"] = file.Plus
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported diff action %s", action)
	}
}

func applyHunks(content string, hunks []diffHunk, create bool) (string, error) {
	return applyHunksContext(context.Background(), content, hunks, create)
}

func applyHunksContext(ctx context.Context, content string, hunks []diffHunk, create bool) (string, error) {
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	hadFinalNewline := strings.HasSuffix(normalized, "\n")
	if hadFinalNewline {
		normalized = strings.TrimSuffix(normalized, "\n")
	}
	var lines []string
	if normalized != "" {
		lines = strings.Split(normalized, "\n")
	}
	offset := 0
	for _, hunk := range hunks {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		index := hunk.OldStart - 1
		if hunk.OldCount == 0 {
			index = hunk.OldStart
		}
		if hunk.OldStart == 0 {
			index = 0
		}
		index += offset
		if index < 0 || index+len(hunk.Before) > len(lines) {
			return "", fmt.Errorf("hunk at old line %d is outside the file", hunk.OldStart)
		}
		for lineIndex, expected := range hunk.Before {
			if lines[index+lineIndex] != expected {
				return "", fmt.Errorf("hunk at old line %d did not match", hunk.OldStart)
			}
		}
		replacement := append([]string(nil), hunk.After...)
		updated := make([]string, 0, len(lines)-len(hunk.Before)+len(replacement))
		updated = append(updated, lines[:index]...)
		updated = append(updated, replacement...)
		updated = append(updated, lines[index+len(hunk.Before):]...)
		lines = updated
		offset += len(replacement) - len(hunk.Before)
	}
	result := strings.Join(lines, newline)
	if len(lines) > 0 && (create || hadFinalNewline) {
		result += newline
	}
	return result, nil
}

func patchResult(mode string, results []map[string]any) map[string]any {
	ok, applied := true, 0
	for _, result := range results {
		if result["ok"] == true {
			applied++
		} else {
			ok = false
		}
	}
	return map[string]any{"ok": ok, "mode": mode, "applied": applied, "files": results, "results": results}
}

func failedPatchResult(mode string, results []map[string]any, rollback map[string]any) map[string]any {
	return map[string]any{
		"ok": false, "mode": mode, "applied": 0, "rolled_back": true,
		"rollback": rollback, "files": results, "results": results,
	}
}

func (e *Engine) Undo() (map[string]any, error) {
	return e.UndoContext(context.Background())
}

func (e *Engine) UndoContext(ctx context.Context) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	history, err := e.readHistoryLocked()
	if err != nil || len(history) == 0 {
		return nil, errors.New("no patch history to undo")
	}
	return e.undoBatchLocked(ctx, history[len(history)-1].ID)
}

func (e *Engine) undoBatchLocked(ctx context.Context, batchID string) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	history, err := e.readHistoryLocked()
	if err != nil {
		return nil, err
	}
	index := -1
	for current := range history {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if history[current].ID == batchID {
			index = current
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("patch backup batch %s was not found", batchID)
	}
	batch := history[index]
	restored, failures := restoreBatchContext(ctx, batch)
	if len(failures) > 0 {
		return map[string]any{"ok": false, "batch_id": batch.ID, "restored": restored, "errors": failures}, errors.New("one or more backup paths could not be restored")
	}
	history = append(history[:index], history[index+1:]...)
	if err := e.Store.WriteJSON(e.Store.PatchHistory, history); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(batch.BatchDir); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "batch_id": batch.ID, "restored": restored, "errors": failures}, nil
}

func (e *Engine) readHistoryLocked() ([]backupBatch, error) {
	var history []backupBatch
	if err := e.Store.ReadJSON(e.Store.PatchHistory, &history); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return history, nil
}

func restoreBatch(batch backupBatch) ([]string, []map[string]string) {
	return restoreBatchContext(context.Background(), batch)
}

func restoreBatchContext(ctx context.Context, batch backupBatch) ([]string, []map[string]string) {
	var restored []string
	var failures []map[string]string
	for _, item := range batch.Files {
		if err := ctx.Err(); err != nil {
			failures = append(failures, map[string]string{"path": item.Path, "error": err.Error()})
			break
		}
		if err := restoreBackupFileContext(ctx, item); err != nil {
			failures = append(failures, map[string]string{"path": item.Path, "error": err.Error()})
		} else {
			restored = append(restored, item.Path)
		}
	}
	return restored, failures
}

func restoreBackupFile(item backupFile) error {
	return restoreBackupFileContext(context.Background(), item)
}

func restoreBackupFileContext(ctx context.Context, item backupFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !item.HadContent {
		return os.RemoveAll(item.Path)
	}
	if err := os.RemoveAll(item.Path); err != nil {
		return err
	}
	switch item.Kind {
	case "directory":
		return copyTreeContext(ctx, item.BackupFile, item.Path)
	case "file":
		mode := fs.FileMode(item.Mode)
		if mode == 0 {
			mode = 0o644
		}
		return copyFileContext(ctx, item.BackupFile, item.Path, mode)
	case "symlink":
		if err := os.MkdirAll(filepath.Dir(item.Path), 0o755); err != nil {
			return err
		}
		return os.Symlink(item.LinkTarget, item.Path)
	default:
		return fmt.Errorf("unsupported backup kind %q", item.Kind)
	}
}

// WriteFileAtomic replaces a regular file through a temporary sibling and
// preserves the existing permission bits. New files use defaultMode.
func WriteFileAtomic(path string, data []byte, defaultMode fs.FileMode) error {
	mode := defaultMode
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("path is not a regular file: %s", path)
		}
		mode = info.Mode().Perm()
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	return atomicWriteFile(path, data, mode)
}

func atomicWriteFile(path string, data []byte, mode fs.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".codebridge-write-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func readFileContext(ctx context.Context, path string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(contextReader{ctx: ctx, reader: file})
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func copyFile(src, dst string, mode fs.FileMode) error {
	return copyFileContext(context.Background(), src, dst, mode)
}

func copyFileContext(ctx context.Context, src, dst string, mode fs.FileMode) error {
	_, err := copyFileContextLimited(ctx, src, dst, mode, -1)
	return err
}

func copyFileContextLimited(ctx context.Context, src, dst string, mode fs.FileMode, maxBytes int64) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	input, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	output, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return 0, err
	}
	ok := false
	defer func() {
		_ = output.Close()
		if !ok {
			_ = os.Remove(dst)
		}
	}()
	reader := io.Reader(contextReader{ctx: ctx, reader: input})
	if maxBytes >= 0 {
		reader = io.LimitReader(reader, maxBytes+1)
	}
	written, err := io.Copy(output, reader)
	if err != nil {
		return written, err
	}
	if maxBytes >= 0 && written > maxBytes {
		return written, fmt.Errorf("backup data exceeds remaining quota of %d bytes", maxBytes)
	}
	if err := output.Sync(); err != nil {
		return written, err
	}
	if err := output.Close(); err != nil {
		return written, err
	}
	ok = true
	return written, nil
}

func copyTree(src, dst string) error {
	return copyTreeContext(context.Background(), src, dst)
}

func copyTreeContext(ctx context.Context, src, dst string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, relative)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.IsDir():
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyFileContext(ctx, path, target, info.Mode())
		default:
			return fmt.Errorf("unsupported file type %s", info.Mode())
		}
	})
}

func DecodeOperations(value any) ([]Operation, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var operations []Operation
	if err := json.Unmarshal(raw, &operations); err != nil {
		return nil, err
	}
	return operations, nil
}
