// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"errors"
	"fmt"
	"os"

	"codebridge/internal/config"
	"codebridge/internal/workspaceregistry"
)

var (
	saveWorkspaceOverride = config.SaveOverrideFile
	saveWorkspaceRegistry = workspaceregistry.Save
	removeWorkspaceConfig = os.Remove
)

type workspaceFileSnapshot struct {
	path   string
	exists bool
	data   []byte
	mode   os.FileMode
}

func captureWorkspaceFile(path string) (workspaceFileSnapshot, error) {
	snapshot := workspaceFileSnapshot{path: path, mode: 0o600}
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
	snapshot.exists = true
	snapshot.data = raw
	snapshot.mode = info.Mode().Perm()
	return snapshot, nil
}

func wrapOptionalError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func (snapshot workspaceFileSnapshot) restore() error {
	if !snapshot.exists {
		if err := os.Remove(snapshot.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return atomicWriteFile(snapshot.path, snapshot.data, snapshot.mode)
}
