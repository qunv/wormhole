package sqlcore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"codebridge/internal/config"
	"codebridge/internal/database"

	"github.com/DATA-DOG/go-sqlmock"
)

type testDialect struct {
	validated  string
	configured int
	described  int
}

func (*testDialect) Name() string                                 { return "test" }
func (*testDialect) Placeholder(position int) string              { return fmt.Sprintf("?%d", position) }
func (*testDialect) QuoteIdentifier(value string) (string, error) { return "[" + value + "]", nil }
func (d *testDialect) ValidateReadOnlySQL(statement string) error {
	d.validated = statement
	if statement == "SELECT forbidden" {
		return errors.New("dialect-specific rejection")
	}
	return nil
}
func (d *testDialect) ConfigureReadOnly(ctx context.Context, tx *sql.Tx, _ config.DatabaseConnectionConfig) error {
	d.configured++
	_, err := tx.ExecContext(ctx, "SET DIALECT READ ONLY")
	return err
}
func (*testDialect) ExplainSQL(statement string) string { return "EXPLAIN DIALECT " + statement }
func (d *testDialect) Describe(context.Context, Queryer, config.DatabaseConnectionConfig, database.DescribeRequest) (database.DescribeResult, error) {
	d.described++
	return database.DescribeResult{Tables: []database.TableDescription{{Schema: "main", Name: "items"}}}, nil
}
func (*testDialect) NormalizeError(err error) error { return err }

func testConnectionConfig() config.DatabaseConnectionConfig {
	return config.DatabaseConnectionConfig{
		Access: config.DatabaseAccessConfig{Mode: "read-only", MaskColumns: []string{"*.secret"}},
		Limits: config.DatabaseLimitsConfig{
			QueryTimeoutMS: 1000, MaxRows: 10, MaxResultBytes: 1024,
			MaxCellBytes: 64, MaxConcurrentQueries: 1,
		},
		Pool: config.DatabasePoolConfig{MaxOpen: 2, MaxIdle: 1, MaxLifetimeSeconds: 60},
	}
}

func TestClientQueryUsesSharedExecutionAndLimits(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	dialect := &testDialect{}
	client, err := NewWithDB(db, testConnectionConfig(), dialect)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	mock.ExpectBegin()
	mock.ExpectExec("SET DIALECT READ ONLY").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT id, secret FROM items").WillReturnRows(
		sqlmock.NewRows([]string{"id", "secret"}).
			AddRow(int64(1), "first-secret").
			AddRow(int64(2), "second-secret"),
	)
	mock.ExpectRollback()

	result, err := client.Query(context.Background(), database.QueryRequest{
		SQL: "SELECT id, secret FROM items", MaxRows: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dialect.configured != 1 {
		t.Fatalf("ConfigureReadOnly calls = %d, want 1", dialect.configured)
	}
	if len(result.Rows) != 1 || result.RowCount != 1 || !result.Truncated {
		t.Fatalf("unexpected bounded result: %#v", result)
	}
	if got := result.Rows[0][1]; got != "[masked]" {
		t.Fatalf("masked column = %#v, want [masked]", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClientExplainAndDescribeDelegateToDialect(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	dialect := &testDialect{}
	client, err := NewWithDB(db, testConnectionConfig(), dialect)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	mock.ExpectBegin()
	mock.ExpectExec("SET DIALECT READ ONLY").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("EXPLAIN DIALECT SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"plan"}).AddRow("ok"))
	mock.ExpectRollback()
	if _, err := client.Explain(context.Background(), database.QueryRequest{SQL: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec("SET DIALECT READ ONLY").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	described, err := client.Describe(context.Background(), database.DescribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if dialect.described != 1 || len(described.Tables) != 1 || described.Tables[0].Name != "items" {
		t.Fatalf("unexpected describe result: %#v", described)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClientValidationDelegatesToDialect(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	dialect := &testDialect{}
	client, err := NewWithDB(db, testConnectionConfig(), dialect)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.ValidateReadOnlySQL("SELECT forbidden"); err == nil {
		t.Fatal("expected dialect-specific validation error")
	}
	if dialect.validated != "SELECT forbidden" {
		t.Fatalf("validated statement = %q", dialect.validated)
	}
}
