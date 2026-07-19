package sqlcore

import (
	"context"
	"testing"

	"codebridge/internal/config"
	"codebridge/internal/database"

	"github.com/DATA-DOG/go-sqlmock"
)

type mutationTestDialect struct{ testDialect }

func mutationConnectionConfig() config.DatabaseConnectionConfig {
	cfg := testConnectionConfig()
	cfg.Environment = "dev"
	cfg.Access.Mode = "read-write"
	return cfg
}

func (*mutationTestDialect) PrimaryKeyColumns(ctx context.Context, queryer Queryer, _ config.DatabaseConnectionConfig, schema, table string) ([]string, error) {
	rows, err := queryer.QueryContext(ctx, "TEST PRIMARY KEY", schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

func TestStructuredMutationPreviewAndExecute(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	dialect := &mutationTestDialect{}
	client, err := NewWithDB(db, mutationConnectionConfig(), dialect)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := database.MutationRequest{
		Operation: "update", Schema: "app", Table: "users",
		Values: map[string]any{"status": "disabled"}, Where: map[string]any{"id": int64(7)},
		MaxAffectedRows: 1,
	}

	mock.ExpectBegin()
	mock.ExpectExec("SET DIALECT READ ONLY").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("TEST PRIMARY KEY").WithArgs("app", "users").
		WillReturnRows(sqlmock.NewRows([]string{"column"}).AddRow("id"))
	mock.ExpectRollback()
	preview, err := client.PreviewMutation(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Operation != "update" || len(preview.PrimaryKeyColumns) != 1 || preview.PrimaryKeyColumns[0] != "id" {
		t.Fatalf("unexpected preview: %#v", preview)
	}

	mock.ExpectBegin()
	mock.ExpectQuery("TEST PRIMARY KEY").WithArgs("app", "users").
		WillReturnRows(sqlmock.NewRows([]string{"column"}).AddRow("id"))
	mock.ExpectExec(`UPDATE \[app\]\.\[users\] SET \[status\] = \?1 WHERE \[id\] = \?2`).
		WithArgs("disabled", int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	result, err := client.Mutate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.AffectedRows != 1 || result.Table != "users" {
		t.Fatalf("unexpected mutation result: %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStructuredMutationRollsBackAboveAffectedRowLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewWithDB(db, mutationConnectionConfig(), &mutationTestDialect{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := database.MutationRequest{
		Operation: "delete", Schema: "app", Table: "users",
		Where: map[string]any{"id": int64(7)}, MaxAffectedRows: 1,
	}
	mock.ExpectBegin()
	mock.ExpectQuery("TEST PRIMARY KEY").WithArgs("app", "users").
		WillReturnRows(sqlmock.NewRows([]string{"column"}).AddRow("id"))
	mock.ExpectExec(`DELETE FROM \[app\]\.\[users\] WHERE \[id\] = \?1`).
		WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectRollback()
	if _, err := client.Mutate(context.Background(), request); err == nil {
		t.Fatal("mutation above maxAffectedRows was not rejected")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStructuredMutationRequiresCompletePrimaryKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewWithDB(db, mutationConnectionConfig(), &mutationTestDialect{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	mock.ExpectBegin()
	mock.ExpectExec("SET DIALECT READ ONLY").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("TEST PRIMARY KEY").WithArgs("app", "memberships").
		WillReturnRows(sqlmock.NewRows([]string{"column"}).AddRow("account_id").AddRow("user_id"))
	mock.ExpectRollback()
	_, err = client.PreviewMutation(context.Background(), database.MutationRequest{
		Operation: "update", Schema: "app", Table: "memberships",
		Values: map[string]any{"role": "admin"}, Where: map[string]any{"account_id": 1},
		MaxAffectedRows: 1,
	})
	if err == nil {
		t.Fatal("incomplete composite primary key was accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
