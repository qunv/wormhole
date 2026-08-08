// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var taskContextWord = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_./:-]{2,}`)

var taskContextStopWords = names(
	"and", "the", "for", "from", "with", "into", "this", "that", "what", "where", "when", "which",
	"how", "why", "can", "could", "should", "would", "please", "help", "review", "project", "code",
	"hay", "cho", "toi", "tôi", "voi", "với", "nay", "này", "the", "thể", "them", "thêm", "phan", "phần",
)

func (r *Runtime) taskContext(ctx context.Context, args map[string]any) (any, error) {
	if err := required(args, "query"); err != nil {
		return nil, err
	}
	query := strings.TrimSpace(stringArg(args, "query", ""))
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	root, err := r.Workspace.Resolve(stringArg(args, "path", "."))
	if err != nil {
		return nil, err
	}
	level := contextDetailLevel(args, "normal")
	defaultTokens := detailTokenDefault(level, 6_000, 12_000, 24_000)
	tokenBudget, charBudget := r.contextCharBudget(args, defaultTokens)
	pathArg := r.Workspace.Relative(root)
	inventory, cached, err := r.loadRepoInventory(ctx, root, boolArg(args, "refresh", false))
	if err != nil {
		return nil, err
	}

	depth := intArg(args, "depth", detailInt(level, 2, 3, 5))
	maxEntries := intArg(args, "max_entries", detailInt(level, 80, 180, 500))
	tree, dirs, files := treeFromInventory(inventory.Entries, depth, maxEntries)
	tree, treeTruncated := limitStringsByChars(tree, maxEntries, max(1_000, charBudget/10))
	important, importantTruncated := limitMaps(inventory.Important, detailInt(level, 8, 16, 40))

	result := map[string]any{
		"kind": "task_context", "query": query, "root": pathArg,
		"detail_level": level, "token_budget": tokenBudget, "char_budget": charBudget,
		"cache":   map[string]any{"inventory_hit": cached, "generation": inventory.Generation},
		"profile": compactProjectProfile(inventory.Profile, level),
		"tree": map[string]any{
			"depth": depth, "dirs": dirs, "files": files, "entries": tree,
			"truncated": inventory.Truncated || treeTruncated,
		},
		"important_files": important,
	}
	warnings := []string{}
	truncated := treeTruncated || importantTruncated || inventory.Truncated

	if boolArg(args, "include_git", true) {
		if git, gitErr := r.gitStatus(ctx, pathArg); gitErr == nil {
			result["git"] = git
		} else {
			warnings = append(warnings, "git status: "+gitErr.Error())
		}
	}

	codegraphBudget := min(max(4_000, charBudget*45/100), r.Config.MaxCommandOutput)
	if boolArg(args, "include_codegraph", true) {
		value, graphErr := r.codegraphExplore(ctx, map[string]any{
			"query": query, "projectPath": pathArg,
			"max_output_chars": codegraphBudget,
			"timeout_ms":       intArg(args, "timeout_ms", 120_000),
		})
		if graphErr != nil {
			warnings = append(warnings, "codegraph: "+graphErr.Error())
		} else {
			result["codegraph"] = value
			if text, ok := value.(string); ok && strings.Contains(text, "truncated by Wormhole") {
				truncated = true
			}
		}
	}

	searchLimit := min(max(intArg(args, "search_limit", detailInt(level, 12, 24, 60)), 1), 200)
	terms := taskSearchTerms(query, detailInt(level, 3, 5, 8))
	matches := make([]map[string]any, 0, searchLimit)
	readTargets := map[string]int{}
	readPaths := make([]string, 0, min(searchLimit, 16))
	searchEngines := map[string]bool{}
	patterns := make([]string, 0, len(terms))
	for _, term := range terms {
		patterns = append(patterns, regexp.QuoteMeta(term))
	}
	combinedPattern := strings.Join(patterns, "|")
	found, engine, searchErr := r.Workspace.SearchContext(ctx, pathArg, combinedPattern, true, "", 2, searchLimit)
	if searchErr != nil {
		warnings = append(warnings, fmt.Sprintf("search %q: %v", combinedPattern, searchErr))
	} else {
		searchEngines[engine] = true
		for _, match := range found {
			matches = append(matches, map[string]any{
				"term": matchedTaskTerm(terms, match.Text+"\n"+match.Snippet),
				"path": match.Path, "line": match.Line,
				"text": capString(match.Text, 500), "snippet": capString(match.Snippet, 1_200),
			})
			if _, exists := readTargets[match.Path]; !exists {
				readTargets[match.Path] = match.Line
				readPaths = append(readPaths, match.Path)
			}
		}
	}
	result["search"] = map[string]any{
		"terms": terms, "engines": sortedKeys(searchEngines),
		"count": len(matches), "matches": matches,
	}

	maxReadFiles := min(max(intArg(args, "max_read_files", detailInt(level, 4, 8, 14)), 0), 24)
	readBudget := max(2_000, charBudget*30/100)
	if len(readPaths) > maxReadFiles {
		readPaths = readPaths[:maxReadFiles]
		truncated = true
	}
	reads := make([]map[string]any, 0, len(readPaths))
	perFile := max(1_000, readBudget/max(1, len(readPaths)))
	lineCount := detailInt(level, 40, 80, 160)
	contextBefore := detailInt(level, 8, 16, 30)
	for _, path := range readPaths {
		start := max(1, readTargets[path]-contextBefore)
		read, readErr := r.readOne(map[string]any{
			"path": path, "start_line": start, "line_count": lineCount, "max_chars": perFile,
		})
		if readErr != nil {
			warnings = append(warnings, "read "+path+": "+readErr.Error())
			continue
		}
		if read["truncated"] == true {
			truncated = true
		}
		reads = append(reads, read)
	}
	result["reads"] = map[string]any{"count": len(reads), "files": reads}

	if boolArg(args, "include_memory", false) && r.Config.Memory.Enabled {
		memoryBudget := min(max(400, tokenBudget/10), r.Config.Memory.TokenBudget)
		memoryValue, memoryErr := r.handleMemory(ctx, "memory_context", map[string]any{
			"query": query, "path": pathArg, "limit": detailInt(level, 3, 6, 10), "token_budget": memoryBudget,
		})
		if memoryErr != nil {
			warnings = append(warnings, "memory: "+memoryErr.Error())
		} else {
			result["memory"] = memoryValue
		}
	}

	if len(warnings) > 0 {
		result["warnings"] = warnings
	}
	result["truncated"] = truncated
	result["chars_estimated"] = fitTaskContextBudget(result, max(1, charBudget-96))
	return result, nil
}

func detailInt(level string, compact, normal, full int) int {
	switch level {
	case "compact":
		return compact
	case "full":
		return full
	default:
		return normal
	}
}

func taskSearchTerms(query string, limit int) []string {
	seen := map[string]bool{}
	terms := []string{}
	for _, term := range taskContextWord.FindAllString(query, -1) {
		lower := strings.ToLower(term)
		if len(lower) < 3 || taskContextStopWords[lower] || seen[lower] {
			continue
		}
		seen[lower] = true
		terms = append(terms, term)
		if len(terms) >= limit {
			break
		}
	}
	if len(terms) == 0 {
		terms = append(terms, capString(query, 120))
	}
	return terms
}

func matchedTaskTerm(terms []string, text string) string {
	lower := strings.ToLower(text)
	for _, term := range terms {
		if strings.Contains(lower, strings.ToLower(term)) {
			return term
		}
	}
	if len(terms) > 0 {
		return terms[0]
	}
	return ""
}

func fitTaskContextBudget(result map[string]any, budget int) int {
	estimated := estimatedJSONChars(result)
	if budget <= 0 {
		return estimated
	}
	for attempts := 0; attempts < 96 && estimated > budget; attempts++ {
		changed := false
		if codegraph, ok := result["codegraph"].(string); ok && len(codegraph) > 1_000 {
			over := estimated - budget
			limit := max(1_000, len(codegraph)-over-256)
			result["codegraph"], _ = capText(codegraph, limit)
			changed = true
		}
		if !changed {
			if reads, ok := result["reads"].(map[string]any); ok {
				files, _ := reads["files"].([]map[string]any)
				for index := len(files) - 1; index >= 0; index-- {
					content, _ := files[index]["content"].(string)
					if len(content) <= 400 {
						continue
					}
					files[index]["content"], _ = capText(content, max(400, len(content)/2))
					files[index]["truncated"] = true
					changed = true
					break
				}
				if !changed && len(files) > 1 {
					files = files[:len(files)-1]
					reads["files"] = files
					reads["count"] = len(files)
					changed = true
				}
			}
		}
		if !changed {
			if search, ok := result["search"].(map[string]any); ok {
				matches, _ := search["matches"].([]map[string]any)
				if len(matches) > 1 {
					matches = matches[:max(1, len(matches)/2)]
					search["matches"] = matches
					search["count"] = len(matches)
					search["truncated"] = true
					changed = true
				} else if len(matches) == 1 {
					text, _ := matches[0]["text"].(string)
					snippet, _ := matches[0]["snippet"].(string)
					if len(snippet) > 300 {
						matches[0]["snippet"], _ = capText(snippet, 300)
						changed = true
					} else if len(text) > 200 {
						matches[0]["text"], _ = capText(text, 200)
						changed = true
					}
				}
			}
		}
		if !changed {
			if tree, ok := result["tree"].(map[string]any); ok {
				if entries, ok := tree["entries"].([]string); ok && len(entries) > 5 {
					tree["entries"] = entries[:max(5, len(entries)/2)]
					tree["truncated"] = true
					changed = true
				}
			}
		}
		if !changed {
			if important, ok := result["important_files"].([]map[string]any); ok && len(important) > 2 {
				result["important_files"] = important[:max(2, len(important)/2)]
				changed = true
			} else if _, exists := result["memory"]; exists {
				delete(result, "memory")
				changed = true
			} else if warnings, ok := result["warnings"].([]string); ok && len(warnings) > 1 {
				result["warnings"] = warnings[:1]
				changed = true
			} else if _, exists := result["codegraph"]; exists {
				delete(result, "codegraph")
				changed = true
			} else if _, exists := result["reads"]; exists {
				delete(result, "reads")
				changed = true
			}
		}
		if !changed {
			break
		}
		result["truncated"] = true
		estimated = estimatedJSONChars(result)
	}
	return estimated
}
