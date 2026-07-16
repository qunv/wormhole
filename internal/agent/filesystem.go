// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"codebridge/internal/patch"
)

func (r *Runtime) handleFS(_ context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "list_files":
		entries, err := r.Workspace.List(stringArg(args, "path", "."), boolArg(args, "recursive", false), intArg(args, "limit", 200))
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": stringArg(args, "path", "."), "count": len(entries), "entries": entries}, nil
	case "read_file":
		return r.readOne(args)
	case "stat_path":
		target, err := r.Workspace.Resolve(stringArg(args, "path", ""))
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(target)
		if err != nil {
			return nil, err
		}
		kind := "other"
		if info.IsDir() {
			kind = "directory"
		} else if info.Mode().IsRegular() {
			kind = "file"
		}
		return map[string]any{
			"path": r.Workspace.Relative(target), "type": kind, "size": info.Size(),
			"modified": info.ModTime().UTC(), "mode": info.Mode().String(),
		}, nil
	case "search_text":
		matches, engine, err := r.Workspace.Search(
			stringArg(args, "path", "."), stringArg(args, "query", ""),
			boolArg(args, "regex", false), stringArg(args, "glob", ""),
			intArg(args, "context", 0), intArg(args, "limit", 100),
		)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"query": stringArg(args, "query", ""), "regex": boolArg(args, "regex", false),
			"engine": engine, "count": len(matches), "matches": matches,
		}, nil
	case "find_files":
		files, engine, err := r.Workspace.FindFiles(stringArg(args, "path", "."), stringArg(args, "glob", ""), intArg(args, "limit", 300))
		if err != nil {
			return nil, err
		}
		return map[string]any{"glob": stringArg(args, "glob", ""), "engine": engine, "count": len(files), "files": files}, nil
	case "read_many":
		return r.readMany(args)
	case "repo_overview":
		tree, dirs, files, err := r.Workspace.Tree(stringArg(args, "path", "."), intArg(args, "depth", 3), intArg(args, "max_entries", 800))
		if err != nil {
			return nil, err
		}
		var manifests []string
		for _, entry := range tree {
			if manifestNames[strings.ToLower(filepath.Base(entry))] {
				manifests = append(manifests, entry)
			}
		}
		return map[string]any{
			"root": stringArg(args, "path", "."), "depth": intArg(args, "depth", 3),
			"dirs": dirs, "files": files, "tree": tree, "manifests": manifests,
		}, nil
	case "write_file":
		target, err := r.Workspace.Resolve(stringArg(args, "path", ""))
		if err != nil {
			return nil, err
		}
		if err := r.Patches.Backup("write_file", []string{target}); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		content := stringArg(args, "content", "")
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "path": r.Workspace.Relative(target), "bytes": len(content)}, nil
	case "replace_in_file":
		target, err := r.Workspace.Resolve(stringArg(args, "path", ""))
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(target)
		if err != nil {
			return nil, err
		}
		oldText, newText := stringArg(args, "old_text", ""), stringArg(args, "new_text", "")
		if oldText == "" || !strings.Contains(string(raw), oldText) {
			return nil, fmt.Errorf("old_text not found in %s", r.Workspace.Relative(target))
		}
		if err := r.Patches.Backup("replace_in_file", []string{target}); err != nil {
			return nil, err
		}
		count := 1
		content := strings.Replace(string(raw), oldText, newText, 1)
		if boolArg(args, "replace_all", false) {
			count = strings.Count(string(raw), oldText)
			content = strings.ReplaceAll(string(raw), oldText, newText)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "path": r.Workspace.Relative(target), "replacements": count}, nil
	case "apply_patch", "preview_patch", "validate_patch":
		dryRun := name != "apply_patch"
		var result map[string]any
		var err error
		if diff := stringArg(args, "diff", ""); strings.TrimSpace(diff) != "" {
			result, err = r.Patches.ApplyDiff(diff, dryRun)
		} else {
			operations, decodeErr := patch.DecodeOperations(args["operations"])
			if decodeErr != nil {
				return nil, decodeErr
			}
			result, err = r.Patches.ApplyOperations(operations, dryRun)
		}
		if err != nil {
			return nil, err
		}
		if name == "validate_patch" {
			var conflicts []any
			for _, item := range result["files"].([]map[string]any) {
				if item["ok"] != true {
					conflicts = append(conflicts, map[string]any{"path": item["path"], "conflict": item["conflict"]})
				}
			}
			return map[string]any{"ok": len(conflicts) == 0, "conflicts": conflicts}, nil
		}
		return result, nil
	case "make_dir":
		target, err := r.Workspace.Resolve(stringArg(args, "path", ""))
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "path": r.Workspace.Relative(target)}, nil
	case "move_path":
		source, err := r.Workspace.Resolve(stringArg(args, "from", ""))
		if err != nil {
			return nil, err
		}
		if r.Workspace.IsRoot(source) {
			return nil, errors.New("refusing to move a configured root")
		}
		destination, err := r.Workspace.Resolve(stringArg(args, "to", ""))
		if err != nil {
			return nil, err
		}
		if err := r.Patches.Backup("move_path", []string{source, destination}); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return nil, err
		}
		if err := os.Rename(source, destination); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "from": r.Workspace.Relative(source), "to": r.Workspace.Relative(destination)}, nil
	case "delete_path":
		target, err := r.Workspace.Resolve(stringArg(args, "path", ""))
		if err != nil {
			return nil, err
		}
		if r.Workspace.IsRoot(target) {
			return nil, errors.New("refusing to delete a configured root")
		}
		info, err := os.Stat(target)
		if err != nil {
			return nil, err
		}
		if info.IsDir() && !boolArg(args, "recursive", false) {
			return nil, errors.New("path is a directory; pass recursive=true")
		}
		if err := r.Patches.Backup("delete_path", []string{target}); err != nil {
			return nil, err
		}
		if info.IsDir() {
			err = os.RemoveAll(target)
		} else {
			err = os.Remove(target)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "deleted": r.Workspace.Relative(target)}, nil
	case "undo_last_patch":
		return r.Patches.Undo()
	default:
		return nil, fmt.Errorf("unsupported filesystem tool: %s", name)
	}
}

func (r *Runtime) readOne(args map[string]any) (map[string]any, error) {
	target, err := r.Workspace.Resolve(stringArg(args, "path", ""))
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	content := string(raw)
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	maxChars := intArg(args, "max_chars", r.Config.ReadDefault)
	if start := intArg(args, "start_line", 0); start > 0 || intArg(args, "line_count", 0) > 0 {
		if start <= 0 {
			start = 1
		}
		from := min(start-1, len(lines))
		to := len(lines)
		if count := intArg(args, "line_count", 0); count > 0 {
			to = min(from+count, len(lines))
		}
		selected := strings.Join(lines[from:to], "\n")
		selected, truncated := capText(selected, maxChars)
		return map[string]any{
			"path": r.Workspace.Relative(target), "total_lines": len(lines), "start_line": start,
			"returned_lines": to - from, "content": selected, "truncated": truncated,
		}, nil
	}
	content, truncated := capText(content, maxChars)
	return map[string]any{
		"path": r.Workspace.Relative(target), "total_lines": len(lines), "chars": len(raw),
		"content": content, "truncated": truncated,
	}, nil
}

func (r *Runtime) readMany(args map[string]any) (any, error) {
	paths := stringsArg(args, "paths")
	requests := arrayArg(args, "requests")
	if len(paths) > 0 && len(requests) > 0 {
		return nil, errors.New("use either paths or requests, not both")
	}
	if len(requests) == 0 {
		for _, path := range paths {
			requests = append(requests, map[string]any{"path": path})
		}
	}
	if len(requests) == 0 || len(requests) > 100 {
		return nil, errors.New("provide 1-100 read requests")
	}
	concurrency := min(max(intArg(args, "concurrency", 8), 1), 16)
	defaultMax := intArg(args, "max_chars_per_file", 40_000)
	results := make([]map[string]any, len(requests))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				request, _ := requests[index].(map[string]any)
				if request == nil {
					results[index] = map[string]any{"error": "read request must be an object"}
					continue
				}
				if _, ok := request["max_chars"]; !ok {
					request["max_chars"] = defaultMax
				}
				value, err := r.readOne(request)
				if err != nil {
					results[index] = map[string]any{"path": stringArg(request, "path", ""), "error": err.Error()}
				} else {
					results[index] = value
				}
			}
		}()
	}
	for index := range requests {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	remaining, failed := r.Config.MaxBatchReadChars, 0
	batchTruncated := false
	for _, result := range results {
		if result["error"] != nil {
			failed++
			continue
		}
		content, _ := result["content"].(string)
		if len(content) > remaining {
			result["content"] = content[:max(remaining, 0)]
			result["truncated"], result["batch_truncated"] = true, true
			batchTruncated = true
		}
		remaining = max(0, remaining-len(content))
	}
	return map[string]any{
		"count": len(results), "failed": failed, "files": results,
		"chars_returned":  r.Config.MaxBatchReadChars - remaining,
		"max_batch_chars": r.Config.MaxBatchReadChars, "batch_truncated": batchTruncated,
	}, nil
}

var manifestNames = map[string]bool{
	"package.json": true, "go.mod": true, "cargo.toml": true, "pyproject.toml": true,
	"requirements.txt": true, "pom.xml": true, "build.gradle": true, "pubspec.yaml": true,
	"gemfile": true, "composer.json": true, "dockerfile": true, "makefile": true,
}

func decodeMap(value any) map[string]any {
	raw, _ := json.Marshal(value)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}
