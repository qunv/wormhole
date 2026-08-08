// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package adminauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	SessionTTL         = 12 * time.Hour
	maxSessions        = 64
	failureWindow      = 5 * time.Minute
	maxFailures        = 5
	failureBlockPeriod = 30 * time.Second
)

var (
	ErrInvalidCredentials = errors.New("invalid admin username or password")
	ErrRateLimited        = errors.New("too many failed admin login attempts")
)

type session struct {
	Username          string
	CredentialVersion string
	ExpiresAt         time.Time
}

type failureState struct {
	WindowStart  time.Time
	Attempts     int
	BlockedUntil time.Time
}

// Manager provides bounded in-memory Admin UI sessions while re-reading the
// credential version on validation so a CLI password reset immediately
// invalidates every existing browser session.
type Manager struct {
	path     string
	loginMu  sync.Mutex
	mu       sync.Mutex
	sessions map[[sha256.Size]byte]session
	failure  failureState
	now      func() time.Time
}

func NewManager(path string) *Manager {
	return &Manager{path: path, sessions: make(map[[sha256.Size]byte]session), now: time.Now}
}

// Status returns whether credentials exist and whether the supplied session
// token is currently authenticated.
func (m *Manager) Status(token string) (configured bool, authenticated bool, username string, err error) {
	credential, err := LoadCredentials(m.path)
	if errors.Is(err, ErrNotConfigured) {
		return false, false, "", nil
	}
	if err != nil {
		return false, false, "", err
	}
	configured, username = true, credential.Username
	if token == "" {
		return configured, false, username, nil
	}
	authenticated, _, err = m.Validate(token)
	return configured, authenticated, username, err
}

// Login verifies credentials, applies a bounded failed-attempt throttle, and
// creates a random session token. RetryAfter is non-zero when rate limited.
func (m *Manager) Login(username, password string) (token string, authenticatedUsername string, retryAfter time.Duration, err error) {
	// Password verification is deliberately serialized. This keeps the expensive
	// KDF bounded and prevents concurrent attempts from racing past the failure
	// threshold before each one records its result.
	m.loginMu.Lock()
	defer m.loginMu.Unlock()

	now := m.now()
	m.mu.Lock()
	m.pruneLocked(now)
	if now.Before(m.failure.BlockedUntil) {
		retryAfter = m.failure.BlockedUntil.Sub(now)
		m.mu.Unlock()
		return "", "", retryAfter, ErrRateLimited
	}
	m.mu.Unlock()

	credential, loadErr := LoadCredentials(m.path)
	if loadErr != nil {
		return "", "", 0, loadErr
	}
	if !VerifyPassword(credential, username, password) {
		m.recordFailure(now)
		return "", "", 0, ErrInvalidCredentials
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", 0, fmt.Errorf("generate admin session: %w", err)
	}
	token = hex.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	m.mu.Lock()
	m.failure = failureState{}
	m.pruneLocked(now)
	if len(m.sessions) >= maxSessions {
		var oldestKey [sha256.Size]byte
		oldest := now.Add(SessionTTL)
		for key, current := range m.sessions {
			if current.ExpiresAt.Before(oldest) {
				oldestKey, oldest = key, current.ExpiresAt
			}
		}
		delete(m.sessions, oldestKey)
	}
	m.sessions[digest] = session{
		Username: credential.Username, CredentialVersion: credential.CredentialVersion,
		ExpiresAt: now.Add(SessionTTL),
	}
	m.mu.Unlock()
	return token, credential.Username, 0, nil
}

// Validate checks a random session token and the current persisted credential
// version. Invalid or expired tokens are removed from the bounded session map.
func (m *Manager) Validate(token string) (bool, string, error) {
	raw, err := hex.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return false, "", nil
	}
	digest := sha256.Sum256([]byte(token))
	now := m.now()
	m.mu.Lock()
	m.pruneLocked(now)
	current, exists := m.sessions[digest]
	m.mu.Unlock()
	if !exists || !now.Before(current.ExpiresAt) {
		return false, "", nil
	}
	credential, err := LoadCredentials(m.path)
	if errors.Is(err, ErrNotConfigured) {
		m.Logout(token)
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if current.CredentialVersion != credential.CredentialVersion || current.Username != credential.Username {
		m.Logout(token)
		return false, "", nil
	}
	return true, current.Username, nil
}

func (m *Manager) Logout(token string) {
	digest := sha256.Sum256([]byte(token))
	m.mu.Lock()
	delete(m.sessions, digest)
	m.mu.Unlock()
}

func (m *Manager) recordFailure(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failure.WindowStart.IsZero() || now.Sub(m.failure.WindowStart) > failureWindow {
		m.failure = failureState{WindowStart: now}
	}
	m.failure.Attempts++
	if m.failure.Attempts >= maxFailures {
		m.failure.BlockedUntil = now.Add(failureBlockPeriod)
	}
}

func (m *Manager) pruneLocked(now time.Time) {
	for key, current := range m.sessions {
		if !now.Before(current.ExpiresAt) {
			delete(m.sessions, key)
		}
	}
	if !m.failure.WindowStart.IsZero() && now.Sub(m.failure.WindowStart) > failureWindow && !now.Before(m.failure.BlockedUntil) {
		m.failure = failureState{}
	}
}
