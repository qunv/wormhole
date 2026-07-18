// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"bufio"
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
	if err := os.MkdirAll(primary, 0o755); err != nil {
		return nil, err
	}
	roots := dedupe(append([]string{primary}, extra...))
	realRoots := make([]string, 0, len(roots))
	for i, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		roots[i] = abs
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			real = abs
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
	root, err := m.Resolve(start)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 {
		limit = 300
	}
	if m.RGBin != "" {
		args := []string{"--files", "-g", glob}
		cmd := exec.Command(m.RGBin, args...)
		cmd.Dir = root
		result, err := cmd.Output()
		if err == nil {
			lines := nonEmptyLines(string(result))
			out := make([]string, 0, min(limit, len(lines)))
			for _, line := range lines {
				out = append(out, m.Relative(filepath.Join(root, line)))
				if len(out) >= limit {
					break
				}
			}
			return out, "ripgrep", nil
		}
	}
	var out []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
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
	root, err := m.Resolve(start)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 {
		limit = 100
	}
	if m.RGBin != "" {
		args := []string{"--no-heading", "--with-filename", "-n", "-S", "--color", "never"}
		if !regex {
			args = append(args, "-F")
		}
		if glob != "" {
			args = append(args, "-g", glob)
		}
		args = append(args, "-e", query, "--", ".")
		cmd := exec.Command(m.RGBin, args...)
		cmd.Dir = root
		raw, runErr := cmd.Output()
		if runErr == nil || exitCode(runErr) == 1 {
			matches := parseGrep(string(raw), root, m, limit)
			if contextLines > 0 {
				m.attachContext(matches, contextLines)
			}
			return matches, "ripgrep", nil
		}
	}
	matches, err := m.searchScan(root, query, regex, glob, limit)
	if contextLines > 0 {
		m.attachContext(matches, contextLines)
	}
	return matches, "scan", err
}

func (m *Manager) Tree(start string, depth, limit int) ([]string, int, int, error) {
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
		if level > depth || len(entries) >= limit {
			return nil
		}
		items, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].IsDir() != items[j].IsDir() {
				return items[i].IsDir()
			}
			return items[i].Name() < items[j].Name()
		})
		for _, item := range items {
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
				_ = walk(path, level+1)
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

func parseGrep(raw, root string, manager *Manager, limit int) []Match {
	var matches []Match
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		first := strings.IndexByte(line, ':')
		if first < 0 {
			continue
		}
		secondOffset := strings.IndexByte(line[first+1:], ':')
		if secondOffset < 0 {
			continue
		}
		second := first + 1 + secondOffset
		number, err := strconv.Atoi(line[first+1 : second])
		if err != nil {
			continue
		}
		text := line[second+1:]
		if len(text) > 500 {
			text = text[:500]
		}
		matches = append(matches, Match{
			Path: manager.Relative(filepath.Join(root, line[:first])),
			Line: number, Text: text,
		})
		if len(matches) >= limit {
			break
		}
	}
	return matches
}

func (m *Manager) searchScan(root, query string, useRegex bool, glob string, limit int) ([]Match, error) {
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
		lineNumber := 0
		for scanner.Scan() {
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
		file.Close()
		return nil
	})
	if errors.Is(err, errStopWalk) {
		err = nil
	}
	return matches, err
}

func (m *Manager) attachContext(matches []Match, count int) {
	cache := map[string][]string{}
	for i := range matches {
		path, err := m.Resolve(matches[i].Path)
		if err != nil {
			continue
		}
		lines, ok := cache[path]
		if !ok {
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			lines = strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
			cache[path] = lines
		}
		from := max(1, matches[i].Line-count)
		to := min(len(lines), matches[i].Line+count)
		var snippet []string
		for line := from; line <= to; line++ {
			snippet = append(snippet, fmt.Sprintf("%d| %s", line, lines[line-1]))
		}
		matches[i].Snippet = strings.Join(snippet, "\n")
	}
}

var errStopWalk = errors.New("stop walk")

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
