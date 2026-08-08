// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"encoding/json"
	"sort"
	"strings"
)

func contextDetailLevel(args map[string]any, fallback string) string {
	level := strings.ToLower(strings.TrimSpace(stringArg(args, "detail_level", fallback)))
	switch level {
	case "compact", "normal", "full":
		return level
	default:
		return fallback
	}
}

func detailTokenDefault(level string, compact, normal, full int) int {
	switch level {
	case "compact":
		return compact
	case "full":
		return full
	default:
		return normal
	}
}

func (r *Runtime) contextCharBudget(args map[string]any, fallbackTokens int) (int, int) {
	tokens := intArg(args, "token_budget", fallbackTokens)
	if tokens <= 0 {
		tokens = fallbackTokens
	}
	maxTokens := max(1_000, r.Config.MaxBatchReadChars/4)
	tokens = min(max(tokens, 1_000), maxTokens)
	chars := min(tokens*4, r.Config.MaxBatchReadChars)
	return tokens, chars
}

func compactProjectProfile(profile map[string]any, level string) map[string]any {
	if profile == nil {
		return map[string]any{}
	}
	out := map[string]any{}
	for _, key := range []string{"languages", "frameworks", "packageManagers", "manifests"} {
		if value, exists := profile[key]; exists {
			out[key] = value
		}
	}
	if level == "full" {
		for key, value := range profile {
			out[key] = value
		}
		return out
	}
	if level == "normal" {
		if scripts, ok := profile["scripts"].(map[string]string); ok {
			out["scripts"] = boundedStringMap(scripts, 20)
		} else if scripts, ok := profile["scripts"].(map[string]any); ok {
			out["scripts"] = boundedAnyMap(scripts, 20)
		}
	}
	return out
}

func boundedStringMap(values map[string]string, limit int) map[string]string {
	if len(values) <= limit {
		return values
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]string, min(limit, len(keys)))
	for _, key := range keys[:min(limit, len(keys))] {
		out[key] = values[key]
	}
	return out
}

func boundedAnyMap(values map[string]any, limit int) map[string]any {
	if len(values) <= limit {
		return values
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]any, min(limit, len(keys)))
	for _, key := range keys[:min(limit, len(keys))] {
		out[key] = values[key]
	}
	return out
}

func limitStringsByChars(values []string, maxItems, maxChars int) ([]string, bool) {
	if maxItems <= 0 || maxChars <= 0 {
		return nil, len(values) > 0
	}
	out := make([]string, 0, min(maxItems, len(values)))
	used := 0
	for _, value := range values {
		cost := len(value) + 3
		if len(out) >= maxItems || used+cost > maxChars {
			return out, true
		}
		out = append(out, value)
		used += cost
	}
	return out, len(out) < len(values)
}

func limitMaps(values []map[string]any, maxItems int) ([]map[string]any, bool) {
	if maxItems <= 0 {
		return nil, len(values) > 0
	}
	if len(values) <= maxItems {
		return values, false
	}
	return values[:maxItems], true
}

func estimatedJSONChars(value any) int {
	raw, _ := json.Marshal(value)
	return len(raw)
}

func fitWorkspaceSnapshotBudget(result map[string]any, budget int) int {
	estimated := estimatedJSONChars(result)
	if budget <= 0 {
		return estimated
	}
	for attempts := 0; attempts < 32 && estimated > budget; attempts++ {
		changed := false
		if symbols, ok := result["symbols"].([]map[string]any); ok && len(symbols) > 20 {
			result["symbols"] = symbols[:max(20, len(symbols)/2)]
			changed = true
		} else if tree, ok := result["tree"].(map[string]any); ok {
			if entries, ok := tree["entries"].([]string); ok && len(entries) > 20 {
				tree["entries"] = entries[:max(20, len(entries)/2)]
				tree["truncated"] = true
				changed = true
			}
		}
		if !changed {
			if important, ok := result["important_files"].([]map[string]any); ok && len(important) > 4 {
				result["important_files"] = important[:max(4, len(important)/2)]
				changed = true
			} else if _, exists := result["memory"]; exists {
				delete(result, "memory")
				changed = true
			} else if git, ok := result["git"].(map[string]any); ok {
				if files, ok := git["files"].([]map[string]any); ok && len(files) > 10 {
					git["files"] = files[:max(10, len(files)/2)]
					git["truncated"] = true
					changed = true
				}
			}
		}
		if !changed {
			if reads, ok := result["recommended_reads"].([]string); ok && len(reads) > 4 {
				result["recommended_reads"] = reads[:max(4, len(reads)/2)]
				changed = true
			} else if _, exists := result["next_best_actions"]; exists {
				delete(result, "next_best_actions")
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
