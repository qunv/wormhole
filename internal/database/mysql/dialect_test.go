package mysql

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"

	"codebridge/internal/database"

	"github.com/DATA-DOG/go-sqlmock"
	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestDialectSQLPrimitivesAndSafety(t *testing.T) {
	dialect := Dialect{}
	if got := dialect.Placeholder(3); got != "?" {
		t.Fatalf("Placeholder(3) = %q", got)
	}
	quoted, err := dialect.QuoteIdentifier("user`data")
	if err != nil {
		t.Fatal(err)
	}
	if quoted != "`user``data`" {
		t.Fatalf("quoted identifier = %q", quoted)
	}
	if got := dialect.ExplainSQL("SELECT 1"); got != "EXPLAIN FORMAT=JSON SELECT 1" {
		t.Fatalf("unexpected explain SQL: %q", got)
	}

	for _, statement := range []string{
		"SELECT GET_LOCK('name', 1)",
		"SELECT LOAD_FILE('/etc/passwd')",
		"SELECT SLEEP(30)",
		"SELECT * FROM mysql.user",
		"SELECT * FROM performance_schema.threads",
		"SELECT @session_value",
		"SELECT @session_value := 1",
	} {
		if err := dialect.ValidateReadOnlySQL(statement); err == nil {
			t.Fatalf("unsafe query was accepted: %s", statement)
		}
	}
	for _, statement := range []string{
		"SELECT 'GET_LOCK' AS value",
		"SELECT 1 /* LOAD_FILE('/tmp/x') */",
		"SELECT sleep FROM app.metrics",
		"SELECT * FROM app.users",
	} {
		if err := dialect.ValidateReadOnlySQL(statement); err != nil {
			t.Fatalf("safe query %q was rejected: %v", statement, err)
		}
	}
}

func TestDialectConfigureReadOnlySetsExecutionCap(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec(regexp.QuoteMeta("SET SESSION MAX_EXECUTION_TIME = 1500")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	if err := (Dialect{}).ConfigureReadOnly(context.Background(), tx, mysqlTestConfig("dev")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDialectDescribeColumnsAndIndexes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := mysqlTestConfig("dev")

	mock.ExpectQuery("SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, COLUMN_TYPE").
		WithArgs("app", "app", "users", 11).
		WillReturnRows(sqlmock.NewRows([]string{
			"TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME", "COLUMN_TYPE", "IS_NULLABLE", "ORDINAL_POSITION",
		}).
			AddRow("app", "users", "id", "bigint unsigned", "NO", 1).
			AddRow("app", "users", "email", "varchar(255)", "NO", 2))

	mock.ExpectQuery("SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE").
		WithArgs("app", "app", "users").
		WillReturnRows(sqlmock.NewRows([]string{
			"TABLE_SCHEMA", "TABLE_NAME", "INDEX_NAME", "NON_UNIQUE", "SEQ_IN_INDEX", "COLUMN_NAME", "INDEX_TYPE", "SUB_PART",
		}).
			AddRow("app", "users", "PRIMARY", 0, 1, "id", "BTREE", nil).
			AddRow("app", "users", "idx_email", 1, 1, "email", "BTREE", 32))

	result, err := (Dialect{}).Describe(context.Background(), db, cfg, database.DescribeRequest{
		Schema: "app", Table: "users", IncludeIndexes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tables) != 1 || len(result.Tables[0].Columns) != 2 || len(result.Tables[0].Indexes) != 2 {
		t.Fatalf("unexpected describe result: %#v", result)
	}
	if got := result.Tables[0].Indexes[0].Definition; got != "UNIQUE BTREE (`id`)" {
		t.Fatalf("primary index definition = %q", got)
	}
	if got := result.Tables[0].Indexes[1].Definition; got != "BTREE (`email`(32))" {
		t.Fatalf("email index definition = %q", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDialectNormalizesMySQLErrors(t *testing.T) {
	err := Dialect{}.NormalizeError(&mysqldriver.MySQLError{
		Number: 1142, SQLState: [5]byte{'4', '2', '0', '0', '0'}, Message: "SELECT command denied",
	})
	if err == nil || !strings.Contains(err.Error(), "MySQL error 1142 SQLSTATE 42000") {
		t.Fatalf("unexpected normalized error: %v", err)
	}
}
