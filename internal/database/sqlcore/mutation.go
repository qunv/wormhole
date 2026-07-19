// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package sqlcore

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"codebridge/internal/config"
	"codebridge/internal/database"
)

const hardMaxAffectedRows int64 = 100

var mutationIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

// MutationDialect adds only the primary-key introspection needed by the shared
// structured mutation engine. Raw mutation SQL is never accepted.
type MutationDialect interface {
	PrimaryKeyColumns(context.Context, Queryer, config.DatabaseConnectionConfig, string, string) ([]string, error)
}

func (c *Client) SupportsMutation() bool {
	_, ok := c.dialect.(MutationDialect)
	environment := strings.ToLower(strings.TrimSpace(c.config.Environment))
	return ok && c.config.Access.Mode == "read-write" && environment != "prod" && environment != "production"
}

func (c *Client) PreviewMutation(ctx context.Context, request database.MutationRequest) (database.MutationPreview, error) {
	if !c.SupportsMutation() {
		return database.MutationPreview{}, fmt.Errorf("database driver %q does not support structured mutations", c.dialect.Name())
	}
	tx, err := c.beginReadOnly(ctx)
	if err != nil {
		return database.MutationPreview{}, err
	}
	defer tx.Rollback()
	plan, err := c.prepareMutation(ctx, tx, request)
	if err != nil {
		return database.MutationPreview{}, c.normalizeError(err)
	}
	return plan.preview, nil
}

func (c *Client) Mutate(ctx context.Context, request database.MutationRequest) (database.MutationResult, error) {
	if !c.SupportsMutation() {
		return database.MutationResult{}, fmt.Errorf("database driver %q does not support structured mutations", c.dialect.Name())
	}
	started := time.Now()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return database.MutationResult{}, c.normalizeError(err)
	}
	defer tx.Rollback()
	plan, err := c.prepareMutation(ctx, tx, request)
	if err != nil {
		return database.MutationResult{}, c.normalizeError(err)
	}
	execution, err := tx.ExecContext(ctx, plan.statement, plan.params...)
	if err != nil {
		return database.MutationResult{}, c.normalizeError(err)
	}
	affected, err := execution.RowsAffected()
	if err != nil {
		return database.MutationResult{}, c.normalizeError(err)
	}
	if affected > request.MaxAffectedRows {
		return database.MutationResult{}, fmt.Errorf("mutation affected %d rows, exceeding maxAffectedRows=%d; transaction rolled back", affected, request.MaxAffectedRows)
	}
	if err := tx.Commit(); err != nil {
		return database.MutationResult{}, c.normalizeError(err)
	}
	return database.MutationResult{
		MutationPreview: plan.preview,
		AffectedRows:    affected,
		ElapsedMS:       time.Since(started).Milliseconds(),
	}, nil
}

type mutationPlan struct {
	preview   database.MutationPreview
	statement string
	params    []any
}

func (c *Client) prepareMutation(ctx context.Context, queryer Queryer, request database.MutationRequest) (mutationPlan, error) {
	request.Operation = strings.ToLower(strings.TrimSpace(request.Operation))
	request.Schema = strings.TrimSpace(request.Schema)
	request.Table = strings.TrimSpace(request.Table)
	if request.Operation != "update" && request.Operation != "delete" {
		return mutationPlan{}, fmt.Errorf("mutation operation must be update or delete")
	}
	if !mutationIdentifier.MatchString(request.Schema) || !mutationIdentifier.MatchString(request.Table) {
		return mutationPlan{}, fmt.Errorf("mutation schema and table must use simple SQL identifiers")
	}
	if request.MaxAffectedRows < 1 || request.MaxAffectedRows > hardMaxAffectedRows {
		return mutationPlan{}, fmt.Errorf("maxAffectedRows must be between 1 and %d", hardMaxAffectedRows)
	}
	if len(request.Where) == 0 || len(request.Where) > 32 {
		return mutationPlan{}, fmt.Errorf("mutation where must contain 1-32 equality predicates")
	}
	if request.Operation == "update" && (len(request.Values) == 0 || len(request.Values) > 32) {
		return mutationPlan{}, fmt.Errorf("update values must contain 1-32 columns")
	}
	if request.Operation == "delete" && len(request.Values) != 0 {
		return mutationPlan{}, fmt.Errorf("delete mutation must not contain values")
	}
	for name, value := range request.Where {
		if !mutationIdentifier.MatchString(name) || !isMutationScalar(value) {
			return mutationPlan{}, fmt.Errorf("invalid mutation predicate column or value for %q", name)
		}
	}
	for name, value := range request.Values {
		if !mutationIdentifier.MatchString(name) || !isMutationScalar(value) {
			return mutationPlan{}, fmt.Errorf("invalid mutation value column or value for %q", name)
		}
	}

	dialect := c.dialect.(MutationDialect)
	primaryKeys, err := dialect.PrimaryKeyColumns(ctx, queryer, c.config, request.Schema, request.Table)
	if err != nil {
		return mutationPlan{}, err
	}
	if len(primaryKeys) == 0 {
		return mutationPlan{}, fmt.Errorf("structured mutations require a primary key on %s.%s", request.Schema, request.Table)
	}
	for _, key := range primaryKeys {
		if _, ok := request.Where[key]; !ok {
			return mutationPlan{}, fmt.Errorf("mutation where must include primary-key column %q", key)
		}
		if _, changesPrimaryKey := request.Values[key]; changesPrimaryKey {
			return mutationPlan{}, fmt.Errorf("structured update cannot modify primary-key column %q", key)
		}
	}

	valueColumns := sortedMapKeys(request.Values)
	predicateColumns := sortedMapKeys(request.Where)
	quotedSchema, err := c.dialect.QuoteIdentifier(request.Schema)
	if err != nil {
		return mutationPlan{}, err
	}
	quotedTable, err := c.dialect.QuoteIdentifier(request.Table)
	if err != nil {
		return mutationPlan{}, err
	}
	target := quotedSchema + "." + quotedTable
	params := make([]any, 0, len(valueColumns)+len(predicateColumns))
	position := 0

	var statement strings.Builder
	switch request.Operation {
	case "update":
		statement.WriteString("UPDATE ")
		statement.WriteString(target)
		statement.WriteString(" SET ")
		for index, column := range valueColumns {
			if index > 0 {
				statement.WriteString(", ")
			}
			quoted, err := c.dialect.QuoteIdentifier(column)
			if err != nil {
				return mutationPlan{}, err
			}
			position++
			statement.WriteString(quoted)
			statement.WriteString(" = ")
			statement.WriteString(c.dialect.Placeholder(position))
			params = append(params, request.Values[column])
		}
	case "delete":
		statement.WriteString("DELETE FROM ")
		statement.WriteString(target)
	}
	statement.WriteString(" WHERE ")
	for index, column := range predicateColumns {
		if index > 0 {
			statement.WriteString(" AND ")
		}
		quoted, err := c.dialect.QuoteIdentifier(column)
		if err != nil {
			return mutationPlan{}, err
		}
		statement.WriteString(quoted)
		if request.Where[column] == nil {
			statement.WriteString(" IS NULL")
			continue
		}
		position++
		statement.WriteString(" = ")
		statement.WriteString(c.dialect.Placeholder(position))
		params = append(params, request.Where[column])
	}

	return mutationPlan{
		preview: database.MutationPreview{
			Operation: request.Operation, Schema: request.Schema, Table: request.Table,
			ValueColumns: valueColumns, PredicateColumns: predicateColumns,
			PrimaryKeyColumns: append([]string(nil), primaryKeys...), MaxAffectedRows: request.MaxAffectedRows,
		},
		statement: statement.String(), params: params,
	}, nil
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func isMutationScalar(value any) bool {
	switch value.(type) {
	case nil, bool, string, []byte,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number, time.Time:
		return true
	default:
		return false
	}
}
