// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wormhole/internal/processx"
	"wormhole/internal/security"
)

func (r *Runtime) handleExec(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "run_command":
		return r.runCommand(ctx, args)
	case "run_commands":
		return r.runCommands(ctx, args)
	case "proc_start":
		if err := security.ArbitraryShellAllowed(r.Config.Mode); err != nil {
			return nil, err
		}
		command := stringArg(args, "command", "")
		if err := security.CommandAllowed(command, r.Config.Mode, false); err != nil {
			return nil, err
		}
		cwd, err := r.Workspace.Resolve(stringArg(args, "cwd", "."))
		if err != nil {
			return nil, err
		}
		proc, err := r.Processes.Start(command, cwd, stringArg(args, "shell", ""), stringArg(args, "name", ""))
		if err != nil {
			return nil, err
		}
		r.invalidateRepositoryCaches()
		return map[string]any{
			"ok": true, "id": proc.ID, "name": proc.Name, "command": command,
			"cwd": cwd, "pid": proc.PID,
		}, nil
	case "proc_list":
		return map[string]any{"processes": r.Processes.List()}, nil
	case "proc_output":
		return r.Processes.Output(stringArg(args, "id", ""), intArg(args, "tail_chars", 0))
	case "proc_stop":
		id := stringArg(args, "id", "")
		if err := r.Processes.Stop(id); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "id": id, "status": "stopping"}, nil
	case "git":
		return r.rawGit(ctx, args)
	case "git_status":
		return r.gitStatusWithRefresh(ctx, stringArg(args, "cwd", "."), boolArg(args, "refresh", false))
	case "git_diff":
		return r.gitDiff(ctx, args)
	default:
		return nil, fmt.Errorf("unsupported execution tool: %s", name)
	}
}

func (r *Runtime) runCommand(ctx context.Context, args map[string]any) (any, error) {
	command := stringArg(args, "command", "")
	if command == "" {
		return nil, errors.New("command is required")
	}
	if !boolArg(args, "internal_quality_command", false) {
		if err := security.ArbitraryShellAllowed(r.Config.Mode); err != nil {
			return nil, err
		}
	}
	if err := security.CommandAllowed(command, r.Config.Mode, false); err != nil {
		return nil, err
	}
	cwd, err := r.Workspace.Resolve(stringArg(args, "cwd", "."))
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(intArg(args, "timeout_ms", 120_000)) * time.Millisecond
	maxChars := min(max(intArg(args, "max_output_chars", r.Config.CommandOutput), 1), r.Config.MaxCommandOutput)
	result := processx.Run(ctx, command, cwd, stringArg(args, "shell", ""), timeout, maxChars)
	if !boolArg(args, "defer_cache_invalidation", false) && commandMayMutateWorkspace(command) {
		r.invalidateRepositoryCaches()
	}
	stdout := processx.Trim(result.Stdout, intArg(args, "head_lines", 0), intArg(args, "tail_lines", 0), maxChars)
	stderr := processx.Trim(result.Stderr, intArg(args, "head_lines", 0), intArg(args, "tail_lines", 0), maxChars)
	return map[string]any{
		"cwd": cwd, "command": command, "shell": stringArg(args, "shell", ""),
		"exit_code": result.ExitCode, "timed_out": result.TimedOut,
		"stdout": stdout, "stderr": stderr,
		"stdout_truncated": result.StdoutTruncated || len(stdout) < len(result.Stdout),
		"stderr_truncated": result.StderrTruncated || len(stderr) < len(result.Stderr),
	}, nil
}

func (r *Runtime) runCommands(ctx context.Context, args map[string]any) (any, error) {
	items := arrayArg(args, "commands")
	if len(items) == 0 || len(items) > 12 {
		return nil, errors.New("commands must contain 1-12 entries")
	}
	results := make([]any, len(items))
	entries := make([]map[string]any, len(items))
	mayMutate := make([]bool, len(items))
	executedMutation := make([]bool, len(items))
	for index := range items {
		entry := decodeMap(items[index])
		entry["defer_cache_invalidation"] = true
		entries[index] = entry
		mayMutate[index] = commandMayMutateWorkspace(stringArg(entry, "command", ""))
	}
	runOne := func(index int) {
		entry := entries[index]
		if _, ok := entry["max_output_chars"]; !ok {
			entry["max_output_chars"] = 10_000
		}
		result, err := r.runCommand(ctx, entry)
		if err != nil {
			results[index] = map[string]any{"index": index, "command": stringArg(entry, "command", ""), "error": err.Error(), "exit_code": -1}
			return
		}
		value := result.(map[string]any)
		value["index"] = index
		results[index] = value
		executedMutation[index] = mayMutate[index]
	}
	parallel := boolArg(args, "parallel", false)
	completed := len(items)
	if parallel {
		limit := min(max(intArg(args, "max_concurrency", 4), 1), 4)
		jobs := make(chan int)
		var wg sync.WaitGroup
		for range limit {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for index := range jobs {
					runOne(index)
				}
			}()
		}
		for index := range items {
			jobs <- index
		}
		close(jobs)
		wg.Wait()
	} else {
		stopOnFailure := boolArg(args, "stop_on_failure", true)
		for index := range items {
			runOne(index)
			entry := results[index].(map[string]any)
			if stopOnFailure && entry["exit_code"] != 0 {
				completed = index + 1
				results = results[:completed]
				break
			}
		}
	}
	ok := completed == len(items)
	for _, item := range results {
		if item.(map[string]any)["exit_code"] != 0 {
			ok = false
		}
	}
	for index := range results {
		if executedMutation[index] {
			r.invalidateRepositoryCaches()
			break
		}
	}
	return map[string]any{
		"ok": ok, "parallel": parallel, "requested": len(items), "completed": completed,
		"stopped_early": completed < len(items), "results": results,
	}, nil
}

func commandMayMutateWorkspace(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	// Shell composition, substitution, and redirection can turn an otherwise
	// read-only prefix into a mutating command. Fail closed for those forms.
	if strings.ContainsAny(command, "\r\n;&|><`") || strings.Contains(command, "$(") {
		return true
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	name := strings.ToLower(filepath.Base(fields[0]))
	args := fields[1:]
	switch name {
	case "pwd", "ls", "cat", "rg", "grep", "head", "tail", "wc":
		return false
	case "find":
		for _, arg := range args {
			switch strings.ToLower(arg) {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fls", "-fprint", "-fprintf":
				return true
			}
		}
		return false
	case "git":
		return !security.IsReadOnlyGit(args)
	case "go":
		return !goCommandReadOnly(args)
	default:
		return true
	}
}

func goCommandReadOnly(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch strings.ToLower(args[0]) {
	case "vet", "list", "version":
		return true
	case "env":
		return true
	case "test":
		for _, arg := range args[1:] {
			lower := strings.ToLower(arg)
			for _, outputFlag := range []string{
				"-c", "-o", "-coverprofile", "-trace", "-cpuprofile", "-memprofile",
				"-mutexprofile", "-blockprofile",
			} {
				if lower == outputFlag || strings.HasPrefix(lower, outputFlag+"=") {
					return false
				}
			}
		}
		return true
	default:
		return false
	}
}

func (r *Runtime) rawGit(ctx context.Context, args map[string]any) (any, error) {
	argv := stringsArg(args, "args")
	if len(argv) == 0 {
		return nil, errors.New("git args are required")
	}
	if security.BadGitFlag(argv) {
		return nil, errors.New("that git flag is blocked because it can escape the repo or run external programs")
	}
	if r.Config.Mode != "full" && !security.IsReadOnlyGit(argv) {
		return nil, errors.New("mutating git commands are blocked in safe mode")
	}
	cwd, err := r.Workspace.Resolve(stringArg(args, "cwd", "."))
	if err != nil {
		return nil, err
	}
	result := processx.Capture(ctx, "git", argv, cwd, 120*time.Second)
	if result.ExitCode == 0 && !security.IsReadOnlyGit(argv) {
		r.invalidateRepositoryCaches()
	}
	return map[string]any{
		"cwd": cwd, "args": argv, "exit_code": result.ExitCode,
		"timed_out": result.TimedOut, "stdout": result.Stdout, "stderr": result.Stderr,
	}, nil
}

func (r *Runtime) gitStatus(ctx context.Context, cwdArg string) (any, error) {
	return r.gitStatusWithRefresh(ctx, cwdArg, false)
}

func (r *Runtime) gitStatusWithRefresh(ctx context.Context, cwdArg string, refresh bool) (any, error) {
	cwd, err := r.Workspace.Resolve(cwdArg)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	generation := r.currentRepositoryGeneration()
	r.repoCacheMu.Lock()
	if cached, ok := r.gitStatusCache[cwd]; !refresh && ok && cached.Generation == generation && now.Sub(cached.GeneratedAt) < r.gitStatusCacheTTL() {
		r.repoCacheMu.Unlock()
		return gitStatusMap(cached, true), nil
	}
	r.repoCacheMu.Unlock()

	result := processx.Capture(ctx, "git", []string{"status", "--porcelain=v2", "--branch", "--untracked-files=all"}, cwd, 30*time.Second)
	snapshot := gitStatusSnapshot{GeneratedAt: now, Generation: generation, CWD: cwd}
	if result.ExitCode != 0 {
		snapshot.Error = firstLine(result.Stderr)
	} else {
		snapshot.IsGitRepo = true
		snapshot.Branch, snapshot.Files = parseGitStatusV2(result.Stdout)
		snapshot.RenderedFiles = renderGitStatusFiles(snapshot.Files)
		clean := len(snapshot.Files) == 0
		snapshot.Clean = &clean
	}
	r.repoCacheMu.Lock()
	if r.repoGeneration == generation {
		if r.gitStatusCache == nil {
			r.gitStatusCache = map[string]gitStatusSnapshot{}
		}
		if _, exists := r.gitStatusCache[cwd]; !exists && len(r.gitStatusCache) >= gitStatusCacheLimit {
			oldestCWD := ""
			var oldest time.Time
			for candidate, entry := range r.gitStatusCache {
				if oldestCWD == "" || entry.GeneratedAt.Before(oldest) {
					oldestCWD, oldest = candidate, entry.GeneratedAt
				}
			}
			delete(r.gitStatusCache, oldestCWD)
		}
		r.gitStatusCache[cwd] = snapshot
	}
	r.repoCacheMu.Unlock()
	return gitStatusMap(snapshot, false), nil
}

func parseGitStatusV2(output string) (string, []gitFileStatus) {
	branch := ""
	var files []gitFileStatus
	for _, line := range nonEmptyLines(output) {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			branch = strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
			if branch == "(detached)" {
				branch = "HEAD"
			}
		case strings.HasPrefix(line, "1 "):
			fields := strings.SplitN(line, " ", 9)
			if len(fields) == 9 {
				files = append(files, gitFileStatus{Index: gitStatusCode(fields[1], 0), Worktree: gitStatusCode(fields[1], 1), Path: fields[8]})
			}
		case strings.HasPrefix(line, "2 "):
			fields := strings.SplitN(line, " ", 10)
			if len(fields) == 10 {
				path, _, _ := strings.Cut(fields[9], "\t")
				files = append(files, gitFileStatus{Index: gitStatusCode(fields[1], 0), Worktree: gitStatusCode(fields[1], 1), Path: path})
			}
		case strings.HasPrefix(line, "u "):
			fields := strings.Fields(line)
			if len(fields) >= 11 {
				files = append(files, gitFileStatus{Index: gitStatusCode(fields[1], 0), Worktree: gitStatusCode(fields[1], 1), Path: strings.Join(fields[10:], " ")})
			}
		case strings.HasPrefix(line, "? "):
			files = append(files, gitFileStatus{Index: "?", Worktree: "?", Path: strings.TrimPrefix(line, "? ")})
		}
	}
	return branch, files
}

func gitStatusCode(value string, index int) string {
	if index < 0 || index >= len(value) || value[index] == '.' {
		return " "
	}
	return string(value[index])
}

func renderGitStatusFiles(files []gitFileStatus) []map[string]any {
	if len(files) == 0 {
		return nil
	}
	rendered := make([]map[string]any, 0, len(files))
	for _, file := range files {
		rendered = append(rendered, map[string]any{"index": file.Index, "worktree": file.Worktree, "path": file.Path})
	}
	return rendered
}

func gitStatusMap(snapshot gitStatusSnapshot, cached bool) map[string]any {
	if !snapshot.IsGitRepo {
		return map[string]any{
			"cwd": snapshot.CWD, "is_git_repo": false, "clean": nil,
			"error": snapshot.Error, "cached": cached,
		}
	}
	files := snapshot.RenderedFiles
	if files == nil && len(snapshot.Files) > 0 {
		files = renderGitStatusFiles(snapshot.Files)
	}
	return map[string]any{
		"cwd": snapshot.CWD, "is_git_repo": true, "branch": snapshot.Branch,
		"clean": snapshot.Clean != nil && *snapshot.Clean, "count": len(files), "files": files,
		"cached": cached, "generated_at": snapshot.GeneratedAt,
	}
}

func (r *Runtime) gitDiff(ctx context.Context, args map[string]any) (any, error) {
	cwd, err := r.Workspace.Resolve(stringArg(args, "cwd", "."))
	if err != nil {
		return nil, err
	}
	argv := []string{"diff"}
	if boolArg(args, "staged", false) {
		argv = append(argv, "--staged")
	}
	if rel := stringArg(args, "path", ""); rel != "" {
		target, err := r.Workspace.Resolve(rel)
		if err != nil {
			return nil, err
		}
		local, err := filepath.Rel(cwd, target)
		if err != nil || strings.HasPrefix(local, "..") {
			return nil, errors.New("diff path must be within the selected repo directory")
		}
		argv = append(argv, "--", local)
	}
	result := processx.Capture(ctx, "git", argv, cwd, 60*time.Second)
	if result.ExitCode != 0 {
		return map[string]any{
			"cwd": cwd, "is_git_repo": false, "error": firstLine(result.Stderr),
			"exit_code": result.ExitCode,
		}, nil
	}
	diff, truncated := capText(result.Stdout, r.Config.MaxCommandOutput)
	return map[string]any{
		"cwd": cwd, "is_git_repo": true, "staged": boolArg(args, "staged", false),
		"diff": diff, "chars": len(result.Stdout), "truncated": truncated,
	}, nil
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	if len(value) > 200 {
		value = value[:200]
	}
	return value
}

func nonEmptyLines(value string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func executableExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
