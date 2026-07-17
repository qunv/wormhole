// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package security

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
)

var catastrophic = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(^|[;&|]\s*)format(\.com)?\s+(/|[a-z]:)`),
	regexp.MustCompile(`(?i)\bdiskpart\b`),
	regexp.MustCompile(`(?i)\bmkfs(\.[a-z0-9]+)?\b`),
	regexp.MustCompile(`(?i)\bfdisk\b`),
	regexp.MustCompile(`(?i)\b(shutdown|reboot|halt|poweroff)\b`),
	regexp.MustCompile(`(?i)\bdd\b[^\n]*\bof=/dev/(sd|nvme|disk|hd)`),
	regexp.MustCompile(`:\(\)\s*\{\s*:\|:&\s*\}\s*;:`),
	regexp.MustCompile(`(?i)\brm\s+-[rRfIlE]*\s+(--no-preserve-root\s+)?/(\s|$|\*)`),
	regexp.MustCompile(`(?i)\breg\s+delete\s+hk(lm|ey_local_machine)`),
}

var safeBlocks = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(del|erase|rmdir|rd|remove-item|rm|format|shutdown|restart-computer|stop-computer|diskpart)\b`),
	regexp.MustCompile(`(?i)\bgit\s+clean\b`),
	regexp.MustCompile(`(?i)\bgit\s+reset\s+--hard\b`),
	regexp.MustCompile(`(?i)\breg\s+delete\b`),
	regexp.MustCompile(`(?i)\b(takeown|icacls)\b`),
	regexp.MustCompile(`(?i)(^|\s)~[\\/]`),
}

func CommandAllowed(command, mode string, allowDangerous bool) error {
	if !allowDangerous {
		for _, pattern := range catastrophic {
			if pattern.MatchString(command) {
				return fmt.Errorf("command blocked: catastrophic system operation")
			}
		}
	}
	if mode != "full" {
		for _, pattern := range safeBlocks {
			if pattern.MatchString(command) {
				return fmt.Errorf("command blocked by safe mode")
			}
		}
		if runtime.GOOS == "windows" && regexp.MustCompile(`(?i)[a-z]:\\`).MatchString(command) {
			return fmt.Errorf("command blocked by safe mode")
		}
	}
	return nil
}

func ArbitraryShellAllowed(mode string) error {
	if mode != "full" {
		return fmt.Errorf("arbitrary shell commands are disabled in safe mode; use a dedicated read-only or quality tool")
	}
	return nil
}

type Risk struct {
	Kind          string `json:"kind"`
	NeedsApproval bool   `json:"needsApproval"`
}

func Classify(action string) Risk {
	patterns := []struct {
		kind    string
		pattern *regexp.Regexp
	}{
		{"install", regexp.MustCompile(`(?i)\b(npm|pip|pip3|yarn|pnpm|cargo|apt|brew|gem|composer)\s+install\b`)},
		{"network", regexp.MustCompile(`(?i)\b(curl|wget|fetch|git\s+push|git\s+fetch|git\s+pull|git\s+clone|docker\s+(push|pull|run|build))\b`)},
		{"delete", regexp.MustCompile(`(?i)\b(delete_path|rm\s+-rf|remove-item)\b`)},
		{"git_mutation", regexp.MustCompile(`(?i)\bgit\s+(push|reset|clean|restore|checkout|commit|merge|rebase)\b`)},
	}
	for _, candidate := range patterns {
		if candidate.pattern.MatchString(action) {
			return Risk{Kind: candidate.kind, NeedsApproval: true}
		}
	}
	for _, pattern := range catastrophic {
		if pattern.MatchString(action) {
			return Risk{Kind: "catastrophic", NeedsApproval: true}
		}
	}
	return Risk{Kind: "general"}
}

func ExplainRisk(action, policy string) map[string]any {
	risk := Classify(action)
	level := map[string]string{
		"install":      "HIGH — installs packages or changes dependencies",
		"network":      "HIGH — performs an external network operation",
		"delete":       "HIGH — permanently removes files",
		"git_mutation": "MEDIUM — mutates git state or remote state",
		"catastrophic": "CRITICAL — system-level destructive operation",
		"general":      "LOW — standard operation",
	}[risk.Kind]
	decision := "ALLOWED"
	if policy == "strict" && risk.Kind != "general" {
		decision = "BLOCKED"
	} else if policy == "balanced" && risk.NeedsApproval {
		decision = "NEEDS_APPROVAL"
	} else if policy == "full" && risk.Kind == "catastrophic" {
		decision = "BLOCKED"
	}
	return map[string]any{"action": action, "kind": risk.Kind, "risk": level, "decision": decision, "policy": policy}
}

func IsReadOnlyGit(args []string) bool {
	readOnly := map[string]bool{
		"status": true, "diff": true, "log": true, "show": true, "ls-files": true,
		"ls-tree": true, "rev-parse": true, "blame": true, "grep": true,
		"cat-file": true, "describe": true, "shortlog": true, "reflog": true,
		"whatchanged": true, "name-rev": true, "merge-base": true,
		"symbolic-ref": true, "for-each-ref": true, "count-objects": true,
		"version": true, "help": true,
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			if arg == "--version" || arg == "--help" || arg == "-v" || arg == "-h" {
				return true
			}
			continue
		}
		return readOnly[strings.ToLower(arg)]
	}
	return false
}

func BadGitFlag(args []string) bool {
	for _, arg := range args {
		lower := strings.ToLower(arg)
		if arg == "-c" || arg == "-C" ||
			strings.HasPrefix(lower, "--git-dir") ||
			strings.HasPrefix(lower, "--work-tree") ||
			strings.HasPrefix(lower, "--output") ||
			lower == "--no-index" || lower == "--ext-diff" ||
			strings.HasPrefix(lower, "--exec-path") ||
			strings.HasPrefix(lower, "--upload-pack") ||
			strings.HasPrefix(lower, "--receive-pack") {
			return true
		}
	}
	return false
}
