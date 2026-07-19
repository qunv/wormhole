// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"codebridge/internal/config"
)

var identifierToken = regexp.MustCompile(`[a-z_][a-z0-9_$]*|[().,;]`)

var forbiddenReadTokens = map[string]bool{
	"insert": true, "update": true, "delete": true, "merge": true,
	"copy": true, "call": true, "do": true, "create": true,
	"alter": true, "drop": true, "truncate": true, "grant": true,
	"revoke": true, "comment": true, "refresh": true, "reindex": true,
	"vacuum": true, "analyze": true, "cluster": true, "checkpoint": true,
	"lock": true, "set": true, "reset": true, "listen": true,
	"notify": true, "unlisten": true, "discard": true, "prepare": true,
	"execute": true, "deallocate": true, "into": true,
}

// SQLIdentifiers returns lowercase SQL identifiers after removing literals and
// comments. Dialects can reuse it for engine-specific safety checks.
func SQLIdentifiers(value string) []string {
	return identifierToken.FindAllString(strings.ToLower(stripSQLLiteralsAndComments(value)), -1)
}

// SQLFunctionCalls returns identifiers that are immediately followed by an
// opening parenthesis after literals and comments have been removed.
func SQLFunctionCalls(value string) []string {
	identifiers := SQLIdentifiers(value)
	calls := make([]string, 0)
	for index := 0; index+1 < len(identifiers); index++ {
		if isIdentifier(identifiers[index]) && identifiers[index+1] == "(" {
			calls = append(calls, identifiers[index])
		}
	}
	return calls
}

func ValidateReadOnlySQL(value string) (string, string, error) {
	query := strings.TrimSpace(value)
	if query == "" {
		return "", "", fmt.Errorf("sql is required")
	}
	if strings.HasSuffix(query, ";") {
		query = strings.TrimSpace(strings.TrimSuffix(query, ";"))
	}
	clean := stripSQLLiteralsAndComments(query)
	if strings.Contains(clean, ";") {
		return "", "", fmt.Errorf("multiple SQL statements are not allowed")
	}
	tokens := SQLIdentifiers(clean)
	if len(tokens) == 0 || (tokens[0] != "select" && tokens[0] != "with") {
		return "", "", fmt.Errorf("only SELECT and read-only WITH statements are allowed")
	}
	for _, token := range tokens {
		if forbiddenReadTokens[token] {
			return "", "", fmt.Errorf("read-only SQL contains forbidden token %q", token)
		}
	}
	normalized := strings.Join(strings.Fields(query), " ")
	sum := sha256.Sum256([]byte(normalized))
	return query, "sha256:" + hex.EncodeToString(sum[:12]), nil
}

func CheckQueryAccess(sql string, access config.DatabaseAccessConfig) error {
	tokens := SQLIdentifiers(sql)
	allowedSchemas := make(map[string]bool, len(access.AllowedSchemas))
	for _, schema := range access.AllowedSchemas {
		allowedSchemas[strings.ToLower(schema)] = true
	}
	denied := make(map[string]bool, len(access.DeniedTables)*2)
	for _, table := range access.DeniedTables {
		normalized := strings.ToLower(strings.TrimSpace(table))
		denied[normalized] = true
		if index := strings.LastIndex(normalized, "."); index >= 0 {
			denied[normalized[index+1:]] = true
		}
	}
	for _, relation := range referencedRelations(tokens) {
		if denied[relation] {
			return fmt.Errorf("access to table %q is denied", relation)
		}
		parts := strings.Split(relation, ".")
		if len(parts) == 2 {
			if len(allowedSchemas) > 0 && !allowedSchemas[parts[0]] {
				return fmt.Errorf("schema %q is not allowed", parts[0])
			}
			if denied[parts[1]] {
				return fmt.Errorf("access to table %q is denied", relation)
			}
		}
	}
	return nil
}

func CheckDescribeAccess(request DescribeRequest, access config.DatabaseAccessConfig) error {
	schema := strings.ToLower(strings.TrimSpace(request.Schema))
	table := strings.ToLower(strings.TrimSpace(request.Table))
	if schema != "" && len(access.AllowedSchemas) > 0 {
		allowed := false
		for _, candidate := range access.AllowedSchemas {
			if strings.EqualFold(candidate, schema) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("schema %q is not allowed", schema)
		}
	}
	for _, denied := range access.DeniedTables {
		denied = strings.ToLower(strings.TrimSpace(denied))
		if denied == table || (schema != "" && denied == schema+"."+table) {
			return fmt.Errorf("access to table %q is denied", denied)
		}
	}
	return nil
}

func referencedRelations(tokens []string) []string {
	var relations []string
	for index := 0; index < len(tokens); index++ {
		if tokens[index] != "from" && tokens[index] != "join" {
			continue
		}
		index++
		for index < len(tokens) && tokens[index] == "(" {
			index++
		}
		if index >= len(tokens) || !isIdentifier(tokens[index]) {
			continue
		}
		relation := tokens[index]
		if index+2 < len(tokens) && tokens[index+1] == "." && isIdentifier(tokens[index+2]) {
			relation += "." + tokens[index+2]
		}
		relations = append(relations, relation)
	}
	return relations
}

func isIdentifier(value string) bool {
	return value != "" && ((value[0] >= 'a' && value[0] <= 'z') || value[0] == '_')
}

func stripSQLLiteralsAndComments(value string) string {
	var out strings.Builder
	for index := 0; index < len(value); {
		switch {
		case value[index] == '\'' || value[index] == '"':
			quote := value[index]
			out.WriteByte(' ')
			index++
			for index < len(value) {
				if value[index] == quote {
					if index+1 < len(value) && value[index+1] == quote {
						index += 2
						continue
					}
					index++
					break
				}
				index++
			}
		case index+1 < len(value) && value[index:index+2] == "--":
			for index < len(value) && value[index] != '\n' {
				index++
			}
			out.WriteByte('\n')
		case index+1 < len(value) && value[index:index+2] == "/*":
			index += 2
			for index+1 < len(value) && value[index:index+2] != "*/" {
				index++
			}
			if index+1 < len(value) {
				index += 2
			}
			out.WriteByte(' ')
		default:
			out.WriteByte(value[index])
			index++
		}
	}
	return out.String()
}
