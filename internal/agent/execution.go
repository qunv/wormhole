// Codebridge
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

	"codebridge/internal/processx"
	"codebridge/internal/security"
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
		return r.gitStatus(ctx, stringArg(args, "cwd", "."))
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
	runOne := func(index int) {
		entry := decodeMap(items[index])
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
	return map[string]any{
		"ok": ok, "parallel": parallel, "requested": len(items), "completed": completed,
		"stopped_early": completed < len(items), "results": results,
	}, nil
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
	return map[string]any{
		"cwd": cwd, "args": argv, "exit_code": result.ExitCode,
		"timed_out": result.TimedOut, "stdout": result.Stdout, "stderr": result.Stderr,
	}, nil
}

func (r *Runtime) gitStatus(ctx context.Context, cwdArg string) (any, error) {
	cwd, err := r.Workspace.Resolve(cwdArg)
	if err != nil {
		return nil, err
	}
	result := processx.Capture(ctx, "git", []string{"status", "--porcelain"}, cwd, 30*time.Second)
	if result.ExitCode != 0 {
		return map[string]any{
			"cwd": cwd, "is_git_repo": false, "clean": nil,
			"error": firstLine(result.Stderr),
		}, nil
	}
	branchResult := processx.Capture(ctx, "git", []string{"rev-parse", "--abbrev-ref", "HEAD"}, cwd, 30*time.Second)
	var files []map[string]any
	for _, line := range nonEmptyLines(result.Stdout) {
		if len(line) < 3 {
			continue
		}
		files = append(files, map[string]any{
			"index": string(line[0]), "worktree": string(line[1]), "path": strings.TrimSpace(line[3:]),
		})
	}
	return map[string]any{
		"cwd": cwd, "is_git_repo": true, "branch": strings.TrimSpace(branchResult.Stdout),
		"clean": len(files) == 0, "count": len(files), "files": files,
	}, nil
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
