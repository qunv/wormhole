// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package patch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	HadContent bool   `json:"had_content"`
	Kind       string `json:"kind"`
}

type backupBatch struct {
	ID       string       `json:"id"`
	TS       string       `json:"ts"`
	Tool     string       `json:"tool"`
	BatchDir string       `json:"batch_dir"`
	Files    []backupFile `json:"files"`
}

type Engine struct {
	Workspace *workspace.Manager
	Store     *state.Store
}

func (e *Engine) Backup(tool string, paths []string) error {
	batch := backupBatch{
		ID: fmt.Sprintf("%d", time.Now().UnixNano()), TS: time.Now().UTC().Format(time.RFC3339Nano),
		Tool: tool,
	}
	batch.BatchDir = filepath.Join(e.Store.BackupsDir, batch.ID)
	if err := os.MkdirAll(batch.BatchDir, 0o700); err != nil {
		return err
	}
	for i, candidate := range paths {
		target, err := e.Workspace.Resolve(candidate)
		if err != nil {
			continue
		}
		item := backupFile{Path: target, Kind: "missing"}
		info, err := os.Stat(target)
		if errors.Is(err, os.ErrNotExist) {
			batch.Files = append(batch.Files, item)
			continue
		}
		if err != nil {
			continue
		}
		item.HadContent = true
		item.BackupFile = filepath.Join(batch.BatchDir, fmt.Sprintf("%d-%s", i, filepath.Base(target)))
		if info.IsDir() {
			item.Kind = "directory"
			err = copyTree(target, item.BackupFile)
		} else {
			item.Kind = "file"
			err = copyFile(target, item.BackupFile, info.Mode())
		}
		if err != nil {
			return err
		}
		batch.Files = append(batch.Files, item)
	}
	var history []backupBatch
	_ = e.Store.ReadJSON(e.Store.PatchHistory, &history)
	history = append(history, batch)
	if len(history) > 50 {
		for _, old := range history[:len(history)-50] {
			_ = os.RemoveAll(old.BatchDir)
		}
		history = history[len(history)-50:]
	}
	return e.Store.WriteJSON(e.Store.PatchHistory, history)
}

func (e *Engine) ApplyOperations(operations []Operation, dryRun bool) (map[string]any, error) {
	if len(operations) == 0 {
		return nil, errors.New("operations must not be empty")
	}
	if !dryRun {
		var paths []string
		for _, op := range operations {
			paths = append(paths, op.Path)
			if op.RenameTo != "" {
				paths = append(paths, op.RenameTo)
			}
		}
		if err := e.Backup("apply_patch_ops", paths); err != nil {
			return nil, err
		}
	}
	var results []map[string]any
	for _, op := range operations {
		result, err := e.applyOne(op, dryRun)
		if err != nil {
			results = append(results, map[string]any{"op": op.Op, "path": op.Path, "ok": false, "conflict": err.Error()})
			if !dryRun {
				break
			}
			continue
		}
		results = append(results, result)
	}
	ok := true
	applied := 0
	for _, result := range results {
		if valid, _ := result["ok"].(bool); valid {
			applied++
		} else {
			ok = false
		}
	}
	return map[string]any{"ok": ok, "mode": "operations", "applied": applied, "files": results, "results": results}, nil
}

func (e *Engine) applyOne(op Operation, dryRun bool) (map[string]any, error) {
	target, err := e.Workspace.Resolve(op.Path)
	if err != nil {
		return nil, err
	}
	switch op.Op {
	case "create":
		if !dryRun {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(target, []byte(op.Content), 0o644); err != nil {
				return nil, err
			}
		}
		return map[string]any{"op": op.Op, "path": e.Workspace.Relative(target), "ok": true, "bytes": len(op.Content)}, nil
	case "update":
		raw, err := os.ReadFile(target)
		if err != nil {
			return nil, err
		}
		content := string(raw)
		count := 0
		for _, edit := range op.Edits {
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
			if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
				return nil, err
			}
		}
		return map[string]any{"op": op.Op, "path": e.Workspace.Relative(target), "ok": true, "replacements": count}, nil
	case "delete":
		if e.Workspace.IsRoot(target) {
			return nil, errors.New("refusing to delete a configured root")
		}
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
		if e.Workspace.IsRoot(target) {
			return nil, errors.New("refusing to rename a configured root")
		}
		if op.RenameTo == "" {
			return nil, errors.New("rename requires rename_to")
		}
		dst, err := e.Workspace.Resolve(op.RenameTo)
		if err != nil {
			return nil, err
		}
		if !dryRun {
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return nil, err
			}
			if err := os.Rename(target, dst); err != nil {
				return nil, err
			}
		}
		return map[string]any{"op": op.Op, "path": e.Workspace.Relative(target), "to": e.Workspace.Relative(dst), "ok": true}, nil
	default:
		return nil, fmt.Errorf("unknown operation: %s", op.Op)
	}
}

type diffFile struct {
	Minus string
	Plus  string
	Hunks []diffHunk
}
type diffHunk struct{ Before, After []string }

func ParseUnifiedDiff(text string) ([]diffFile, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var files []diffFile
	var current *diffFile
	var hunk *diffHunk
	clean := func(value string) string {
		value = strings.TrimSpace(strings.Trim(value, `"'`))
		value = strings.TrimPrefix(value, "a/")
		value = strings.TrimPrefix(value, "b/")
		return value
	}
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if strings.HasPrefix(line, "--- ") {
			next := ""
			if index+1 < len(lines) && strings.HasPrefix(lines[index+1], "+++ ") {
				next = clean(strings.TrimPrefix(lines[index+1], "+++ "))
				index++
			}
			files = append(files, diffFile{Minus: clean(strings.TrimPrefix(line, "--- ")), Plus: next})
			current = &files[len(files)-1]
			hunk = nil
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			current.Hunks = append(current.Hunks, diffHunk{})
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
		}
	}
	if len(files) == 0 {
		return nil, errors.New("no file sections found in diff (need ---/+++ headers)")
	}
	return files, nil
}

func (e *Engine) ApplyDiff(text string, dryRun bool) (map[string]any, error) {
	files, err := ParseUnifiedDiff(text)
	if err != nil {
		return nil, err
	}
	if !dryRun {
		var paths []string
		for _, file := range files {
			path := file.Minus
			if path == "/dev/null" {
				path = file.Plus
			}
			paths = append(paths, path)
		}
		if err := e.Backup("apply_patch_diff", paths); err != nil {
			return nil, err
		}
	}
	var results []map[string]any
	for _, file := range files {
		path := file.Minus
		action := "update"
		if path == "/dev/null" {
			path, action = file.Plus, "create"
		} else if file.Plus == "/dev/null" {
			action = "delete"
		}
		target, resolveErr := e.Workspace.Resolve(path)
		if resolveErr != nil {
			results = append(results, map[string]any{"path": path, "action": action, "ok": false, "conflict": resolveErr.Error()})
			continue
		}
		if action == "delete" {
			if e.Workspace.IsRoot(target) {
				results = append(results, map[string]any{"path": path, "action": action, "ok": false, "conflict": "refusing to delete a configured root"})
				continue
			}
			_, statErr := os.Stat(target)
			if statErr != nil {
				results = append(results, map[string]any{"path": path, "action": action, "ok": false, "conflict": "file not found"})
				continue
			}
			if !dryRun {
				_ = os.RemoveAll(target)
			}
			results = append(results, map[string]any{"path": path, "action": action, "ok": true})
			continue
		}
		if action == "create" {
			var lines []string
			for _, hunk := range file.Hunks {
				lines = append(lines, hunk.After...)
			}
			content := strings.Join(lines, "\n") + "\n"
			if !dryRun {
				_ = os.MkdirAll(filepath.Dir(target), 0o755)
				if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
					results = append(results, map[string]any{"path": path, "action": action, "ok": false, "conflict": err.Error()})
					continue
				}
			}
			results = append(results, map[string]any{"path": path, "action": action, "ok": true, "preview_chars": len(content)})
			continue
		}
		raw, readErr := os.ReadFile(target)
		if readErr != nil {
			results = append(results, map[string]any{"path": path, "action": action, "ok": false, "conflict": readErr.Error()})
			continue
		}
		content, matched := string(raw), true
		for _, hunk := range file.Hunks {
			before, after := strings.Join(hunk.Before, "\n"), strings.Join(hunk.After, "\n")
			if before == after {
				continue
			}
			if before == "" {
				if content != "" && !strings.HasSuffix(content, "\n") {
					content += "\n"
				}
				content += after
			} else if strings.Contains(content, before) {
				content = strings.Replace(content, before, after, 1)
			} else {
				matched = false
				break
			}
		}
		if matched && !dryRun {
			if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
				matched = false
			}
		}
		result := map[string]any{"path": path, "action": action, "ok": matched}
		if !matched {
			result["conflict"] = "one or more hunks did not match"
		}
		results = append(results, result)
	}
	ok, applied := true, 0
	for _, result := range results {
		if result["ok"] == true {
			applied++
		} else {
			ok = false
		}
	}
	return map[string]any{"ok": ok, "mode": "diff", "applied": applied, "files": results, "results": results}, nil
}

func (e *Engine) Undo() (map[string]any, error) {
	var history []backupBatch
	if err := e.Store.ReadJSON(e.Store.PatchHistory, &history); err != nil || len(history) == 0 {
		return nil, errors.New("no patch history to undo")
	}
	batch := history[len(history)-1]
	var restored []string
	var failures []map[string]string
	for _, item := range batch.Files {
		if !item.HadContent {
			if err := os.RemoveAll(item.Path); err != nil {
				failures = append(failures, map[string]string{"path": item.Path, "error": err.Error()})
			} else {
				restored = append(restored, item.Path)
			}
			continue
		}
		_ = os.RemoveAll(item.Path)
		var err error
		if item.Kind == "directory" {
			err = copyTree(item.BackupFile, item.Path)
		} else {
			err = copyFile(item.BackupFile, item.Path, 0o644)
		}
		if err != nil {
			failures = append(failures, map[string]string{"path": item.Path, "error": err.Error()})
		} else {
			restored = append(restored, item.Path)
		}
	}
	history = history[:len(history)-1]
	_ = e.Store.WriteJSON(e.Store.PatchHistory, history)
	return map[string]any{"ok": len(failures) == 0, "batch_id": batch.ID, "restored": restored, "errors": failures}, nil
}

func copyFile(src, dst string, mode fs.FileMode) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, raw, mode)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
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
