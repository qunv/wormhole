// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"codebridge/internal/config"
	"codebridge/internal/database"
	"codebridge/internal/database/sqlcore"

	_ "modernc.org/sqlite"
)

// New opens an existing SQLite file through a root-confined, read-only URI.
// The credential value is a filesystem path resolved from credentialRef.
func New(_ string, cfg config.DatabaseConnectionConfig, credential string) (database.Connection, error) {
	path, err := validateDatabasePath(credential, cfg.FileRoot)
	if err != nil {
		return nil, err
	}
	dsn := sqliteReadOnlyDSN(path, cfg.Limits.QueryTimeoutMS)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	client, err := sqlcore.NewWithDB(db, cfg, Dialect{})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return client, nil
}

func validateDatabasePath(credential, root string) (string, error) {
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return "", fmt.Errorf("SQLite database path is required")
	}
	lower := strings.ToLower(credential)
	if credential == ":memory:" || strings.Contains(lower, "mode=memory") {
		return "", fmt.Errorf("SQLite memory databases are not allowed")
	}
	if strings.ContainsAny(credential, "?#") || strings.HasPrefix(lower, "file:") {
		return "", fmt.Errorf("SQLite credential must be a plain filesystem path without URI parameters")
	}
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("SQLite fileRoot is required")
	}

	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("SQLite fileRoot cannot be resolved")
	}
	canonicalRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return "", fmt.Errorf("SQLite fileRoot cannot be resolved")
	}
	rootInfo, err := os.Stat(canonicalRoot)
	if err != nil || !rootInfo.IsDir() {
		return "", fmt.Errorf("SQLite fileRoot is not an existing directory")
	}

	path := credential
	if !filepath.IsAbs(path) {
		path = filepath.Join(canonicalRoot, path)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("SQLite database path cannot be resolved")
	}
	canonicalPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", fmt.Errorf("SQLite database path cannot be resolved")
	}
	info, err := os.Stat(canonicalPath)
	if err != nil {
		return "", fmt.Errorf("SQLite database file is unavailable")
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("SQLite database must be a regular file")
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("SQLite database is outside configured fileRoot")
	}
	return canonicalPath, nil
}

func sqliteReadOnlyDSN(path string, timeoutMS int) string {
	uriPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(uriPath) >= 2 && uriPath[1] == ':' {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath}
	query := uri.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "trusted_schema(0)")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", max(timeoutMS, 1)))
	uri.RawQuery = query.Encode()
	return uri.String()
}
