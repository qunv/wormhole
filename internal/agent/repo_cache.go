// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	repoCacheTTL            = 5 * time.Minute
	repoInventoryEntryLimit = 50_000
	repoInventoryCacheLimit = 8
	repoViewCacheLimit      = 16
	repoSymbolCacheLimit    = 16
	gitStatusCacheLimit     = 16
)

type repoInventoryEntry struct {
	Path         string
	RootRelative string
	Type         string
	Size         int64
	Modified     time.Time
	Depth        int
}

type repoInventory struct {
	GeneratedAt time.Time
	Generation  uint64
	Root        string
	Entries     []repoInventoryEntry
	Dirs        int
	Files       int
	Truncated   bool
	Profile     map[string]any
	Important   []map[string]any
}

type repoViewKey struct {
	Root       string
	Generation uint64
	Depth      int
	Limit      int
	Symbols    bool
}

type repoSymbolKey struct {
	Root       string
	Generation uint64
	MaxFiles   int
	MaxMatches int
	Kind       string
}

type repoSymbolCacheEntry struct {
	GeneratedAt time.Time
	Symbols     []map[string]any
}

type gitFileStatus struct {
	Index    string
	Worktree string
	Path     string
}

type gitStatusSnapshot struct {
	GeneratedAt   time.Time
	Generation    uint64
	CWD           string
	IsGitRepo     bool
	Branch        string
	Clean         *bool
	Files         []gitFileStatus
	RenderedFiles []map[string]any
	Error         string
}

func (r *Runtime) currentRepositoryGeneration() uint64 {
	r.repoCacheMu.Lock()
	defer r.repoCacheMu.Unlock()
	return r.repoGeneration
}

func (r *Runtime) gitStatusCacheTTL() time.Duration {
	milliseconds := r.Config.GitStatusCacheMS
	if milliseconds <= 0 {
		milliseconds = 2_000
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func (r *Runtime) repositoryCacheStats() map[string]any {
	if r == nil {
		return map[string]any{"enabled": false}
	}
	r.repoCacheMu.Lock()
	defer r.repoCacheMu.Unlock()
	return map[string]any{
		"enabled": true, "generation": r.repoGeneration,
		"inventories": len(r.repoInventories), "inventory_limit": repoInventoryCacheLimit,
		"views": len(r.repoViews), "view_limit": repoViewCacheLimit,
		"symbol_views": len(r.repoSymbols), "symbol_view_limit": repoSymbolCacheLimit,
		"git_status_snapshots": len(r.gitStatusCache), "git_status_limit": gitStatusCacheLimit,
		"inventory_entry_limit": repoInventoryEntryLimit,
		"ttl_seconds":           int64(repoCacheTTL / time.Second),
		"git_status_ttl_ms":     int64(r.gitStatusCacheTTL() / time.Millisecond),
	}
}

func (r *Runtime) invalidateRepositoryCaches() {
	if r == nil {
		return
	}
	r.repoCacheMu.Lock()
	r.repoGeneration++
	r.repoInventories = nil
	r.repoViews = nil
	r.repoSymbols = nil
	r.gitStatusCache = nil
	r.repoCacheMu.Unlock()
}

func (r *Runtime) loadRepoInventory(ctx context.Context, root string, refresh bool) (repoInventory, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if refresh {
		// A forced refresh represents a new repository observation generation.
		// Discard every derived view so other depth/symbol combinations cannot
		// survive with stale content under the previous generation key.
		r.invalidateRepositoryCaches()
	}
	now := time.Now().UTC()
	r.repoCacheMu.Lock()
	generation := r.repoGeneration
	if !refresh && r.repoInventories != nil {
		if cached, ok := r.repoInventories[root]; ok && cached.Generation == generation && now.Sub(cached.GeneratedAt) < repoCacheTTL {
			r.repoCacheMu.Unlock()
			return cached, true, nil
		}
	}
	r.repoCacheMu.Unlock()

	// Repository scans are deliberately serialized per runtime. This avoids two
	// simultaneous snapshot requests duplicating the same filesystem walk while
	// still allowing ordinary reads and Git calls to proceed.
	r.repoIndexMu.Lock()
	defer r.repoIndexMu.Unlock()

	now = time.Now().UTC()
	r.repoCacheMu.Lock()
	generation = r.repoGeneration
	if !refresh && r.repoInventories != nil {
		if cached, ok := r.repoInventories[root]; ok && cached.Generation == generation && now.Sub(cached.GeneratedAt) < repoCacheTTL {
			r.repoCacheMu.Unlock()
			return cached, true, nil
		}
	}
	r.repoCacheMu.Unlock()

	inventory, err := r.buildRepoInventory(ctx, root, generation)
	if err != nil {
		return repoInventory{}, false, err
	}
	r.repoCacheMu.Lock()
	if r.repoGeneration == generation {
		if r.repoInventories == nil {
			r.repoInventories = map[string]repoInventory{}
		}
		if _, exists := r.repoInventories[root]; !exists && len(r.repoInventories) >= repoInventoryCacheLimit {
			oldestRoot := ""
			var oldest time.Time
			for candidate, entry := range r.repoInventories {
				if oldestRoot == "" || entry.GeneratedAt.Before(oldest) {
					oldestRoot, oldest = candidate, entry.GeneratedAt
				}
			}
			delete(r.repoInventories, oldestRoot)
		}
		r.repoInventories[root] = inventory
	}
	r.repoCacheMu.Unlock()
	return inventory, false, nil
}

func (r *Runtime) buildRepoInventory(ctx context.Context, root string, generation uint64) (repoInventory, error) {
	inventory := repoInventory{
		GeneratedAt: time.Now().UTC(), Generation: generation, Root: root,
		Entries: make([]repoInventoryEntry, 0, min(repoInventoryEntryLimit, 4096)),
	}
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		items, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil
			}
			return err
		}
		sort.Slice(items, func(left, right int) bool {
			if items[left].IsDir() != items[right].IsDir() {
				return items[left].IsDir()
			}
			return items[left].Name() < items[right].Name()
		})
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			if r.Workspace.Skips[item.Name()] {
				continue
			}
			if len(inventory.Entries) >= repoInventoryEntryLimit {
				inventory.Truncated = true
				return errStop
			}
			path := filepath.Join(dir, item.Name())
			info, infoErr := item.Info()
			if infoErr != nil {
				continue
			}
			rootRelative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				continue
			}
			entry := repoInventoryEntry{
				Path: r.Workspace.Relative(path), RootRelative: filepath.ToSlash(rootRelative),
				Type: "file", Size: info.Size(), Modified: info.ModTime().UTC(), Depth: depth,
			}
			if info.IsDir() {
				entry.Type = "directory"
				inventory.Dirs++
			} else {
				inventory.Files++
			}
			inventory.Entries = append(inventory.Entries, entry)
			if info.IsDir() {
				if walkErr := walk(path, depth+1); walkErr != nil {
					return walkErr
				}
			}
		}
		return nil
	}
	if err := walk(root, 1); err != nil && !errors.Is(err, errStop) {
		return repoInventory{}, err
	}
	inventory.Profile = r.projectProfileFromInventory(ctx, root, inventory.Entries)
	inventory.Important = r.importantFilesFromInventory(inventory.Entries)
	return inventory, nil
}

func (r *Runtime) projectProfileFromInventory(ctx context.Context, root string, entries []repoInventoryEntry) map[string]any {
	languages, frameworks, packageManagers := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var manifests []string
	scripts := map[string]string{}
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
	if ctx.Err() == nil {
		if raw, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
			languages["javascript"] = true
			var pkg struct {
				Scripts      map[string]string `json:"scripts"`
				Dependencies map[string]string `json:"dependencies"`
				DevDeps      map[string]string `json:"devDependencies"`
			}
			if decodeJSON(raw, &pkg) == nil {
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
	}
	return map[string]any{
		"rootDir": root, "languages": sortedKeys(languages), "frameworks": sortedKeys(frameworks),
		"packageManagers": sortedKeys(packageManagers), "manifests": manifests, "scripts": scripts,
	}
}

func (r *Runtime) importantFilesFromInventory(entries []repoInventoryEntry) []map[string]any {
	names := map[string]bool{
		"readme.md": true, "agents.md": true, "contributing.md": true, "license": true,
		"go.mod": true, "go.sum": true, "package.json": true, "package-lock.json": true,
		"pnpm-lock.yaml": true, "yarn.lock": true, "cargo.toml": true, "pyproject.toml": true,
		"dockerfile": true, "docker-compose.yml": true, "makefile": true, ".gitignore": true,
		"tsconfig.json": true, "vite.config.ts": true, "next.config.js": true,
	}
	out := make([]map[string]any, 0, 32)
	for _, entry := range entries {
		if entry.Type != "file" || entry.Depth > 4 {
			continue
		}
		base := strings.ToLower(filepath.Base(entry.Path))
		if !names[base] && !strings.HasPrefix(entry.RootRelative, ".github/workflows/") {
			continue
		}
		out = append(out, map[string]any{"path": entry.Path, "size": entry.Size})
		if len(out) >= 100 {
			break
		}
	}
	return out
}

func treeFromInventory(entries []repoInventoryEntry, depth, limit int) ([]string, int, int) {
	depth, limit = normalizeRepoIndexParams(depth, limit)
	tree := make([]string, 0, min(limit, len(entries)))
	dirs, files := 0, 0
	for _, entry := range entries {
		if entry.Depth > depth {
			continue
		}
		path := entry.Path
		if entry.Type == "directory" {
			dirs++
			path += "/"
		} else {
			files++
		}
		tree = append(tree, path)
		if len(tree) >= limit {
			break
		}
	}
	return tree, dirs, files
}

func (r *Runtime) cachedRepoView(key repoViewKey) (repoIndex, bool) {
	r.repoCacheMu.Lock()
	defer r.repoCacheMu.Unlock()
	if r.repoViews == nil {
		return repoIndex{}, false
	}
	value, ok := r.repoViews[key]
	return value, ok
}

func (r *Runtime) storeRepoView(key repoViewKey, value repoIndex) {
	r.repoCacheMu.Lock()
	defer r.repoCacheMu.Unlock()
	if r.repoGeneration != key.Generation {
		return
	}
	if r.repoViews == nil {
		r.repoViews = map[repoViewKey]repoIndex{}
	}
	store := func(candidate repoViewKey) {
		if _, exists := r.repoViews[candidate]; !exists && len(r.repoViews) >= repoViewCacheLimit {
			var oldestKey repoViewKey
			var oldest time.Time
			first := true
			for existing, entry := range r.repoViews {
				if first || entry.TS.Before(oldest) {
					oldestKey, oldest, first = existing, entry.TS, false
				}
			}
			delete(r.repoViews, oldestKey)
		}
		r.repoViews[candidate] = value
	}
	store(key)
	if key.Symbols {
		subsetKey := key
		subsetKey.Symbols = false
		store(subsetKey)
	}
}

func decodeJSON(raw []byte, target any) error {
	// Kept local to the repository cache to make package.json parsing easy to
	// replace with a bounded decoder later without changing inventory callers.
	return json.Unmarshal(raw, target)
}

var extensionLanguages = map[string]string{
	".go": "go", ".js": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".ts": "typescript", ".tsx": "typescript", ".jsx": "javascript", ".py": "python",
	".rs": "rust", ".java": "java", ".kt": "kotlin", ".dart": "dart", ".rb": "ruby",
	".php": "php", ".cs": "csharp", ".cpp": "cpp", ".c": "c", ".swift": "swift",
}
