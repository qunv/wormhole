// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"codebridge/internal/config"
	"codebridge/internal/database"
	"codebridge/internal/database/sqlcore"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// New registers PostgreSQL with the driver-neutral manager while delegating
// pool, transaction, query, scanning, masking, and result limits to sqlcore.
func New(_ string, cfg config.DatabaseConnectionConfig, credential string) (database.Connection, error) {
	return sqlcore.Open("pgx", credential, cfg, Dialect{})
}
