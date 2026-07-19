package database

import (
	"context"
	"strings"
	"testing"

	"codebridge/internal/config"
)

func mutationManagerConfig(environment, access string) config.DatabaseConfig {
	cfg := managerTestConfig()
	connection := cfg.Connections["db.dev"]
	connection.Environment = environment
	connection.Access.Mode = access
	connection.Access.AllowedSchemas = []string{"app"}
	connection.Access.DeniedTables = []string{"app.secrets"}
	cfg.Connections["db.dev"] = connection
	return cfg
}

func TestManagerStructuredMutationPolicyAndMetrics(t *testing.T) {
	t.Setenv("CODEBRIDGE_TEST_DATABASE_DSN", "test-credential")
	manager, err := NewManager(mutationManagerConfig("dev", "read-write"), map[string]Constructor{
		"test": func(string, config.DatabaseConnectionConfig, string) (Connection, error) {
			return &managerTestConnection{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	request := MutationRequest{
		Operation: "update", Schema: "app", Table: "users",
		Values: map[string]any{"status": "disabled"}, Where: map[string]any{"id": 1},
		MaxAffectedRows: 1,
	}
	preview, summary, err := manager.PreviewMutation(context.Background(), "db.dev", request)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Table != "users" || summary.Access != "read-write" {
		t.Fatalf("unexpected preview: %#v summary=%#v", preview, summary)
	}
	result, _, err := manager.Mutate(context.Background(), "db.dev", request)
	if err != nil {
		t.Fatal(err)
	}
	if result.AffectedRows != 1 {
		t.Fatalf("affected rows = %d", result.AffectedRows)
	}
	metrics := manager.List(context.Background(), false)[0].Metrics
	if metrics.MutationPreviewTotal != 1 || metrics.MutationTotal != 1 || metrics.AffectedRowsTotal != 1 {
		t.Fatalf("unexpected mutation metrics: %#v", metrics)
	}
	if _, _, err := manager.PreviewMutation(context.Background(), "db.dev", MutationRequest{
		Operation: "delete", Schema: "app", Table: "secrets", Where: map[string]any{"id": 1}, MaxAffectedRows: 1,
	}); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("denied mutation table was not blocked: %v", err)
	}
}

func TestManagerRejectsProductionAndReadOnlyMutations(t *testing.T) {
	t.Setenv("CODEBRIDGE_TEST_DATABASE_DSN", "test-credential")
	for _, test := range []struct {
		name        string
		environment string
		access      string
		want        string
	}{
		{name: "production", environment: "prod", access: "read-only", want: "production"},
		{name: "read-only", environment: "dev", access: "read-only", want: "read-write"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, err := NewManager(mutationManagerConfig(test.environment, test.access), map[string]Constructor{
				"test": func(string, config.DatabaseConnectionConfig, string) (Connection, error) {
					return &managerTestConnection{}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			_, _, err = manager.PreviewMutation(context.Background(), "db.dev", MutationRequest{
				Operation: "update", Schema: "app", Table: "users",
				Values: map[string]any{"status": "disabled"}, Where: map[string]any{"id": 1}, MaxAffectedRows: 1,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("mutation policy error = %v, want %q", err, test.want)
			}
		})
	}
}
