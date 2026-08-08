// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"wormhole/internal/config"
)

const (
	lifecycleLockPollInterval        = 100 * time.Millisecond
	lifecycleLockInitializationGrace = 5 * time.Second
	lifecycleLockWaitTimeout         = 2 * time.Minute
)

type lifecycleLockRecord struct {
	PID       int       `json:"pid"`
	Identity  string    `json:"identity"`
	Token     string    `json:"token"`
	Operation string    `json:"operation"`
	Acquired  time.Time `json:"acquiredAt"`
}

func withLifecycleLock(ctx context.Context, operation string, run func() error) error {
	release, err := acquireLifecycleLock(ctx, operation)
	if err != nil {
		return err
	}
	defer release()
	return run()
}

func acquireLifecycleLock(ctx context.Context, operation string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, lifecycleLockWaitTimeout)
		defer cancel()
	}
	identity, err := processIdentity(os.Getpid())
	if err != nil {
		return nil, fmt.Errorf("capture lifecycle lock owner identity: %w", err)
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate lifecycle lock token: %w", err)
	}
	record := lifecycleLockRecord{
		PID: os.Getpid(), Identity: identity, Token: hex.EncodeToString(tokenBytes),
		Operation: operation, Acquired: time.Now().UTC(),
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(config.AppDataDir(), "lifecycle.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	for {
		file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr == nil {
			if _, err := file.Write(append(raw, '\n')); err != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, err
			}
			if err := file.Sync(); err != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, err
			}
			if err := file.Close(); err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			return func() { releaseLifecycleLock(path, record.Token) }, nil
		}
		if !errors.Is(openErr, os.ErrExist) {
			return nil, fmt.Errorf("acquire lifecycle lock: %w", openErr)
		}

		existing, readErr := readLifecycleLock(path)
		if readErr != nil {
			if lifecycleLockIsInitializing(path) {
				select {
				case <-ctx.Done():
					return nil, fmt.Errorf("lifecycle lock is busy while another process initializes it: %w", ctx.Err())
				case <-time.After(lifecycleLockPollInterval):
					continue
				}
			}
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return nil, fmt.Errorf("remove malformed lifecycle lock: %w", removeErr)
			}
			continue
		}
		if !processMatches(existing.PID, existing.Identity) {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return nil, fmt.Errorf("remove stale lifecycle lock: %w", removeErr)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("lifecycle operation %q is already running in PID %d: %w", existing.Operation, existing.PID, ctx.Err())
		case <-time.After(lifecycleLockPollInterval):
		}
	}
}

func lifecycleLockIsInitializing(path string) bool {
	info, err := os.Stat(path)
	return err == nil && time.Since(info.ModTime()) < lifecycleLockInitializationGrace
}

func readLifecycleLock(path string) (lifecycleLockRecord, error) {
	var record lifecycleLockRecord
	raw, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, err
	}
	return record, nil
}

func releaseLifecycleLock(path, token string) {
	record, err := readLifecycleLock(path)
	if err == nil && record.Token == token {
		_ = os.Remove(path)
	}
}
