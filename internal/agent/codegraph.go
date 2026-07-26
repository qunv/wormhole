// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"codebridge/internal/processx"
)

var (
	codeGraphLookPath = exec.LookPath
	codeGraphCapture  = processx.Capture
)

func (r *Runtime) codegraphExplore(ctx context.Context, args map[string]any) (any, error) {
	if err := required(args, "query"); err != nil {
		return nil, err
	}
	query := strings.TrimSpace(stringArg(args, "query", ""))
	if query == "" {
		return nil, errors.New("query is required")
	}

	root, err := r.Workspace.Resolve(stringArg(args, "projectPath", "."))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("projectPath must be a directory: %s", r.Workspace.Relative(root))
	}

	indexPath := filepath.Join(root, ".codegraph")
	indexInfo, err := os.Stat(indexPath)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("CodeGraph skipped for %s: no .codegraph/ directory exists at the project root. Use workspace_search, search_text, repo_symbols, or read_file instead; indexing is the user's decision.", r.Workspace.Relative(root)), nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect CodeGraph index: %w", err)
	}
	if !indexInfo.IsDir() {
		return fmt.Sprintf("CodeGraph skipped for %s: .codegraph exists but is not a directory. Use the built-in navigation tools instead.", r.Workspace.Relative(root)), nil
	}

	bin, err := codeGraphLookPath("codegraph")
	if err != nil {
		return fmt.Sprintf("CodeGraph index found for %s, but the codegraph CLI is not available on PATH. Use workspace_search, search_text, repo_symbols, or read_file instead.", r.Workspace.Relative(root)), nil
	}

	timeoutMS := intArg(args, "timeout_ms", 120_000)
	if timeoutMS < 1_000 {
		timeoutMS = 1_000
	}
	if timeoutMS > 600_000 {
		timeoutMS = 600_000
	}
	result := codeGraphCapture(ctx, bin, []string{"explore", query}, root, time.Duration(timeoutMS)*time.Millisecond)
	if result.TimedOut {
		return nil, fmt.Errorf("codegraph explore timed out after %dms", timeoutMS)
	}

	stdout := strings.TrimSpace(result.Stdout)
	stderr := strings.TrimSpace(result.Stderr)
	if result.ExitCode != 0 {
		detail := stderr
		if detail == "" {
			detail = stdout
		}
		if detail == "" {
			detail = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		detail, _ = capText(detail, 4_000)
		return nil, fmt.Errorf("codegraph explore failed: %s", detail)
	}
	if stdout == "" {
		stdout = stderr
	}

	level := contextDetailLevel(args, "normal")
	defaultTokens := detailTokenDefault(level, 4_000, 8_000, 20_000)
	_, budgetChars := r.contextCharBudget(args, defaultTokens)
	maxOutput := intArg(args, "max_output_chars", min(budgetChars, r.Config.MaxCommandOutput))
	if maxOutput <= 0 || maxOutput > r.Config.MaxCommandOutput {
		maxOutput = min(budgetChars, r.Config.MaxCommandOutput)
	}
	output, truncated := capText(stdout, maxOutput)
	if truncated {
		output += fmt.Sprintf("\n\n[CodeGraph output truncated by Codebridge at %d characters.]", maxOutput)
	}
	return output, nil
}
