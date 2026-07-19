// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package mysql

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/database"
	"codebridge/internal/database/sqlcore"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// New validates and hardens a MySQL DSN before passing the resulting
// database/sql handle into the shared SQL execution core.
func New(_ string, cfg config.DatabaseConnectionConfig, credential string) (database.Connection, error) {
	driverConfig, err := parseAndHardenDSN(credential, cfg)
	if err != nil {
		return nil, err
	}
	connector, err := mysqldriver.NewConnector(driverConfig)
	if err != nil {
		return nil, fmt.Errorf("create MySQL connector: %w", err)
	}
	db := sql.OpenDB(connector)
	client, err := sqlcore.NewWithDB(db, cfg, Dialect{})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return client, nil
}

func parseAndHardenDSN(credential string, cfg config.DatabaseConnectionConfig) (*mysqldriver.Config, error) {
	driverConfig, err := mysqldriver.ParseDSN(strings.TrimSpace(credential))
	if err != nil {
		return nil, fmt.Errorf("parse MySQL DSN: %w", err)
	}
	if driverConfig.MultiStatements {
		return nil, fmt.Errorf("MySQL DSN multiStatements=true is not allowed")
	}
	if driverConfig.InterpolateParams {
		return nil, fmt.Errorf("MySQL DSN interpolateParams=true is not allowed; parameters must remain driver-bound")
	}
	if driverConfig.AllowAllFiles {
		return nil, fmt.Errorf("MySQL DSN allowAllFiles=true is not allowed")
	}
	if driverConfig.AllowCleartextPasswords {
		return nil, fmt.Errorf("MySQL DSN allowCleartextPasswords=true is not allowed")
	}
	if driverConfig.AllowFallbackToPlaintext {
		return nil, fmt.Errorf("MySQL DSN plaintext TLS fallback is not allowed")
	}
	if driverConfig.AllowOldPasswords {
		return nil, fmt.Errorf("MySQL DSN allowOldPasswords=true is not allowed")
	}
	if driverConfig.TLS != nil && driverConfig.TLS.InsecureSkipVerify {
		return nil, fmt.Errorf("MySQL DSN TLS verification must not be disabled")
	}
	if driverConfig.TLSConfig == "skip-verify" || driverConfig.TLSConfig == "preferred" {
		return nil, fmt.Errorf("MySQL DSN TLS mode %q is not allowed", driverConfig.TLSConfig)
	}

	if len(cfg.Access.AllowedSchemas) > 0 {
		if strings.TrimSpace(driverConfig.DBName) == "" {
			return nil, fmt.Errorf("MySQL DSN must select a default database when allowedSchemas is configured")
		}
		if !containsFold(cfg.Access.AllowedSchemas, driverConfig.DBName) {
			return nil, fmt.Errorf("MySQL DSN default database %q is not in allowedSchemas", driverConfig.DBName)
		}
	}

	if isProduction(cfg.Environment) && driverConfig.Net != "unix" && driverConfig.TLS == nil {
		return nil, fmt.Errorf("production MySQL TCP connections require verified TLS")
	}

	// Keep temporal values portable through the shared result normalizer.
	driverConfig.ParseTime = true
	driverConfig.Loc = time.UTC
	return driverConfig, nil
}

func containsFold(values []string, candidate string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func isProduction(environment string) bool {
	environment = strings.ToLower(strings.TrimSpace(environment))
	return environment == "prod" || environment == "production"
}
