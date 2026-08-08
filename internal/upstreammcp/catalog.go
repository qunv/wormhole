// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package upstreammcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"wormhole/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	toolCatalogVersion  = 1
	maxToolCatalogBytes = 8 << 20
	maxCatalogTools     = 1_000
	maxToolCatalogFiles = 64
	toolCatalogMaxAge   = 90 * 24 * time.Hour
)

var toolCatalogKeyPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// ToolCatalog is the persistent, secret-free tool contract discovered from an
// upstream MCP server. It contains schemas and annotations only; calls,
// arguments, results, credentials, and transport environment are never stored.
type ToolCatalog struct {
	Version int         `json:"version"`
	SavedAt time.Time   `json:"savedAt"`
	Tools   []*mcp.Tool `json:"tools"`
}

// ToolCatalogPath returns the persistent path for one upstream connection key.
func ToolCatalogPath(key string) (string, error) {
	if !toolCatalogKeyPattern.MatchString(key) {
		return "", errors.New("invalid upstream MCP tool catalog key")
	}
	return filepath.Join(config.AppDataDir(), "upstream-mcp", "catalogs", key+".json"), nil
}

// LoadToolCatalog loads and validates a bounded upstream tool catalog.
func LoadToolCatalog(key string) (ToolCatalog, error) {
	path, err := ToolCatalogPath(key)
	if err != nil {
		return ToolCatalog{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return ToolCatalog{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ToolCatalog{}, err
	}
	if !info.Mode().IsRegular() {
		return ToolCatalog{}, errors.New("upstream MCP tool catalog is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > maxToolCatalogBytes {
		return ToolCatalog{}, fmt.Errorf("upstream MCP tool catalog size must be between 1 and %d bytes", maxToolCatalogBytes)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxToolCatalogBytes+1))
	var catalog ToolCatalog
	if err := decoder.Decode(&catalog); err != nil {
		return ToolCatalog{}, fmt.Errorf("parse upstream MCP tool catalog: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ToolCatalog{}, err
	}
	if err := validateToolCatalog(catalog); err != nil {
		return ToolCatalog{}, err
	}
	catalog.Tools = minimalCatalogTools(catalog.Tools)
	return catalog, nil
}

// SaveToolCatalog atomically persists a validated upstream tool catalog.
func SaveToolCatalog(key string, tools []*mcp.Tool) error {
	path, err := ToolCatalogPath(key)
	if err != nil {
		return err
	}
	catalog := ToolCatalog{
		Version: toolCatalogVersion,
		SavedAt: time.Now().UTC(),
		Tools:   minimalCatalogTools(tools),
	}
	if err := validateToolCatalog(catalog); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal upstream MCP tool catalog: %w", err)
	}
	raw = append(raw, '\n')
	if len(raw) > maxToolCatalogBytes {
		return fmt.Errorf("upstream MCP tool catalog exceeds %d bytes", maxToolCatalogBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".catalog-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	pruneToolCatalogs(filepath.Dir(path), path)
	return nil
}

func pruneToolCatalogs(dir, keepPath string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type catalogFile struct {
		path    string
		modTime time.Time
	}
	files := make([]catalogFile, 0, len(entries))
	cutoff := time.Now().Add(-toolCatalogMaxAge)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			continue
		}
		if path != keepPath && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
			continue
		}
		files = append(files, catalogFile{path: path, modTime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	for index := maxToolCatalogFiles; index < len(files); index++ {
		if files[index].path != keepPath {
			_ = os.Remove(files[index].path)
		}
	}
}

func minimalCatalogTools(tools []*mcp.Tool) []*mcp.Tool {
	minimal := make([]*mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			minimal = append(minimal, nil)
			continue
		}
		copy := &mcp.Tool{
			Name: tool.Name, Title: tool.Title, Description: tool.Description,
			InputSchema: tool.InputSchema,
		}
		if tool.Annotations != nil {
			annotations := *tool.Annotations
			copy.Annotations = &annotations
		}
		minimal = append(minimal, copy)
	}
	return minimal
}

func validateToolCatalog(catalog ToolCatalog) error {
	if catalog.Version != toolCatalogVersion {
		return fmt.Errorf("unsupported upstream MCP tool catalog version %d", catalog.Version)
	}
	if len(catalog.Tools) == 0 || len(catalog.Tools) > maxCatalogTools {
		return fmt.Errorf("upstream MCP tool catalog must contain between 1 and %d tools", maxCatalogTools)
	}
	seen := make(map[string]bool, len(catalog.Tools))
	for index, tool := range catalog.Tools {
		if tool == nil || tool.Name == "" {
			return fmt.Errorf("upstream MCP tool catalog entry %d has no tool name", index)
		}
		if seen[tool.Name] {
			return fmt.Errorf("upstream MCP tool catalog contains duplicate tool %q", tool.Name)
		}
		seen[tool.Name] = true
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("parse upstream MCP tool catalog trailing data: %w", err)
	}
	return errors.New("upstream MCP tool catalog contains trailing JSON")
}
