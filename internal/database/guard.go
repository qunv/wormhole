// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"codebridge/internal/config"
)

var forbiddenReadTokens = map[string]bool{
	"insert": true, "update": true, "delete": true, "merge": true,
	"copy": true, "call": true, "do": true, "create": true,
	"alter": true, "drop": true, "truncate": true, "grant": true,
	"revoke": true, "comment": true, "refresh": true, "reindex": true,
	"vacuum": true, "analyze": true, "cluster": true, "checkpoint": true,
	"lock": true, "set": true, "reset": true, "listen": true,
	"notify": true, "unlisten": true, "discard": true, "prepare": true,
	"execute": true, "deallocate": true, "into": true, "outfile": true,
	"dumpfile": true, "attach": true, "detach": true, "pragma": true,
}

type sqlAnalysis struct {
	code   string
	tokens []string
	quoted []bool
}

// SQLCode returns SQL with literals and ordinary comments replaced by spaces.
// It is intended for dialect-specific conservative checks, never execution.
func SQLCode(value string) (string, error) {
	analysis, err := analyzeSQL(value)
	return analysis.code, err
}

// SQLIdentifiers returns lowercase SQL identifiers after removing literals and
// comments. Quoted identifiers are retained as one normalized token.
func SQLIdentifiers(value string) []string {
	analysis, err := analyzeSQL(value)
	if err != nil {
		return nil
	}
	return append([]string(nil), analysis.tokens...)
}

// SQLFunctionCalls returns identifiers immediately followed by an opening
// parenthesis after literals and comments have been removed.
func SQLFunctionCalls(value string) []string {
	identifiers := SQLIdentifiers(value)
	calls := make([]string, 0)
	for index := 0; index+1 < len(identifiers); index++ {
		if isIdentifierToken(identifiers[index]) && identifiers[index+1] == "(" {
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
	analysis, err := analyzeSQL(query)
	if err != nil {
		return "", "", err
	}
	for _, token := range analysis.tokens {
		if token == ";" {
			return "", "", fmt.Errorf("multiple SQL statements are not allowed")
		}
	}
	if len(analysis.tokens) == 0 || analysis.quoted[0] || (analysis.tokens[0] != "select" && analysis.tokens[0] != "with") {
		return "", "", fmt.Errorf("only SELECT and read-only WITH statements are allowed")
	}
	for index, token := range analysis.tokens {
		if !analysis.quoted[index] && forbiddenReadTokens[token] {
			return "", "", fmt.Errorf("read-only SQL contains forbidden token %q", token)
		}
	}
	normalized := strings.Join(strings.Fields(query), " ")
	sum := sha256.Sum256([]byte(normalized))
	return query, "sha256:" + hex.EncodeToString(sum[:12]), nil
}

func CheckQueryAccess(sql string, access config.DatabaseAccessConfig) error {
	analysis, err := analyzeSQL(sql)
	if err != nil {
		return err
	}
	tokens := analysis.tokens
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
		if index >= len(tokens) || !isIdentifierToken(tokens[index]) {
			continue
		}
		relation := tokens[index]
		if index+2 < len(tokens) && tokens[index+1] == "." && isIdentifierToken(tokens[index+2]) {
			relation += "." + tokens[index+2]
		}
		relations = append(relations, relation)
	}
	return relations
}

func isIdentifierToken(value string) bool {
	if value == "" {
		return false
	}
	switch value {
	case "(", ")", ".", ",", ";":
		return false
	}
	return true
}

func analyzeSQL(value string) (sqlAnalysis, error) {
	var code strings.Builder
	tokens := make([]string, 0, 32)
	quoted := make([]bool, 0, 32)
	appendToken := func(value string, isQuoted bool) {
		tokens = append(tokens, value)
		quoted = append(quoted, isQuoted)
	}
	writeSpace := func() {
		if code.Len() == 0 || code.String()[code.Len()-1] != ' ' {
			code.WriteByte(' ')
		}
	}

	for index := 0; index < len(value); {
		current := value[index]
		switch {
		case current == '\'':
			next, err := skipQuoted(value, index, '\'', false)
			if err != nil {
				return sqlAnalysis{}, err
			}
			writeSpace()
			index = next
		case current == '"' || current == '`':
			identifier, next, err := readQuotedIdentifier(value, index, current)
			if err != nil {
				return sqlAnalysis{}, err
			}
			appendToken(strings.ToLower(identifier), true)
			code.WriteString(identifier)
			index = next
		case current == '$':
			if delimiter, ok := dollarQuoteDelimiter(value[index:]); ok {
				end := strings.Index(value[index+len(delimiter):], delimiter)
				if end < 0 {
					return sqlAnalysis{}, fmt.Errorf("unterminated dollar-quoted SQL literal")
				}
				writeSpace()
				index += len(delimiter) + end + len(delimiter)
				continue
			}
			code.WriteByte(current)
			index++
		case index+1 < len(value) && value[index:index+2] == "--":
			if index+2 < len(value) && value[index+2] > ' ' {
				return sqlAnalysis{}, fmt.Errorf("ambiguous double-dash sequence is not allowed; add whitespace after -- or use parameters")
			}
			for index < len(value) && value[index] != '\n' {
				index++
			}
			writeSpace()
		case index+1 < len(value) && value[index:index+2] == "/*":
			if index+2 < len(value) && (value[index+2] == '!' || value[index+2] == '+') {
				return sqlAnalysis{}, fmt.Errorf("executable or optimizer SQL comments are not allowed")
			}
			next, err := skipBlockComment(value, index)
			if err != nil {
				return sqlAnalysis{}, err
			}
			writeSpace()
			index = next
		case isIdentifierStart(current):
			start := index
			index++
			for index < len(value) && isIdentifierPart(value[index]) {
				index++
			}
			token := strings.ToLower(value[start:index])
			appendToken(token, false)
			code.WriteString(value[start:index])
		case current == '(' || current == ')' || current == '.' || current == ',' || current == ';':
			appendToken(string(current), false)
			code.WriteByte(current)
			index++
		default:
			code.WriteByte(current)
			index++
		}
	}
	return sqlAnalysis{code: code.String(), tokens: tokens, quoted: quoted}, nil
}

func skipQuoted(value string, start int, quote byte, identifier bool) (int, error) {
	for index := start + 1; index < len(value); index++ {
		if value[index] == '\\' {
			kind := "literal"
			if identifier {
				kind = "identifier"
			}
			return 0, fmt.Errorf("backslash escapes in SQL %s are not allowed; use query parameters", kind)
		}
		if value[index] != quote {
			continue
		}
		if index+1 < len(value) && value[index+1] == quote {
			index++
			continue
		}
		return index + 1, nil
	}
	return 0, fmt.Errorf("unterminated SQL literal or identifier")
}

func readQuotedIdentifier(value string, start int, quote byte) (string, int, error) {
	var identifier strings.Builder
	for index := start + 1; index < len(value); index++ {
		if value[index] == '\\' {
			return "", 0, fmt.Errorf("backslash escapes in SQL identifiers are not allowed")
		}
		if value[index] != quote {
			identifier.WriteByte(value[index])
			continue
		}
		if index+1 < len(value) && value[index+1] == quote {
			identifier.WriteByte(quote)
			index++
			continue
		}
		if identifier.Len() == 0 {
			return "", 0, fmt.Errorf("empty quoted SQL identifier is not allowed")
		}
		return identifier.String(), index + 1, nil
	}
	return "", 0, fmt.Errorf("unterminated quoted SQL identifier")
}

func skipBlockComment(value string, start int) (int, error) {
	depth := 1
	for index := start + 2; index < len(value); {
		switch {
		case index+1 < len(value) && value[index:index+2] == "/*":
			if index+2 < len(value) && (value[index+2] == '!' || value[index+2] == '+') {
				return 0, fmt.Errorf("executable or optimizer SQL comments are not allowed")
			}
			depth++
			index += 2
		case index+1 < len(value) && value[index:index+2] == "*/":
			depth--
			index += 2
			if depth == 0 {
				return index, nil
			}
		default:
			index++
		}
	}
	return 0, fmt.Errorf("unterminated SQL block comment")
}

func dollarQuoteDelimiter(value string) (string, bool) {
	if len(value) < 2 || value[0] != '$' {
		return "", false
	}
	for index := 1; index < len(value); index++ {
		switch {
		case value[index] == '$':
			return value[:index+1], true
		case isIdentifierPart(value[index]):
			continue
		default:
			return "", false
		}
	}
	return "", false
}

func isIdentifierStart(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= utf8RuneSelf
}

func isIdentifierPart(value byte) bool {
	return isIdentifierStart(value) || value >= '0' && value <= '9' || value == '$'
}

const utf8RuneSelf = 0x80

func stripSQLLiteralsAndComments(value string) string {
	analysis, err := analyzeSQL(value)
	if err != nil {
		return ""
	}
	return analysis.code
}
