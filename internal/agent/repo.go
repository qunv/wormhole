// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"codebridge/internal/processx"
)

type repoIndex struct {
	TS             time.Time        `json:"ts"`
	Root           string           `json:"root"`
	Profile        map[string]any   `json:"profile"`
	Tree           []string         `json:"tree"`
	Dirs           int              `json:"dirs"`
	Files          int              `json:"files"`
	ImportantFiles []map[string]any `json:"important_files"`
	Symbols        []map[string]any `json:"symbols,omitempty"`
}

func (r *Runtime) handleRepo(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "workspace_doctor":
		return r.workspaceDoctor(ctx, args)
	case "workspace_snapshot":
		return r.workspaceSnapshot(ctx, args)
	case "project_profile":
		root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
		if err != nil {
			return nil, err
		}
		return r.projectProfile(root)
	case "important_files":
		root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
		if err != nil {
			return nil, err
		}
		files := r.importantFiles(root)
		return map[string]any{"count": len(files), "files": files}, nil
	case "repo_map":
		return r.repoMap(args)
	case "repo_symbols":
		root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
		if err != nil {
			return nil, err
		}
		symbols, err := r.scanSymbols(root, intArg(args, "max_files", 500), intArg(args, "max_matches", 2000), stringArg(args, "kind", ""))
		if err != nil {
			return nil, err
		}
		return map[string]any{"count": len(symbols), "symbols": symbols}, nil
	case "index_status":
		var index repoIndex
		if err := r.Store.ReadJSON(r.Store.IndexPath, &index); err != nil {
			return map[string]any{"cached": false, "message": "No index cached yet. Call repo_map."}, nil
		}
		age := time.Since(index.TS)
		return map[string]any{
			"cached": true, "fresh": age < 5*time.Minute, "ts": index.TS,
			"age_seconds": int(age.Seconds()), "ttl_seconds": 300,
			"profile_languages": index.Profile["languages"], "profile_frameworks": index.Profile["frameworks"],
			"symbols_cached": index.Symbols != nil, "ripgrep": map[string]any{"available": r.Workspace.RGBin != "", "bin": r.Workspace.RGBin},
		}, nil
	case "detect_test_commands":
		root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
		if err != nil {
			return nil, err
		}
		return r.detectCommands(root), nil
	case "run_tests", "run_build", "run_lint":
		return r.runQualityCommand(ctx, name, args)
	case "run_changed_tests":
		return r.runChangedTests(ctx, args)
	case "quality_gate":
		return r.qualityGate(ctx, args)
	case "review_diff":
		return r.reviewDiff(ctx, args)
	case "security_scan":
		return r.patternScan(stringArg(args, "path", "."), intArg(args, "limit", 200), "security")
	case "todo_scan":
		return r.patternScan(stringArg(args, "path", "."), intArg(args, "limit", 300), "todo")
	case "change_summary":
		return r.changeSummary(ctx, args)
	case "session_report":
		return r.sessionReport(ctx, args)
	default:
		return nil, fmt.Errorf("unsupported repo tool: %s", name)
	}
}

func (r *Runtime) projectProfile(root string) (map[string]any, error) {
	languages, frameworks, packageManagers := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var manifests []string
	scripts := map[string]string{}
	entries, _ := r.Workspace.List(root, true, 4000)
	extensionLanguages := map[string]string{
		".go": "go", ".js": "javascript", ".mjs": "javascript", ".cjs": "javascript",
		".ts": "typescript", ".tsx": "typescript", ".jsx": "javascript", ".py": "python",
		".rs": "rust", ".java": "java", ".kt": "kotlin", ".dart": "dart", ".rb": "ruby",
		".php": "php", ".cs": "csharp", ".cpp": "cpp", ".c": "c", ".swift": "swift",
	}
	for _, entry := range entries {
		if entry.Type != "file" {
			continue
		}
		base := strings.ToLower(filepath.Base(entry.Path))
		if language := extensionLanguages[strings.ToLower(filepath.Ext(entry.Path))]; language != "" {
			languages[language] = true
		}
		if manifestNames[base] {
			manifests = append(manifests, entry.Path)
		}
	}
	if fileExists(filepath.Join(root, "go.mod")) {
		languages["go"], packageManagers["go"] = true, true
	}
	if fileExists(filepath.Join(root, "Cargo.toml")) {
		languages["rust"], packageManagers["cargo"] = true, true
	}
	if fileExists(filepath.Join(root, "pyproject.toml")) || fileExists(filepath.Join(root, "requirements.txt")) {
		languages["python"], packageManagers["pip"] = true, true
	}
	if raw, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		languages["javascript"] = true
		var pkg struct {
			Scripts      map[string]string `json:"scripts"`
			Dependencies map[string]string `json:"dependencies"`
			DevDeps      map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(raw, &pkg) == nil {
			for key, value := range pkg.Scripts {
				scripts[key] = value
			}
			for dependency := range mergeStringMaps(pkg.Dependencies, pkg.DevDeps) {
				switch dependency {
				case "react", "next":
					frameworks[dependency] = true
				case "vue":
					frameworks["vue"] = true
				case "@angular/core":
					frameworks["angular"] = true
				case "svelte":
					frameworks["svelte"] = true
				case "express":
					frameworks["express"] = true
				case "fastify":
					frameworks["fastify"] = true
				}
			}
		}
		switch {
		case fileExists(filepath.Join(root, "pnpm-lock.yaml")):
			packageManagers["pnpm"] = true
		case fileExists(filepath.Join(root, "yarn.lock")):
			packageManagers["yarn"] = true
		default:
			packageManagers["npm"] = true
		}
	}
	return map[string]any{
		"rootDir": root, "languages": sortedKeys(languages), "frameworks": sortedKeys(frameworks),
		"packageManagers": sortedKeys(packageManagers), "manifests": manifests, "scripts": scripts,
	}, nil
}

func (r *Runtime) importantFiles(root string) []map[string]any {
	names := map[string]bool{
		"readme.md": true, "agents.md": true, "contributing.md": true, "license": true,
		"go.mod": true, "go.sum": true, "package.json": true, "package-lock.json": true,
		"pnpm-lock.yaml": true, "yarn.lock": true, "cargo.toml": true, "pyproject.toml": true,
		"dockerfile": true, "docker-compose.yml": true, "makefile": true, ".gitignore": true,
		"tsconfig.json": true, "vite.config.ts": true, "next.config.js": true,
	}
	var out []map[string]any
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root && r.Workspace.Skips[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.Count(rel, string(os.PathSeparator)) > 3 {
			return nil
		}
		base := strings.ToLower(entry.Name())
		if !names[base] && !strings.HasPrefix(filepath.ToSlash(rel), ".github/workflows/") {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		out = append(out, map[string]any{"path": r.Workspace.Relative(path), "size": info.Size()})
		if len(out) >= 100 {
			return errStop
		}
		return nil
	})
	return out
}

func (r *Runtime) buildIndex(root string, depth, limit int, symbols, refresh bool) (repoIndex, bool, error) {
	var cached repoIndex
	if !refresh && r.Store.ReadJSON(r.Store.IndexPath, &cached) == nil &&
		cached.Root == root && time.Since(cached.TS) < 5*time.Minute &&
		(!symbols || cached.Symbols != nil) {
		return cached, true, nil
	}
	tree, dirs, files, err := r.Workspace.Tree(root, depth, limit)
	if err != nil {
		return repoIndex{}, false, err
	}
	profile, err := r.projectProfile(root)
	if err != nil {
		return repoIndex{}, false, err
	}
	index := repoIndex{
		TS: time.Now().UTC(), Root: root, Profile: profile, Tree: tree, Dirs: dirs,
		Files: files, ImportantFiles: r.importantFiles(root),
	}
	if symbols {
		index.Symbols, _ = r.scanSymbols(root, 500, 2000, "")
	}
	_ = r.Store.WriteJSON(r.Store.IndexPath, index)
	return index, false, nil
}

func (r *Runtime) repoMap(args map[string]any) (any, error) {
	root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
	if err != nil {
		return nil, err
	}
	index, cached, err := r.buildIndex(root, intArg(args, "depth", 3), intArg(args, "max_entries", 800), false, boolArg(args, "refresh", false))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"root": r.Workspace.Relative(root), "depth": intArg(args, "depth", 3), "engine": "scan",
		"dirs": index.Dirs, "files": index.Files, "tree": index.Tree, "profile": index.Profile,
		"cached": cached, "ripgrep": map[string]any{"available": r.Workspace.RGBin != "", "bin": r.Workspace.RGBin},
	}, nil
}

func (r *Runtime) workspaceSnapshot(ctx context.Context, args map[string]any) (any, error) {
	root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
	if err != nil {
		return nil, err
	}
	index, cached, err := r.buildIndex(root, intArg(args, "depth", 3), intArg(args, "max_entries", 350), boolArg(args, "include_symbols", false), boolArg(args, "refresh", false))
	if err != nil {
		return nil, err
	}
	git, _ := r.gitStatus(ctx, r.Workspace.Relative(root))
	result := map[string]any{
		"kind": "workspace_snapshot", "pro": true, "version": r.Version, "tier": r.Tier,
		"ts": time.Now().UTC(), "root": r.Workspace.Relative(root), "roots": r.Workspace.Roots,
		"mode": r.Config.Mode, "policy": r.Config.Policy, "profile": index.Profile, "git": git,
		"tree":            map[string]any{"depth": intArg(args, "depth", 3), "dirs": index.Dirs, "files": index.Files, "entries": index.Tree},
		"important_files": index.ImportantFiles, "ripgrep": map[string]any{"available": r.Workspace.RGBin != "", "bin": r.Workspace.RGBin},
		"cache":             map[string]any{"hit": cached, "generated_at": index.TS, "ttl_seconds": 300},
		"recommended_reads": recommendedReads(index.ImportantFiles),
		"next_best_actions": []string{"Read the relevant manifests and entry points.", "Use search_text with context.", "Use review_diff before handoff."},
	}
	if boolArg(args, "include_symbols", false) {
		result["symbols"] = index.Symbols
	}
	return result, nil
}

func (r *Runtime) workspaceDoctor(ctx context.Context, args map[string]any) (any, error) {
	root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
	if err != nil {
		return nil, err
	}
	profile, _ := r.projectProfile(root)
	git, _ := r.gitStatus(ctx, r.Workspace.Relative(root))
	checks := []map[string]any{
		{"id": "workspace", "status": "pass", "label": "Workspace", "detail": root},
		{"id": "roots", "status": "pass", "label": "Root confinement", "detail": len(r.Workspace.Roots)},
		{"id": "ripgrep", "status": ternary(r.Workspace.RGBin != "", "pass", "warn"), "label": "ripgrep", "detail": r.Workspace.RGBin},
		{"id": "auth", "status": ternary(r.Config.AuthToken != "", "pass", "warn"), "label": "MCP auth", "detail": ternary(r.Config.AuthToken != "", "bearer enabled", "no bearer token")},
		{"id": "git", "status": ternary(git.(map[string]any)["is_git_repo"] == true, "pass", "warn"), "label": "Git", "detail": git},
	}
	score := 100
	for _, check := range checks {
		if check["status"] == "warn" {
			score -= 10
		}
	}
	return map[string]any{
		"ok": score >= 70, "score": score, "root": root, "mode": r.Config.Mode,
		"policy": r.Config.Policy, "checks": checks, "profile": profile,
		"recommendations": []string{"Use an MCP bearer token outside a tunnel-only setup.", "Install ripgrep for faster code search."},
	}, nil
}

var symbolPatterns = []struct {
	Kind string
	RE   *regexp.Regexp
}{
	{"function", regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)`)},
	{"function", regexp.MustCompile(`^\s*func\s+(?:\([^)]*\)\s*)?([A-Za-z_][\w]*)\s*\(`)},
	{"class", regexp.MustCompile(`^\s*(?:export\s+)?class\s+([A-Za-z_$][\w$]*)`)},
	{"const", regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)`)},
	{"class", regexp.MustCompile(`^\s*(?:class|struct|interface|type)\s+([A-Za-z_][\w]*)`)},
	{"route", regexp.MustCompile(`\b(?:app|router)\.(get|post|put|patch|delete)\s*\(\s*["']([^"']+)`)},
}

func (r *Runtime) scanSymbols(root string, maxFiles, maxMatches int, kind string) ([]map[string]any, error) {
	var out []map[string]any
	files := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root && r.Workspace.Skips[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if files >= maxFiles || len(out) >= maxMatches {
			return errStop
		}
		if !sourceExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		files++
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		line := 0
		for scanner.Scan() {
			line++
			text := scanner.Text()
			for _, pattern := range symbolPatterns {
				if kind != "" && pattern.Kind != kind {
					continue
				}
				match := pattern.RE.FindStringSubmatch(text)
				if len(match) > 1 {
					name := match[1]
					if pattern.Kind == "route" && len(match) > 2 {
						name += " " + match[2]
					}
					out = append(out, map[string]any{"path": r.Workspace.Relative(path), "line": line, "kind": pattern.Kind, "name": name})
					break
				}
			}
			if len(out) >= maxMatches {
				break
			}
		}
		return nil
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return out, err
}

func (r *Runtime) detectCommands(root string) map[string]any {
	commands := map[string]string{}
	source := "detected"
	if profile := r.currentProfile(); profile != nil {
		if overrides, ok := profile["testCommands"].(map[string]any); ok {
			for _, key := range []string{"test", "build", "lint"} {
				if value, ok := overrides[key].(string); ok && value != "" {
					commands[key] = value
					source = "profile"
				}
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(raw, &pkg) == nil {
			manager := "npm"
			if fileExists(filepath.Join(root, "pnpm-lock.yaml")) {
				manager = "pnpm"
			} else if fileExists(filepath.Join(root, "yarn.lock")) {
				manager = "yarn"
			}
			for _, key := range []string{"test", "build", "lint"} {
				if _, exists := commands[key]; !exists && pkg.Scripts[key] != "" {
					commands[key] = manager + " run " + key
				}
			}
		}
	}
	if fileExists(filepath.Join(root, "go.mod")) {
		if commands["test"] == "" {
			commands["test"] = "go test ./..."
		}
		if commands["build"] == "" {
			commands["build"] = "go build ./..."
		}
		if commands["lint"] == "" && executableExists("golangci-lint") {
			commands["lint"] = "golangci-lint run"
		}
	}
	if fileExists(filepath.Join(root, "Cargo.toml")) {
		if commands["test"] == "" {
			commands["test"] = "cargo test"
		}
		if commands["build"] == "" {
			commands["build"] = "cargo build"
		}
		if commands["lint"] == "" {
			commands["lint"] = "cargo clippy"
		}
	}
	if fileExists(filepath.Join(root, "pyproject.toml")) || fileExists(filepath.Join(root, "pytest.ini")) {
		if commands["test"] == "" {
			commands["test"] = "pytest"
		}
	}
	return map[string]any{"root": root, "source": source, "commands": commands}
}

func (r *Runtime) runQualityCommand(ctx context.Context, tool string, args map[string]any) (any, error) {
	root, err := r.Workspace.Resolve(stringArg(args, "cwd", "."))
	if err != nil {
		return nil, err
	}
	kind := strings.TrimPrefix(tool, "run_")
	command := stringArg(args, "command", "")
	if command == "" {
		detected := r.detectCommands(root)
		commands := detected["commands"].(map[string]string)
		command = commands[kind]
	}
	if command == "" {
		return nil, fmt.Errorf("no %s command detected; provide command explicitly or configure .agent/profile.json", kind)
	}
	entry := map[string]any{
		"command": command, "cwd": r.Workspace.Relative(root),
		"shell": stringArg(args, "shell", ""), "timeout_ms": intArg(args, "timeout_ms", 600_000),
		"max_output_chars": intArg(args, "max_output_chars", 50_000), "internal_quality_command": true,
	}
	value, err := r.runCommand(ctx, entry)
	if err != nil {
		return nil, err
	}
	result := value.(map[string]any)
	result["ok"] = result["exit_code"] == 0
	result["kind"] = kind
	return result, nil
}

func (r *Runtime) qualityGate(ctx context.Context, args map[string]any) (any, error) {
	requested := []string{}
	for _, kind := range []string{"test", "build", "lint"} {
		if boolArg(args, kind, kind == "test") {
			requested = append(requested, kind)
		}
	}
	var results []any
	ok := true
	for _, kind := range requested {
		value, err := r.runQualityCommand(ctx, "run_"+kind, map[string]any{"cwd": stringArg(args, "cwd", ".")})
		if err != nil {
			results = append(results, map[string]any{"kind": kind, "ok": false, "error": err.Error()})
			ok = false
			continue
		}
		results = append(results, value)
		if value.(map[string]any)["ok"] != true {
			ok = false
		}
	}
	return map[string]any{"ok": ok, "requested": requested, "results": results}, nil
}

func (r *Runtime) runChangedTests(ctx context.Context, args map[string]any) (any, error) {
	if command := stringArg(args, "command", ""); command != "" {
		return r.runQualityCommand(ctx, "run_tests", args)
	}
	cwd, err := r.Workspace.Resolve(stringArg(args, "cwd", "."))
	if err != nil {
		return nil, err
	}
	changed := processx.Capture(ctx, "git", []string{"diff", "--name-only", "HEAD"}, cwd, 30*time.Second)
	command := ""
	if fileExists(filepath.Join(cwd, "go.mod")) {
		packages := map[string]bool{}
		for _, path := range nonEmptyLines(changed.Stdout) {
			if strings.HasSuffix(path, ".go") {
				packages["./"+filepath.ToSlash(filepath.Dir(path))] = true
			}
		}
		if len(packages) > 0 {
			command = "go test " + strings.Join(sortedKeys(packages), " ")
		}
	}
	if command == "" {
		detected := r.detectCommands(cwd)
		command = detected["commands"].(map[string]string)["test"]
	}
	return r.runQualityCommand(ctx, "run_tests", map[string]any{"cwd": r.Workspace.Relative(cwd), "command": command})
}

func (r *Runtime) reviewDiff(ctx context.Context, args map[string]any) (any, error) {
	diffValue, err := r.gitDiff(ctx, args)
	if err != nil {
		return nil, err
	}
	diffMap := diffValue.(map[string]any)
	diff, _ := diffMap["diff"].(string)
	if diff == "" {
		return map[string]any{"verdict": "CLEAN", "findings": []any{}, "summary": "No diff to review."}, nil
	}
	patterns := []struct {
		Severity string
		Label    string
		RE       *regexp.Regexp
	}{
		{"P1", "Likely committed secret", regexp.MustCompile(`(?i)^\+.*(api[_-]?key|secret|password|token)\s*[:=]\s*["'][^"']{8,}`)},
		{"P2", "Debug logging added", regexp.MustCompile(`^\+.*\b(console\.log|fmt\.Print|print\()`)},
		{"P2", "Unsafe panic or fatal path added", regexp.MustCompile(`^\+.*\b(panic\(|log\.Fatal|process\.exit\()`)},
		{"P3", "TODO marker added", regexp.MustCompile(`(?i)^\+.*\b(TODO|FIXME|HACK)\b`)},
	}
	var findings []map[string]any
	for lineNumber, line := range strings.Split(diff, "\n") {
		for _, pattern := range patterns {
			if pattern.RE.MatchString(line) {
				findings = append(findings, map[string]any{
					"severity": pattern.Severity, "line": lineNumber + 1, "message": pattern.Label,
					"evidence": capString(line, 240),
				})
			}
		}
	}
	verdict := "PASS"
	for _, finding := range findings {
		if finding["severity"] == "P1" {
			verdict = "BLOCK"
			break
		}
		verdict = "WARN"
	}
	return map[string]any{"verdict": verdict, "findings": findings, "summary": fmt.Sprintf("%d heuristic finding(s)", len(findings))}, nil
}

func (r *Runtime) patternScan(rootArg string, limit int, kind string) (any, error) {
	root, err := r.Workspace.Resolve(rootArg)
	if err != nil {
		return nil, err
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(TODO|FIXME|HACK|XXX)\b`),
	}
	if kind == "security" {
		patterns = []*regexp.Regexp{
			regexp.MustCompile(`(?i)\b(api[_-]?key|secret|password|auth[_-]?token|bearer)\s*[:=]\s*["'][^"']{8,}`),
			regexp.MustCompile(`(?i)\b(eval|exec)\s*\(`),
			regexp.MustCompile(`(?i)\b(md5|sha1)\b`),
		}
	}
	var findings []map[string]any
	tracked := map[string]bool{}
	gitFiles := processx.Capture(context.Background(), "git", []string{"ls-files", "-z"}, root, 30*time.Second)
	if gitFiles.ExitCode == 0 {
		for _, rel := range strings.Split(gitFiles.Stdout, "\x00") {
			if rel != "" {
				tracked[filepath.Clean(filepath.Join(root, rel))] = true
			}
		}
	}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root && r.Workspace.Skips[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(findings) >= limit || !textExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if len(tracked) > 0 && !tracked[filepath.Clean(path)] {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) > 1_000_000 || strings.IndexByte(string(raw), 0) >= 0 {
			return nil
		}
		for lineIndex, line := range strings.Split(string(raw), "\n") {
			for patternIndex, pattern := range patterns {
				if pattern.MatchString(line) {
					text := capString(strings.TrimSpace(line), 300)
					if kind == "security" && patternIndex == 0 {
						text = "[redacted potential credential]"
					}
					findings = append(findings, map[string]any{
						"path": r.Workspace.Relative(path), "line": lineIndex + 1, "text": text,
					})
					break
				}
			}
			if len(findings) >= limit {
				break
			}
		}
		return nil
	})
	return map[string]any{"kind": kind, "count": len(findings), "findings": findings}, nil
}

func (r *Runtime) changeSummary(ctx context.Context, args map[string]any) (any, error) {
	statusValue, err := r.gitStatus(ctx, stringArg(args, "path", "."))
	if err != nil {
		return nil, err
	}
	status := statusValue.(map[string]any)
	if status["is_git_repo"] != true {
		return status, nil
	}
	counts := map[string]int{"added": 0, "modified": 0, "deleted": 0, "renamed": 0, "untracked": 0}
	for _, raw := range status["files"].([]map[string]any) {
		code := fmt.Sprint(raw["index"]) + fmt.Sprint(raw["worktree"])
		switch {
		case code == "??":
			counts["untracked"]++
		case strings.Contains(code, "D"):
			counts["deleted"]++
		case strings.Contains(code, "R"):
			counts["renamed"]++
		case strings.Contains(code, "A"):
			counts["added"]++
		default:
			counts["modified"]++
		}
	}
	return map[string]any{"is_git_repo": true, "count": status["count"], "counts": counts, "files": status["files"]}, nil
}

func (r *Runtime) sessionReport(ctx context.Context, args map[string]any) (any, error) {
	status, _ := r.gitStatus(ctx, stringArg(args, "path", "."))
	var task map[string]any
	_ = readJSONFile(r.statePath("current-task.json"), &task)
	var checkpoint map[string]any
	_ = r.Store.ReadJSON(r.Store.Checkpoint, &checkpoint)
	review, _ := r.reviewDiff(ctx, map[string]any{"cwd": stringArg(args, "path", ".")})
	return map[string]any{
		"ts": time.Now().UTC(), "workspace": r.Workspace.Primary, "git": status,
		"task": task, "checkpoint": checkpoint, "review": review,
	}, nil
}

func recommendedReads(files []map[string]any) []string {
	var out []string
	for _, file := range files {
		path, _ := file["path"].(string)
		if path != "" {
			out = append(out, path)
		}
		if len(out) >= 12 {
			break
		}
	}
	return out
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for key, enabled := range values {
		if enabled {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func mergeStringMaps(values ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, value := range values {
		for key, entry := range value {
			out[key] = entry
		}
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func capString(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

var (
	errStop          = errors.New("stop walk")
	sourceExtensions = map[string]bool{
		".go": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".ts": true,
		".tsx": true, ".py": true, ".rs": true, ".java": true, ".kt": true, ".dart": true,
		".rb": true, ".php": true, ".cs": true, ".c": true, ".h": true, ".cpp": true,
		".swift": true, ".vue": true, ".svelte": true,
	}
	textExtensions = map[string]bool{
		".go": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".ts": true,
		".tsx": true, ".py": true, ".rs": true, ".java": true, ".kt": true, ".dart": true,
		".rb": true, ".php": true, ".cs": true, ".c": true, ".h": true, ".cpp": true,
		".swift": true, ".vue": true, ".svelte": true, ".json": true, ".yaml": true,
		".yml": true, ".toml": true, ".md": true, ".txt": true, ".env": true,
	}
)
