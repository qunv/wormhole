// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var defaultSkips = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true,
	".next": true, ".turbo": true, ".cache": true, "coverage": true,
	".venv": true, "__pycache__": true,
}

type Manager struct {
	Primary   string
	Roots     []string
	realRoots []string
	Skips     map[string]bool
	RGBin     string
	mu        sync.RWMutex
}

type Entry struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

type Match struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Text    string `json:"text"`
	Snippet string `json:"snippet,omitempty"`
}

func New(primary string, extra []string, ignored []string) (*Manager, error) {
	if primary == "" {
		return nil, errors.New("workspace is required")
	}
	primary, err := filepath.Abs(primary)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(primary)
	if err != nil {
		return nil, fmt.Errorf("workspace root does not exist: %s: %w", primary, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory: %s", primary)
	}
	roots := dedupe(append([]string{primary}, extra...))
	realRoots := make([]string, 0, len(roots))
	for i, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		rootInfo, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("workspace root does not exist: %s: %w", abs, err)
		}
		if !rootInfo.IsDir() {
			return nil, fmt.Errorf("workspace root is not a directory: %s", abs)
		}
		roots[i] = abs
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace root %s: %w", abs, err)
		}
		realRoots = append(realRoots, real)
	}
	skips := map[string]bool{}
	for key, value := range defaultSkips {
		skips[key] = value
	}
	for _, name := range ignored {
		skips[name] = true
	}
	rg, _ := exec.LookPath(rgExecutable())
	return &Manager{Primary: primary, Roots: roots, realRoots: realRoots, Skips: skips, RGBin: rg}, nil
}

func (m *Manager) Resolve(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		input = "."
	}
	var resolved string
	if filepath.IsAbs(input) {
		resolved = filepath.Clean(input)
	} else {
		resolved = filepath.Join(m.Primary, input)
	}
	resolved, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	canonical := canonicalize(resolved)
	if !within(canonical, m.realRoots) {
		return "", fmt.Errorf("path is outside the allowed roots or resolves outside via a link: %s", input)
	}
	return resolved, nil
}

// OwningRoot returns the configured workspace root that contains input.
// When roots are nested, the most specific configured root wins. The lookup
// uses the same canonicalization rules as Resolve, so missing targets and
// symlinked ancestors still map to a stable configured root.
func (m *Manager) OwningRoot(input string) (string, error) {
	resolved, err := m.Resolve(input)
	if err != nil {
		return "", err
	}
	canonical := canonicalize(resolved)
	bestIndex, bestLength := -1, -1
	for index, realRoot := range m.realRoots {
		if !within(canonical, []string{realRoot}) {
			continue
		}
		if length := len(filepath.Clean(realRoot)); length > bestLength {
			bestIndex, bestLength = index, length
		}
	}
	if bestIndex < 0 {
		return "", fmt.Errorf("path has no configured owning root: %s", input)
	}
	return m.Roots[bestIndex], nil
}

func (m *Manager) Relative(abs string) string {
	abs = filepath.Clean(abs)
	if samePath(abs, m.Primary) {
		return "."
	}
	rel, err := filepath.Rel(m.Primary, abs)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return filepath.ToSlash(rel)
	}
	return abs
}

func (m *Manager) IsRoot(path string) bool {
	canonical := canonicalize(path)
	for index, root := range m.Roots {
		if samePath(path, root) || samePath(canonical, m.realRoots[index]) {
			return true
		}
	}
	return false
}

func (m *Manager) List(start string, recursive bool, limit int) ([]Entry, error) {
	return m.ListContext(context.Background(), start, recursive, limit)
}

func (m *Manager) ListContext(ctx context.Context, start string, recursive bool, limit int) ([]Entry, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := m.Resolve(start)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	var entries []Entry
	var walk func(string) error
	walk = func(dir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		items, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].IsDir() != items[j].IsDir() {
				return items[i].IsDir()
			}
			return items[i].Name() < items[j].Name()
		})
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(entries) >= limit {
				return nil
			}
			if m.Skips[item.Name()] {
				continue
			}
			path := filepath.Join(dir, item.Name())
			info, err := item.Info()
			if err != nil {
				continue
			}
			kind := "file"
			if info.IsDir() {
				kind = "directory"
			}
			entries = append(entries, Entry{
				Path: m.Relative(path), Type: kind, Size: info.Size(),
				Modified: info.ModTime().UTC().Format(time.RFC3339),
			})
			if recursive && info.IsDir() {
				if err := walk(path); err != nil && !errors.Is(err, fs.ErrPermission) {
					return err
				}
			}
		}
		return nil
	}
	return entries, walk(root)
}

func (m *Manager) FindFiles(start, glob string, limit int) ([]string, string, error) {
	return m.FindFilesContext(context.Background(), start, glob, limit)
}

func (m *Manager) FindFilesContext(ctx context.Context, start, glob string, limit int) ([]string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := m.Resolve(start)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 {
		limit = 300
	}
	if m.RGBin != "" {
		args := []string{"--files", "--color", "never"}
		if glob != "" {
			args = append(args, "-g", glob)
		}
		out := make([]string, 0, limit)
		exit, _, runErr := streamCommandLines(ctx, root, m.RGBin, args, func(line string) bool {
			line = strings.TrimSpace(line)
			if line != "" {
				out = append(out, m.Relative(filepath.Join(root, line)))
			}
			return len(out) < limit
		})
		if runErr == nil && (exit == 0 || exit == 1) {
			return out, "ripgrep", nil
		}
		if errors.Is(runErr, context.DeadlineExceeded) {
			return nil, "ripgrep", runErr
		}
	}
	var out []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return nil
		}
		if entry.IsDir() && path != root && m.Skips[entry.Name()] {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		target := filepath.ToSlash(rel)
		if !strings.Contains(glob, "/") {
			target = entry.Name()
		}
		if globMatch(glob, target) {
			out = append(out, m.Relative(path))
		}
		if len(out) >= limit {
			return errStopWalk
		}
		return nil
	})
	if errors.Is(err, errStopWalk) {
		err = nil
	}
	return out, "scan", err
}

func (m *Manager) Search(start, query string, regex bool, glob string, contextLines, limit int) ([]Match, string, error) {
	return m.SearchContext(context.Background(), start, query, regex, glob, contextLines, limit)
}

func (m *Manager) SearchContext(ctx context.Context, start, query string, regex bool, glob string, contextLines, limit int) ([]Match, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := m.Resolve(start)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 {
		limit = 100
	}
	if m.RGBin != "" {
		args := []string{"--no-heading", "--with-filename", "-n", "-S", "--color", "never", "--no-messages"}
		if !regex {
			args = append(args, "-F")
		}
		if glob != "" {
			args = append(args, "-g", glob)
		}
		args = append(args, "-e", query, "--", ".")
		matches := make([]Match, 0, limit)
		exit, _, runErr := streamCommandLines(ctx, root, m.RGBin, args, func(line string) bool {
			if match, ok := parseGrepLine(line, root, m); ok {
				matches = append(matches, match)
			}
			return len(matches) < limit
		})
		if runErr == nil && (exit == 0 || exit == 1) {
			if contextLines > 0 {
				m.attachContext(ctx, matches, contextLines)
			}
			return matches, "ripgrep", nil
		}
		if errors.Is(runErr, context.DeadlineExceeded) {
			return nil, "ripgrep", runErr
		}
	}
	matches, err := m.searchScan(ctx, root, query, regex, glob, limit)
	if contextLines > 0 {
		m.attachContext(ctx, matches, contextLines)
	}
	return matches, "scan", err
}

func (m *Manager) Tree(start string, depth, limit int) ([]string, int, int, error) {
	return m.TreeContext(context.Background(), start, depth, limit)
}

func (m *Manager) TreeContext(ctx context.Context, start string, depth, limit int) ([]string, int, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := m.Resolve(start)
	if err != nil {
		return nil, 0, 0, err
	}
	if depth <= 0 {
		depth = 3
	}
	if limit <= 0 {
		limit = 800
	}
	var entries []string
	dirs, files := 0, 0
	var walk func(string, int) error
	walk = func(dir string, level int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if level > depth || len(entries) >= limit {
			return nil
		}
		items, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("read tree directory %s: %w", m.Relative(dir), err)
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].IsDir() != items[j].IsDir() {
				return items[i].IsDir()
			}
			return items[i].Name() < items[j].Name()
		})
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(entries) >= limit {
				break
			}
			if m.Skips[item.Name()] {
				continue
			}
			path := filepath.Join(dir, item.Name())
			rel := m.Relative(path)
			if item.IsDir() {
				dirs++
				entries = append(entries, rel+"/")
				if err := walk(path, level+1); err != nil {
					return err
				}
			} else {
				files++
				entries = append(entries, rel)
			}
		}
		return nil
	}
	return entries, dirs, files, walk(root, 1)
}

func DetectGitRoot(cwd string) string {
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel")
	raw, err := cmd.Output()
	if err == nil {
		if root := strings.TrimSpace(string(raw)); root != "" {
			return root
		}
	}
	abs, _ := filepath.Abs(cwd)
	return abs
}

func canonicalize(path string) string {
	current := filepath.Clean(path)
	var tail []string
	for i := 0; i < 64; i++ {
		real, err := filepath.EvalSymlinks(current)
		if err == nil {
			if len(tail) == 0 {
				return real
			}
			parts := append([]string{real}, tail...)
			return filepath.Join(parts...)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(path)
		}
		tail = append([]string{filepath.Base(current)}, tail...)
		current = parent
	}
	return filepath.Clean(path)
}

func within(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func parseGrepLine(line, root string, manager *Manager) (Match, bool) {
	first := strings.IndexByte(line, ':')
	if first < 0 {
		return Match{}, false
	}
	secondOffset := strings.IndexByte(line[first+1:], ':')
	if secondOffset < 0 {
		return Match{}, false
	}
	second := first + 1 + secondOffset
	number, err := strconv.Atoi(line[first+1 : second])
	if err != nil {
		return Match{}, false
	}
	text := line[second+1:]
	if len(text) > 500 {
		text = text[:500]
	}
	return Match{Path: manager.Relative(filepath.Join(root, line[:first])), Line: number, Text: text}, true
}

func (m *Manager) searchScan(ctx context.Context, root, query string, useRegex bool, glob string, limit int) ([]Match, error) {
	var expression regexLike
	if useRegex {
		compiled, err := compileRegex(query)
		if err != nil {
			useRegex = false
		} else {
			expression = compiled
		}
	}
	needle := strings.ToLower(query)
	var matches []Match
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return nil
		}
		if entry.IsDir() && path != root && m.Skips[entry.Name()] {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		if glob != "" {
			rel, _ := filepath.Rel(root, path)
			target := filepath.ToSlash(rel)
			if !strings.Contains(glob, "/") {
				target = entry.Name()
			}
			if !globMatch(glob, target) {
				return nil
			}
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), maxRipgrepLineBytes)
		lineNumber := 0
		for scanner.Scan() {
			if ctxErr := ctx.Err(); ctxErr != nil {
				_ = file.Close()
				return ctxErr
			}
			lineNumber++
			line := scanner.Text()
			found := strings.Contains(strings.ToLower(line), needle)
			if useRegex {
				found = expression.MatchString(line)
			}
			if found {
				text := line
				if len(text) > 500 {
					text = text[:500]
				}
				matches = append(matches, Match{Path: m.Relative(path), Line: lineNumber, Text: text})
				if len(matches) >= limit {
					file.Close()
					return errStopWalk
				}
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			file.Close()
			return scanErr
		}
		file.Close()
		return nil
	})
	if errors.Is(err, errStopWalk) {
		err = nil
	}
	return matches, err
}

func (m *Manager) attachContext(ctx context.Context, matches []Match, count int) {
	type contextGroup struct {
		indices []int
		lines   map[int]string
		maxLine int
	}
	groups := map[string]*contextGroup{}
	for index := range matches {
		path, err := m.Resolve(matches[index].Path)
		if err != nil {
			continue
		}
		group := groups[path]
		if group == nil {
			group = &contextGroup{lines: map[int]string{}}
			groups[path] = group
		}
		group.indices = append(group.indices, index)
		group.maxLine = max(group.maxLine, matches[index].Line+count)
	}
	for path, group := range groups {
		if ctx.Err() != nil {
			return
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), maxRipgrepLineBytes)
		lineNumber := 0
		for lineNumber < group.maxLine && scanner.Scan() {
			if ctx.Err() != nil {
				break
			}
			lineNumber++
			for _, index := range group.indices {
				if lineNumber >= matches[index].Line-count && lineNumber <= matches[index].Line+count {
					group.lines[lineNumber] = scanner.Text()
					break
				}
			}
		}
		_ = file.Close()
		for _, index := range group.indices {
			from := max(1, matches[index].Line-count)
			to := matches[index].Line + count
			var snippet []string
			for line := from; line <= to; line++ {
				if text, ok := group.lines[line]; ok {
					snippet = append(snippet, fmt.Sprintf("%d| %s", line, text))
				}
			}
			matches[index].Snippet = strings.Join(snippet, "\n")
		}
	}
}

const maxRipgrepLineBytes = 1 << 20

var errStopWalk = errors.New("stop walk")

func streamCommandLines(ctx context.Context, root, executable string, args []string, visit func(string) bool) (int, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
	scanner.Buffer(make([]byte, 64<<10), maxRipgrepLineBytes)
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
		return exitCode(waitErr), false, waitErr
	}
	return 0, false, nil
}

func dedupe(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		if item == "" {
			continue
		}
		key := filepath.Clean(item)
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, item)
		}
	}
	return out
}

func nonEmptyLines(value string) []string {
	var out []string
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func rgExecutable() string {
	if runtime.GOOS == "windows" {
		return "rg.exe"
	}
	return "rg"
}
