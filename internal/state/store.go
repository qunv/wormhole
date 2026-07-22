// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"os"
	"path/filepath"
	"sync"

	"codebridge/internal/config"
)

type Store struct {
	DataDir      string
	WorkspaceDir string
	NotesPath    string
	Checkpoint   string
	IndexPath    string
	PatchHistory string
	BackupsDir   string
	ApprovalsDir string
	AuditPath    string
}

var statePathLocks [256]sync.Mutex

func New(workspace string) (*Store, error) {
	return NewAt(workspace, config.AppDataDir())
}

func NewAt(workspace, dataDir string) (*Store, error) {
	sum := sha256.Sum256([]byte(comparePath(workspace)))
	id := hex.EncodeToString(sum[:8])
	if dataDir == "" {
		dataDir = config.AppDataDir()
	}
	workspaceDir := filepath.Join(dataDir, "workspaces", id)
	store := &Store{
		DataDir:      dataDir,
		WorkspaceDir: workspaceDir,
		NotesPath:    filepath.Join(workspaceDir, "notes.json"),
		Checkpoint:   filepath.Join(workspaceDir, "checkpoint.json"),
		IndexPath:    filepath.Join(workspaceDir, "index.json"),
		PatchHistory: filepath.Join(workspaceDir, "patch-history.json"),
		BackupsDir:   filepath.Join(workspaceDir, "backups"),
		ApprovalsDir: filepath.Join(workspaceDir, "approvals"),
		AuditPath:    filepath.Join(dataDir, "audit.log"),
	}
	for _, dir := range []string{dataDir, workspaceDir, store.BackupsDir, store.ApprovalsDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) ReadJSON(path string, target any) error {
	pathLock := statePathLock(path)
	pathLock.Lock()
	defer pathLock.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func (s *Store) WriteJSON(path string, value any) error {
	pathLock := statePathLock(path)
	pathLock.Lock()
	defer pathLock.Unlock()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(raw, '\n'), 0o600)
}

func (s *Store) AppendLine(path string, line []byte) error {
	pathLock := statePathLock(path)
	pathLock.Lock()
	defer pathLock.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(line)
	return err
}

func statePathLock(path string) *sync.Mutex {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(comparePath(filepath.Clean(path))))
	return &statePathLocks[hash.Sum32()%uint32(len(statePathLocks))]
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
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
	return os.Rename(name, path)
}
