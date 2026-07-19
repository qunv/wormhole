package database

import (
	"strings"
	"testing"

	"codebridge/internal/config"
)

func TestValidateReadOnlySQLAdversarialInputs(t *testing.T) {
	rejected := []string{
		"SELECT 1 /*!50000 INTO OUTFILE '/tmp/data' */",
		"SELECT /*+ MAX_EXECUTION_TIME(999999) */ 1",
		"SELECT 1; SELECT 2",
		"SELECT 1--not-a-portable-comment\n",
		"SELECT 'unterminated",
		"SELECT 'backslash\\escape'",
		"SELECT 1 /* unterminated",
		"WITH changed AS (UPDATE users SET active = false RETURNING *) SELECT * FROM changed",
		"SELECT * FROM users INTO OUTFILE '/tmp/users'",
	}
	for _, statement := range rejected {
		if _, _, err := ValidateReadOnlySQL(statement); err == nil {
			t.Fatalf("unsafe or ambiguous SQL was accepted: %q", statement)
		}
	}

	accepted := []string{
		"SELECT 1",
		"SELECT '/* not a comment */' AS value",
		"SELECT 1 -- ordinary comment\n",
		"SELECT /* ordinary comment */ 1",
		"WITH items AS (SELECT id FROM app.users) SELECT id FROM items",
		"SELECT $$UPDATE users SET active = false$$ AS text",
		"SELECT $tag$DELETE FROM users$tag$ AS text",
		"SELECT `update` FROM `app`.`metrics`",
		"SELECT \"delete\" FROM \"app\".\"metrics\"",
	}
	for _, statement := range accepted {
		if _, _, err := ValidateReadOnlySQL(statement); err != nil {
			t.Fatalf("safe SQL %q was rejected: %v", statement, err)
		}
	}
}

func TestQuotedRelationsRemainVisibleToAccessPolicy(t *testing.T) {
	access := config.DatabaseAccessConfig{
		AllowedSchemas: []string{"app"},
		DeniedTables:   []string{"app.secrets"},
	}
	for _, statement := range []string{
		"SELECT * FROM `app`.`secrets`",
		"SELECT * FROM \"app\".\"secrets\"",
	} {
		if err := CheckQueryAccess(statement, access); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Fatalf("quoted denied relation was not blocked for %q: %v", statement, err)
		}
	}
	if err := CheckQueryAccess("SELECT * FROM `other`.`users`", access); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("quoted disallowed schema was not blocked: %v", err)
	}
}

func TestSQLFunctionCallsIgnoreColumnsLiteralsAndComments(t *testing.T) {
	calls := SQLFunctionCalls("SELECT sleep, 'load_file(1)' FROM metrics /* get_lock(1) */ WHERE id = COALESCE(?, 1)")
	if len(calls) != 1 || calls[0] != "coalesce" {
		t.Fatalf("function calls = %#v, want [coalesce]", calls)
	}
}
