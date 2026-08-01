// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package adminauth owns local Admin UI credentials and browser sessions.
package adminauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	credentialSchemaVersion = 1
	passwordAlgorithm       = "pbkdf2-sha256"
	defaultIterations       = 600_000
	passwordKeyBytes        = 32
	saltBytes               = 16
	credentialVersionBytes  = 16
	MinPasswordLength       = 8
	MaxPasswordLength       = 1024
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var (
	ErrNotConfigured     = errors.New("admin credentials are not configured")
	ErrAlreadyConfigured = errors.New("admin credentials are already configured")
)

// Credentials is the owner-only persisted local Admin UI identity. It stores
// only a salted one-way password derivation and a random version used to
// invalidate browser sessions after a CLI password reset.
type Credentials struct {
	SchemaVersion     int       `json:"schemaVersion"`
	Username          string    `json:"username"`
	Algorithm         string    `json:"algorithm"`
	Iterations        int       `json:"iterations"`
	Salt              string    `json:"salt"`
	PasswordHash      string    `json:"passwordHash"`
	CredentialVersion string    `json:"credentialVersion"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// ValidateCredentialInput applies the public username and password constraints
// without reading or writing credential state.
func ValidateCredentialInput(username, password string) error {
	username = strings.TrimSpace(username)
	if !usernamePattern.MatchString(username) {
		return errors.New("admin username must be 1-64 characters using letters, digits, dot, underscore, or hyphen")
	}
	if len(password) < MinPasswordLength {
		return fmt.Errorf("admin password must contain at least %d characters", MinPasswordLength)
	}
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("admin password must not exceed %d characters", MaxPasswordLength)
	}
	return nil
}

// SetCredentials validates, hashes, and atomically stores a local Admin UI
// username and password in an owner-only file, replacing an existing account.
func SetCredentials(path, username, password string) (Credentials, error) {
	return setCredentials(path, username, password, defaultIterations)
}

// SetInitialCredentials creates the first local Admin UI account without ever
// replacing an existing credential file. The exclusive create keeps concurrent
// first-run requests from resetting an account that another request just made.
func SetInitialCredentials(path, username, password string) (Credentials, error) {
	if _, err := LoadCredentials(path); err == nil {
		return Credentials{}, ErrAlreadyConfigured
	} else if !errors.Is(err, ErrNotConfigured) {
		return Credentials{}, err
	}
	return storeCredentials(path, username, password, defaultIterations, false)
}

func setCredentials(path, username, password string, iterations int) (Credentials, error) {
	return storeCredentials(path, username, password, iterations, true)
}

func storeCredentials(path, username, password string, iterations int, replace bool) (Credentials, error) {
	username = strings.TrimSpace(username)
	if err := ValidateCredentialInput(username, password); err != nil {
		return Credentials{}, err
	}
	if iterations < 10_000 {
		return Credentials{}, errors.New("password iteration count is too low")
	}

	salt := make([]byte, saltBytes)
	version := make([]byte, credentialVersionBytes)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return Credentials{}, fmt.Errorf("generate password salt: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, version); err != nil {
		return Credentials{}, fmt.Errorf("generate credential version: %w", err)
	}
	hash := deriveKey([]byte(password), salt, iterations, passwordKeyBytes)
	credential := Credentials{
		SchemaVersion:     credentialSchemaVersion,
		Username:          username,
		Algorithm:         passwordAlgorithm,
		Iterations:        iterations,
		Salt:              base64.RawStdEncoding.EncodeToString(salt),
		PasswordHash:      base64.RawStdEncoding.EncodeToString(hash),
		CredentialVersion: base64.RawStdEncoding.EncodeToString(version),
		UpdatedAt:         time.Now().UTC(),
	}
	raw, err := json.MarshalIndent(credential, "", "  ")
	if err != nil {
		return Credentials{}, err
	}
	data := append(raw, '\n')
	var saveErr error
	if replace {
		saveErr = atomicWrite(path, data, 0o600)
	} else {
		saveErr = exclusiveWrite(path, data, 0o600)
	}
	if saveErr != nil {
		return Credentials{}, fmt.Errorf("save admin credentials: %w", saveErr)
	}
	return credential, nil
}

// LoadCredentials reads and validates the local Admin UI credential document.
func LoadCredentials(path string) (Credentials, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, ErrNotConfigured
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("read admin credentials: %w", err)
	}
	var credential Credentials
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credential); err != nil {
		return Credentials{}, fmt.Errorf("parse admin credentials: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Credentials{}, errors.New("admin credential document contains trailing data; reset it with the Codebridge CLI")
	}
	if credential.SchemaVersion != credentialSchemaVersion || credential.Algorithm != passwordAlgorithm {
		return Credentials{}, errors.New("admin credential format is unsupported; reset it with the Codebridge CLI")
	}
	if !usernamePattern.MatchString(credential.Username) || credential.Iterations < 10_000 || credential.Iterations > 5_000_000 {
		return Credentials{}, errors.New("admin credential document is invalid; reset it with the Codebridge CLI")
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(credential.Salt)
	hash, hashErr := base64.RawStdEncoding.DecodeString(credential.PasswordHash)
	version, versionErr := base64.RawStdEncoding.DecodeString(credential.CredentialVersion)
	if saltErr != nil || hashErr != nil || versionErr != nil || len(salt) != saltBytes || len(hash) != passwordKeyBytes || len(version) != credentialVersionBytes {
		return Credentials{}, errors.New("admin credential document is invalid; reset it with the Codebridge CLI")
	}
	return credential, nil
}

// VerifyPassword performs a constant-time username and password check.
func VerifyPassword(credential Credentials, username, password string) bool {
	salt, saltErr := base64.RawStdEncoding.DecodeString(credential.Salt)
	want, hashErr := base64.RawStdEncoding.DecodeString(credential.PasswordHash)
	if saltErr != nil || hashErr != nil || len(want) != passwordKeyBytes {
		return false
	}
	got := deriveKey([]byte(password), salt, credential.Iterations, len(want))
	providedUsername := sha256.Sum256([]byte(strings.TrimSpace(username)))
	storedUsername := sha256.Sum256([]byte(credential.Username))
	usernameOK := subtle.ConstantTimeCompare(providedUsername[:], storedUsername[:])
	hashOK := subtle.ConstantTimeCompare(got, want)
	return usernameOK&hashOK == 1
}

// deriveKey implements PBKDF2-HMAC-SHA256 without introducing a separate
// crypto dependency. The persisted algorithm and iteration count are explicit
// so a future CLI migration can raise costs or move to another KDF.
func deriveKey(password, salt []byte, iterations, keyLength int) []byte {
	hashLength := sha256.Size
	blocks := (keyLength + hashLength - 1) / hashLength
	result := make([]byte, 0, blocks*hashLength)
	counter := make([]byte, 4)
	for block := 1; block <= blocks; block++ {
		binary.BigEndian.PutUint32(counter, uint32(block))
		mac := hmac.New(sha256.New, password)
		_, _ = mac.Write(salt)
		_, _ = mac.Write(counter)
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for iteration := 1; iteration < iterations; iteration++ {
			mac = hmac.New(sha256.New, password)
			_, _ = mac.Write(u)
			u = mac.Sum(nil)
			for index := range t {
				t[index] ^= u[index]
			}
		}
		result = append(result, t...)
	}
	return result[:keyLength]
}

func exclusiveWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if errors.Is(err, os.ErrExist) {
		return ErrAlreadyConfigured
	}
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".admin-auth-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		if runtime.GOOS != "windows" {
			return err
		}
		// Windows os.Rename does not replace an existing destination. Keep the
		// fallback narrow and owner-only, matching the CLI's atomic-file behavior.
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
		if retryErr := os.Rename(name, path); retryErr != nil {
			return retryErr
		}
	}
	return os.Chmod(path, mode)
}
