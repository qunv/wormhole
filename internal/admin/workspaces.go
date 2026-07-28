// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/workspaceregistry"
)

const maxBrowseDirectories = 200

type createWorkspaceInput struct {
	ID        string `json:"id"`
	Workspace string `json:"workspace"`
}

type browseDirectory struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Git       bool   `json:"git"`
	Suggested string `json:"suggestedId"`
}

// browseWorkspaces exposes a bounded directory-only browser rooted at the
// current user's home directory. Arbitrary paths can still be entered when a
// workspace is created, but browsing never reveals filenames outside home.
func (h *Handler) browseWorkspaces(writer http.ResponseWriter, request *http.Request) {
	home, err := os.UserHomeDir()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "browse_home_failed", err.Error())
		return
	}
	home, err = canonicalDirectory(home)
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "browse_home_failed", err.Error())
		return
	}
	requested := strings.TrimSpace(request.URL.Query().Get("path"))
	if requested == "" {
		requested = home
	} else if !filepath.IsAbs(requested) {
		requested = filepath.Join(home, requested)
	}
	directory, err := canonicalDirectory(requested)
	if err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "browse_path_invalid", err.Error())
		return
	}
	if !pathWithin(home, directory) {
		h.sendError(writer, http.StatusForbidden, "browse_outside_home", "The directory browser is restricted to the current user's home directory.")
		return
	}

	showHidden := request.URL.Query().Get("showHidden") == "true"
	entries, err := os.ReadDir(directory)
	if err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "browse_read_failed", err.Error())
		return
	}
	items := make([]browseDirectory, 0, min(len(entries), maxBrowseDirectories))
	truncated := false
	for _, entry := range entries {
		name := entry.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		if len(items) == maxBrowseDirectories {
			truncated = true
			break
		}
		child := filepath.Join(directory, name)
		items = append(items, browseDirectory{
			Name: name, Path: child, Git: isGitWorkspace(child), Suggested: workspaceregistry.IDFromPath(child),
		})
	}
	sort.Slice(items, func(left, right int) bool {
		return strings.ToLower(items[left].Name) < strings.ToLower(items[right].Name)
	})
	var parent any
	if !sameDirectory(directory, home) {
		candidate := filepath.Dir(directory)
		if pathWithin(home, candidate) {
			parent = candidate
		}
	}
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"root": home, "path": directory, "parent": parent, "directories": items,
		"showHidden": showHidden, "truncated": truncated, "limit": maxBrowseDirectories,
		"selected": map[string]any{
			"path": directory, "git": isGitWorkspace(directory),
			"suggestedId": workspaceregistry.IDFromPath(directory),
		},
	})
}

func (h *Handler) createWorkspace(writer http.ResponseWriter, request *http.Request) {
	raw, err := readBody(writer, request, maxJSONBody)
	if err != nil {
		h.sendError(writer, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	var input createWorkspaceInput
	if err := json.Unmarshal(raw, &input); err != nil {
		h.sendError(writer, http.StatusBadRequest, "invalid_body", "Expected a JSON object containing id and workspace.")
		return
	}
	root, err := canonicalDirectory(input.Workspace)
	if err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "workspace_invalid", err.Error())
		return
	}
	if sameDirectory(root, h.Runtime.Workspace.Primary) {
		h.sendError(writer, http.StatusConflict, "workspace_is_primary", "The primary workspace is already active and cannot be registered as a named workspace.")
		return
	}
	id := workspaceregistry.NormalizeID(input.ID)
	if id == "" {
		id = workspaceregistry.IDFromPath(root)
	}
	if err := workspaceregistry.ValidateID(id); err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "workspace_id_invalid", err.Error())
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	currentRevision, err := fileRevision(workspaceregistry.Path())
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "registry_read_failed", err.Error())
		return
	}
	if !etagMatches(request.Header.Get("If-Match"), currentRevision) {
		h.sendError(writer, http.StatusPreconditionFailed, "revision_conflict", "The workspace registry changed after it was loaded. Reload before adding a workspace.")
		return
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "registry_read_failed", err.Error())
		return
	}
	if _, exists := registry.Workspaces[id]; exists {
		h.sendError(writer, http.StatusConflict, "workspace_exists", fmt.Sprintf("Workspace %q is already registered.", id))
		return
	}
	for _, existing := range registry.Workspaces {
		if sameDirectory(root, existing.Workspace) {
			h.sendError(writer, http.StatusConflict, "workspace_path_exists", fmt.Sprintf("This directory is already registered as workspace %q.", existing.ID))
			return
		}
	}

	now := time.Now().UTC()
	entry := workspaceregistry.Registration{
		ID: id, Workspace: root, ConfigPath: workspaceregistry.ConfigPath(id),
		DataDir: workspaceregistry.DataDir(id), Port: h.Runtime.Config.Port,
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	base, err := h.editableConfig()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "config_read_failed", err.Error())
		return
	}
	base.AuthToken = h.Runtime.Config.AuthToken
	base.ApprovalToken = h.Runtime.Config.ApprovalToken

	snapshot, err := captureAdminFile(entry.ConfigPath)
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "workspace_snapshot_failed", err.Error())
		return
	}
	override := map[string]any{"extraRoots": []any{}}
	if snapshot.Exists {
		override, err = config.ReadOverrideFile(entry.ConfigPath)
		if err != nil {
			h.sendError(writer, http.StatusUnprocessableEntity, "workspace_config_invalid", err.Error())
			return
		}
		for field := range override {
			if workspaceOwnedFields[field] {
				delete(override, field)
			}
		}
	}
	if _, err := effectiveWorkspaceConfig(base, entry, override); err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "workspace_invalid", err.Error())
		return
	}
	if err := config.SaveOverrideFile(entry.ConfigPath, base, override); err != nil {
		h.sendError(writer, http.StatusUnprocessableEntity, "workspace_save_failed", err.Error())
		return
	}
	latestRevision, err := fileRevision(workspaceregistry.Path())
	if err != nil || latestRevision != currentRevision {
		_ = snapshot.Restore()
		h.sendError(writer, http.StatusPreconditionFailed, "revision_conflict", "The workspace registry changed while the workspace was being created. Reload and try again.")
		return
	}
	registry.Workspaces[id] = entry
	if err := workspaceregistry.Save(registry); err != nil {
		rollbackErr := snapshot.Restore()
		message := fmt.Sprintf("save workspace registry: %v", err)
		if rollbackErr != nil {
			message += fmt.Sprintf("; rollback workspace config: %v", rollbackErr)
		}
		h.sendError(writer, http.StatusInternalServerError, "workspace_save_failed", message)
		return
	}
	newRevision, err := fileRevision(workspaceregistry.Path())
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "registry_read_failed", err.Error())
		return
	}
	writer.Header().Set("ETag", quoteETag(newRevision))
	h.sendJSON(writer, http.StatusCreated, map[string]any{
		"workspace": entry, "revision": newRevision, "restartRequired": true,
		"message": "Workspace registered. Restart Codebridge to activate its MCP endpoint.",
	})
}

func (h *Handler) deleteWorkspace(writer http.ResponseWriter, request *http.Request, original workspaceregistry.Registration) {
	purgeConfig := request.URL.Query().Get("deleteConfig") == "true"
	h.mu.Lock()
	defer h.mu.Unlock()
	currentRevision, err := fileRevision(workspaceregistry.Path())
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "registry_read_failed", err.Error())
		return
	}
	if !etagMatches(request.Header.Get("If-Match"), currentRevision) {
		h.sendError(writer, http.StatusPreconditionFailed, "revision_conflict", "The workspace registry changed after it was loaded. Reload before removing a workspace.")
		return
	}
	registry, err := workspaceregistry.Load()
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "registry_read_failed", err.Error())
		return
	}
	entry, exists := registry.Workspaces[original.ID]
	if !exists {
		h.sendError(writer, http.StatusNotFound, "workspace_not_found", "Workspace is no longer registered.")
		return
	}
	var snapshot adminFileSnapshot
	if purgeConfig {
		snapshot, err = captureAdminFile(entry.ConfigPath)
		if err != nil {
			h.sendError(writer, http.StatusInternalServerError, "workspace_snapshot_failed", err.Error())
			return
		}
		if err := os.Remove(entry.ConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			h.sendError(writer, http.StatusInternalServerError, "workspace_delete_failed", err.Error())
			return
		}
	}
	delete(registry.Workspaces, entry.ID)
	if err := workspaceregistry.Save(registry); err != nil {
		if purgeConfig {
			_ = snapshot.Restore()
		}
		h.sendError(writer, http.StatusInternalServerError, "workspace_delete_failed", err.Error())
		return
	}
	newRevision, err := fileRevision(workspaceregistry.Path())
	if err != nil {
		h.sendError(writer, http.StatusInternalServerError, "registry_read_failed", err.Error())
		return
	}
	_, active := h.Runtimes[entry.ID]
	writer.Header().Set("ETag", quoteETag(newRevision))
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"id": entry.ID, "removed": true, "configDeleted": purgeConfig,
		"statePreserved": true, "activeUntilRestart": active,
		"revision": newRevision, "restartRequired": active,
	})
}

func canonicalDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("workspace path is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("workspace does not exist: %s", absolute)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("workspace is not a directory: %s", resolved)
	}
	return filepath.Clean(resolved), nil
}

func pathWithin(root, target string) bool {
	root = comparableDirectory(root)
	target = comparableDirectory(target)
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func sameDirectory(left, right string) bool {
	leftResolved, leftErr := canonicalDirectory(left)
	rightResolved, rightErr := canonicalDirectory(right)
	if leftErr == nil {
		left = leftResolved
	}
	if rightErr == nil {
		right = rightResolved
	}
	return comparableDirectory(left) == comparableDirectory(right)
}

func comparableDirectory(value string) string {
	value = filepath.Clean(strings.TrimSpace(value))
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return value
}

func isGitWorkspace(directory string) bool {
	_, err := os.Stat(filepath.Join(directory, ".git"))
	return err == nil
}

type adminFileSnapshot struct {
	Path   string
	Exists bool
	Data   []byte
	Mode   os.FileMode
}

func captureAdminFile(path string) (adminFileSnapshot, error) {
	snapshot := adminFileSnapshot{Path: path, Mode: 0o600}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return snapshot, err
	}
	snapshot.Exists = true
	snapshot.Data = raw
	snapshot.Mode = info.Mode().Perm()
	return snapshot, nil
}

func (snapshot adminFileSnapshot) Restore() error {
	if !snapshot.Exists {
		if err := os.Remove(snapshot.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeAdminFileAtomic(snapshot.Path, snapshot.Data, snapshot.Mode)
}

func writeAdminFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".admin-rollback-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		if runtime.GOOS != "windows" {
			return err
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
		if retryErr := os.Rename(tempPath, path); retryErr != nil {
			return retryErr
		}
	}
	committed = true
	return nil
}
