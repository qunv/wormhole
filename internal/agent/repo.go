// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"codebridge/internal/processx"
)

const markerScanKind = "to" + "do"

type repoIndex struct {
	SchemaVersion      int              `json:"schema_version"`
	TS                 time.Time        `json:"ts"`
	Generation         uint64           `json:"generation"`
	Root               string           `json:"root"`
	Depth              int              `json:"depth"`
	Limit              int              `json:"limit"`
	SymbolsIncluded    bool             `json:"symbols_included"`
	InventoryTruncated bool             `json:"inventory_truncated,omitempty"`
	Profile            map[string]any   `json:"profile"`
	Tree               []string         `json:"tree"`
	Dirs               int              `json:"dirs"`
	Files              int              `json:"files"`
	ImportantFiles     []map[string]any `json:"important_files"`
	Symbols            []map[string]any `json:"symbols,omitempty"`
}

const repoIndexSchemaVersion = 3

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
		inventory, _, err := r.loadRepoInventory(ctx, root, boolArg(args, "refresh", false))
		if err != nil {
			return nil, err
		}
		return inventory.Profile, nil
	case "important_files":
		root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
		if err != nil {
			return nil, err
		}
		inventory, _, err := r.loadRepoInventory(ctx, root, false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"count": len(inventory.Important), "files": inventory.Important}, nil
	case "repo_map":
		return r.repoMap(ctx, args)
	case "repo_symbols":
		root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
		if err != nil {
			return nil, err
		}
		symbols, err := r.scanSymbols(ctx, root, intArg(args, "max_files", 500), intArg(args, "max_matches", 2000), stringArg(args, "kind", ""), false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"count": len(symbols), "symbols": symbols}, nil
	case "codegraph_explore":
		return r.codegraphExplore(ctx, args)
	case "index_status":
		var index repoIndex
		if err := r.Store.ReadJSON(r.Store.IndexPath, &index); err != nil {
			return map[string]any{
				"cached": false, "message": "No index cached yet. Call repo_map.",
				"memory_cache": r.repositoryCacheStats(),
			}, nil
		}
		age := time.Since(index.TS)
		generation := r.currentRepositoryGeneration()
		return map[string]any{
			"cached": true, "fresh": age < repoCacheTTL && index.Generation == generation, "ts": index.TS,
			"age_seconds": int(age.Seconds()), "ttl_seconds": int(repoCacheTTL / time.Second),
			"schema_version": index.SchemaVersion, "generation": index.Generation,
			"current_generation": generation, "depth": index.Depth, "limit": index.Limit,
			"inventory_truncated": index.InventoryTruncated,
			"symbols_included":    index.SymbolsIncluded,
			"profile_languages":   index.Profile["languages"], "profile_frameworks": index.Profile["frameworks"],
			"symbols_cached": index.SymbolsIncluded, "ripgrep": map[string]any{"available": r.Workspace.RGBin != "", "bin": r.Workspace.RGBin},
			"memory_cache": r.repositoryCacheStats(),
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
		return r.patternScan(ctx, stringArg(args, "path", "."), intArg(args, "limit", 200), "security")
	case "todo_scan":
		return r.patternScan(ctx, stringArg(args, "path", "."), intArg(args, "limit", 300), markerScanKind)
	case "change_summary":
		return r.changeSummary(ctx, args)
	case "session_report":
		return r.sessionReport(ctx, args)
	default:
		return nil, fmt.Errorf("unsupported repo tool: %s", name)
	}
}

func (r *Runtime) buildIndex(ctx context.Context, root string, depth, limit int, symbols, refresh bool) (repoIndex, bool, error) {
	depth, limit = normalizeRepoIndexParams(depth, limit)
	inventory, _, err := r.loadRepoInventory(ctx, root, refresh)
	if err != nil {
		return repoIndex{}, false, err
	}
	key := repoViewKey{
		Root: root, Generation: inventory.Generation, Depth: depth, Limit: limit, Symbols: symbols,
	}
	if !refresh {
		if cached, ok := r.cachedRepoView(key); ok && time.Since(cached.TS) < repoCacheTTL {
			return cached, true, nil
		}
		if !symbols {
			richKey := key
			richKey.Symbols = true
			if cached, ok := r.cachedRepoView(richKey); ok && time.Since(cached.TS) < repoCacheTTL {
				return cached, true, nil
			}
		}
	}
	tree, dirs, files := treeFromInventory(inventory.Entries, depth, limit)
	index := repoIndex{
		SchemaVersion: repoIndexSchemaVersion, TS: inventory.GeneratedAt,
		Generation: inventory.Generation, Root: root, Depth: depth, Limit: limit,
		SymbolsIncluded: symbols, InventoryTruncated: inventory.Truncated,
		Profile: inventory.Profile, Tree: tree, Dirs: dirs, Files: files,
		ImportantFiles: inventory.Important,
	}
	if symbols {
		index.Symbols, err = r.scanSymbols(ctx, root, 500, 2000, "", refresh)
		if err != nil {
			return repoIndex{}, false, err
		}
	}
	r.storeRepoView(key, index)
	_ = r.Store.WriteJSON(r.Store.IndexPath, index)
	return index, false, nil
}

func normalizeRepoIndexParams(depth, limit int) (int, int) {
	if depth <= 0 {
		depth = 3
	}
	if limit <= 0 {
		limit = 800
	}
	return depth, limit
}

func (r *Runtime) repoMap(ctx context.Context, args map[string]any) (any, error) {
	root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
	if err != nil {
		return nil, err
	}
	index, cached, err := r.buildIndex(ctx, root, intArg(args, "depth", 3), intArg(args, "max_entries", 800), false, boolArg(args, "refresh", false))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"root": r.Workspace.Relative(root), "depth": index.Depth, "max_entries": index.Limit,
		"engine": "scan", "dirs": index.Dirs, "files": index.Files, "tree": index.Tree, "profile": index.Profile,
		"cached": cached, "ripgrep": map[string]any{"available": r.Workspace.RGBin != "", "bin": r.Workspace.RGBin},
	}, nil
}

func (r *Runtime) workspaceSnapshot(ctx context.Context, args map[string]any) (any, error) {
	root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
	if err != nil {
		return nil, err
	}
	index, cached, err := r.buildIndex(ctx, root, intArg(args, "depth", 3), intArg(args, "max_entries", 350), boolArg(args, "include_symbols", false), boolArg(args, "refresh", false))
	if err != nil {
		return nil, err
	}
	git, _ := r.gitStatus(ctx, r.Workspace.Relative(root))
	result := map[string]any{
		"kind": "workspace_snapshot", "pro": true, "version": r.Version, "tier": r.Tier,
		"ts": time.Now().UTC(), "root": r.Workspace.Relative(root), "roots": r.Workspace.Roots,
		"mode": r.Config.Mode, "policy": r.Config.Policy, "profile": index.Profile, "git": git,
		"tree":            map[string]any{"depth": index.Depth, "max_entries": index.Limit, "dirs": index.Dirs, "files": index.Files, "entries": index.Tree},
		"important_files": index.ImportantFiles, "ripgrep": map[string]any{"available": r.Workspace.RGBin != "", "bin": r.Workspace.RGBin},
		"cache":             map[string]any{"hit": cached, "generated_at": index.TS, "ttl_seconds": 300},
		"memory":            r.memoryStatus(ctx),
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
	inventory, _, inventoryErr := r.loadRepoInventory(ctx, root, false)
	profile := map[string]any{}
	if inventoryErr == nil {
		profile = inventory.Profile
	}
	git, _ := r.gitStatus(ctx, r.Workspace.Relative(root))
	memoryHealth := r.memoryHealth(ctx, false)
	runtimeMetrics := r.RuntimeMetrics(false, 0)
	auditMetrics := runtimeMetrics["audit"].(map[string]any)
	auditFailures, _ := auditMetrics["write_failures"].(uint64)
	writerAuditFailures, _ := auditMetrics["writer_write_failures"].(uint64)
	auditFailures += writerAuditFailures
	checks := []map[string]any{
		{"id": "workspace", "status": "pass", "label": "Workspace", "detail": root},
		{"id": "roots", "status": "pass", "label": "Root confinement", "detail": len(r.Workspace.Roots)},
		{"id": "ripgrep", "status": ternary(r.Workspace.RGBin != "", "pass", "warn"), "label": "ripgrep", "detail": r.Workspace.RGBin},
		{"id": "auth", "status": ternary(r.Config.AuthToken != "", "pass", "warn"), "label": "MCP auth", "detail": ternary(r.Config.AuthToken != "", "bearer enabled", "no bearer token")},
		{"id": "git", "status": ternary(git.(map[string]any)["is_git_repo"] == true, "pass", "warn"), "label": "Git", "detail": git},
		{"id": "memory", "status": ternary(!r.Config.Memory.Enabled || memoryHealth.Available, "pass", "warn"), "label": "Memory provider", "detail": memoryHealth},
		{"id": "audit", "status": ternary(!r.Config.Audit || auditFailures == 0, "pass", "warn"), "label": "Audit writer", "detail": auditMetrics},
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

func (r *Runtime) scanSymbols(ctx context.Context, root string, maxFiles, maxMatches int, kind string, refresh bool) ([]map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	maxFiles = min(max(maxFiles, 1), 20_000)
	maxMatches = min(max(maxMatches, 1), 20_000)
	generation := r.currentRepositoryGeneration()
	key := repoSymbolKey{Root: root, Generation: generation, MaxFiles: maxFiles, MaxMatches: maxMatches, Kind: kind}
	if !refresh {
		r.repoCacheMu.Lock()
		if cached, ok := r.repoSymbols[key]; ok && time.Since(cached.GeneratedAt) < repoCacheTTL {
			symbols := append([]map[string]any(nil), cached.Symbols...)
			r.repoCacheMu.Unlock()
			return symbols, nil
		}
		r.repoCacheMu.Unlock()
	}
	var out []map[string]any
	files := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
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
		info, infoErr := entry.Info()
		if infoErr != nil || info.Size() > 4<<20 {
			return nil
		}
		files++
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		line := 0
		for scanner.Scan() {
			if ctxErr := ctx.Err(); ctxErr != nil {
				_ = file.Close()
				return ctxErr
			}
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
		scanErr := scanner.Err()
		_ = file.Close()
		return scanErr
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	r.repoCacheMu.Lock()
	if r.repoGeneration == generation {
		if r.repoSymbols == nil {
			r.repoSymbols = map[repoSymbolKey]repoSymbolCacheEntry{}
		}
		if _, exists := r.repoSymbols[key]; !exists && len(r.repoSymbols) >= repoSymbolCacheLimit {
			var oldestKey repoSymbolKey
			var oldest time.Time
			first := true
			for candidate, entry := range r.repoSymbols {
				if first || entry.GeneratedAt.Before(oldest) {
					oldestKey, oldest, first = candidate, entry.GeneratedAt, false
				}
			}
			delete(r.repoSymbols, oldestKey)
		}
		r.repoSymbols[key] = repoSymbolCacheEntry{GeneratedAt: time.Now().UTC(), Symbols: append([]map[string]any(nil), out...)}
	}
	r.repoCacheMu.Unlock()
	return out, nil
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

func (r *Runtime) patternScan(ctx context.Context, rootArg string, limit int, kind string) (any, error) {
	root, err := r.Workspace.Resolve(rootArg)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limit = min(max(limit, 1), 10_000)
	patterns := scanPatterns(kind)
	findings, gitAvailable, err := r.patternScanGit(ctx, root, limit, kind, patterns)
	if err == nil && gitAvailable {
		return map[string]any{"kind": kind, "engine": "git-grep", "count": len(findings), "findings": findings}, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	findings, trackedOnly, err := r.patternScanFallback(ctx, root, limit, kind, patterns)
	if err != nil {
		return nil, err
	}
	engine := "scan"
	if trackedOnly {
		engine = "tracked-scan"
	}
	return map[string]any{"kind": kind, "engine": engine, "count": len(findings), "findings": findings}, nil
}

func scanPatterns(kind string) []*regexp.Regexp {
	if kind == "security" {
		return []*regexp.Regexp{
			regexp.MustCompile(`(?i)\b(api[_-]?key|secret|password|auth[_-]?token|bearer)\s*[:=]\s*["'][^"']{8,}`),
			regexp.MustCompile(`(?i)\b(eval|exec)\s*\(`),
			regexp.MustCompile(`(?i)\b(md5|sha1)\b`),
		}
	}
	markerPattern := `(?i)\b(` + "TO" + "DO|FIX" + "ME|HA" + "CK|X" + "XX" + `)\b`
	return []*regexp.Regexp{regexp.MustCompile(markerPattern)}
}

func (r *Runtime) patternScanGit(ctx context.Context, root string, limit int, kind string, patterns []*regexp.Regexp) ([]map[string]any, bool, error) {
	prefilter := "TO" + "DO|FIX" + "ME|HA" + "CK|X" + "XX"
	if kind == "security" {
		prefilter = "api[_-]?key|secret|password|auth[_-]?token|bearer|eval|exec|md5|sha1"
	}
	args := []string{"grep", "-n", "-I", "-i", "-E", "-e", prefilter, "--", "."}
	findings := make([]map[string]any, 0, min(limit, 64))
	exit, stopped, runErr := streamAgentCommandLines(ctx, root, "git", args, 30*time.Second, func(line string) bool {
		path, lineNumber, text, ok := parsePatternGrepLine(line)
		if !ok {
			return true
		}
		if finding, matched := r.patternFinding(filepath.Join(root, path), lineNumber, text, kind, patterns); matched {
			findings = append(findings, finding)
		}
		return len(findings) < limit
	})
	if stopped {
		return findings, true, nil
	}
	if exit == 0 || exit == 1 {
		return findings, true, nil
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return nil, true, runErr
	}
	return nil, false, runErr
}

func (r *Runtime) patternScanFallback(ctx context.Context, root string, limit int, kind string, patterns []*regexp.Regexp) ([]map[string]any, bool, error) {
	findings := make([]map[string]any, 0, min(limit, 64))
	trackedAvailable, trackedErr := r.scanTrackedPatternFiles(ctx, root, limit, kind, patterns, &findings)
	if trackedAvailable {
		return findings, true, trackedErr
	}
	if trackedErr != nil && ctx.Err() != nil {
		return nil, true, trackedErr
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root && r.Workspace.Skips[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(findings) >= limit {
			return errStop
		}
		return r.scanPatternFile(ctx, path, limit, kind, patterns, &findings)
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return findings, false, err
}

func (r *Runtime) scanTrackedPatternFiles(ctx context.Context, root string, limit int, kind string, patterns []*regexp.Regexp, findings *[]map[string]any) (bool, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(timeoutCtx, "git", "ls-files", "-z")
	command.Dir = root
	stdout, err := command.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := command.Start(); err != nil {
		return false, nil
	}
	reader := bufio.NewReaderSize(stdout, 32<<10)
	stopped := false
	for {
		record, readErr := reader.ReadString(0)
		record = strings.TrimSuffix(record, "\x00")
		if record != "" {
			path := filepath.Join(root, filepath.FromSlash(record))
			if scanErr := r.scanPatternFile(timeoutCtx, path, limit, kind, patterns, findings); scanErr != nil {
				cancel()
				_ = command.Wait()
				return true, scanErr
			}
			if len(*findings) >= limit {
				stopped = true
				cancel()
				break
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			cancel()
			_ = command.Wait()
			return true, readErr
		}
	}
	waitErr := command.Wait()
	if stopped {
		return true, nil
	}
	if timeoutCtx.Err() != nil {
		return true, timeoutCtx.Err()
	}
	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			return false, nil
		}
		return false, waitErr
	}
	return true, nil
}

func (r *Runtime) scanPatternFile(ctx context.Context, path string, limit int, kind string, patterns []*regexp.Regexp, findings *[]map[string]any) error {
	if len(*findings) >= limit || !textExtensions[strings.ToLower(filepath.Ext(path))] {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1_000_000 {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1_000_000)
	lineNumber := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return err
		}
		lineNumber++
		line := scanner.Text()
		if strings.IndexByte(line, 0) >= 0 {
			_ = file.Close()
			return nil
		}
		if finding, matched := r.patternFinding(path, lineNumber, line, kind, patterns); matched {
			*findings = append(*findings, finding)
			if len(*findings) >= limit {
				break
			}
		}
	}
	scanErr := scanner.Err()
	_ = file.Close()
	return scanErr
}

func (r *Runtime) patternFinding(path string, lineNumber int, line, kind string, patterns []*regexp.Regexp) (map[string]any, bool) {
	for patternIndex, pattern := range patterns {
		if !pattern.MatchString(line) {
			continue
		}
		text := capString(strings.TrimSpace(line), 300)
		if kind == "security" && patternIndex == 0 {
			text = "[redacted potential credential]"
		}
		return map[string]any{"path": r.Workspace.Relative(path), "line": lineNumber, "text": text}, true
	}
	return nil, false
}

func parsePatternGrepLine(line string) (string, int, string, bool) {
	for first := strings.IndexByte(line, ':'); first >= 0; {
		rest := line[first+1:]
		secondOffset := strings.IndexByte(rest, ':')
		if secondOffset < 0 {
			break
		}
		second := first + 1 + secondOffset
		lineNumber, parseErr := strconv.Atoi(line[first+1 : second])
		if parseErr == nil {
			return line[:first], lineNumber, line[second+1:], true
		}
		next := strings.IndexByte(line[first+1:], ':')
		if next < 0 {
			break
		}
		first += next + 1
	}
	return "", 0, "", false
}

func streamAgentCommandLines(ctx context.Context, root, executable string, args []string, timeout time.Duration, visit func(string) bool) (int, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, executable, args...)
	cmd.Dir = root
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, false, err
	}
	if err := cmd.Start(); err != nil {
		return -1, false, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	stopped := false
	for scanner.Scan() {
		if !visit(scanner.Text()) {
			stopped = true
			cancel()
			break
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if stopped {
		return 0, true, nil
	}
	if timeoutCtx.Err() != nil {
		return -1, false, timeoutCtx.Err()
	}
	if scanErr != nil {
		return -1, false, scanErr
	}
	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			return exitError.ExitCode(), false, waitErr
		}
		return -1, false, waitErr
	}
	return 0, false, nil
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
		"memory": r.memoryStatus(ctx), "runtime": r.RuntimeMetrics(true, 10),
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
