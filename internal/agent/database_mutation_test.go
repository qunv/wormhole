package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"codebridge/internal/config"
)

func mutationArgs() map[string]any {
	return map[string]any{
		"alias": "db.dev", "operation": "update", "schema": "app", "table": "users",
		"values": map[string]any{"status": "disabled", "secret": "new-private-value"},
		"where":  map[string]any{"id": float64(7)}, "max_affected_rows": float64(1),
	}
}

func TestDatabaseMutationApprovalActionIsDeterministicAndOpaque(t *testing.T) {
	first := databaseMutationApprovalAction(mutationArgs())
	secondArgs := mutationArgs()
	secondArgs["values"] = map[string]any{"secret": "new-private-value", "status": "disabled"}
	second := databaseMutationApprovalAction(secondArgs)
	if first == "" || first != second {
		t.Fatalf("approval actions differ: %q != %q", first, second)
	}
	if strings.Contains(first, "new-private-value") || strings.Contains(first, "disabled") {
		t.Fatalf("approval action leaked mutation values: %s", first)
	}
}

func TestDatabaseMutationRequiresExactApprovalEvenInFullPolicy(t *testing.T) {
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.NoTunnel = true
	cfg.Policy = "full"
	cfg.ApprovalToken = t.Name()
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	args := mutationArgs()
	action := databaseMutationApprovalAction(args)
	if err := runtime.enforcePolicy("db_mutate", args); err == nil || !strings.Contains(err.Error(), "approval required") {
		t.Fatalf("unapproved mutation was not blocked: %v", err)
	}
	record, err := runtime.Approvals.Request([]string{action}, "test structured mutation", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Approvals.Decide(record.ID, t.Name(), "approved"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.enforcePolicy("db_mutate", args); err != nil {
		t.Fatalf("approved mutation was blocked: %v", err)
	}
	if err := runtime.enforcePolicy("db_mutate", args); err == nil {
		t.Fatal("one-time mutation approval was reused")
	}
}

func TestStrictPolicyBlocksDatabaseMutationBeforeApproval(t *testing.T) {
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.NoTunnel = true
	cfg.Policy = "strict"
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.enforcePolicy("db_mutate", mutationArgs()); err == nil || !strings.Contains(err.Error(), "policy=strict") {
		t.Fatalf("strict policy did not block mutation: %v", err)
	}
}

func TestDatabaseMutationAuditArgumentsExcludeValues(t *testing.T) {
	text := strings.TrimSpace(string(mustJSONBytes(databaseAuditArguments(mutationArgs()))))
	for _, forbidden := range []string{"new-private-value", "disabled", `"values"`, `"where"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("safe audit arguments leaked %q in %s", forbidden, text)
		}
	}
	for _, expected := range []string{"db.dev", "app", "users", "predicate_columns", "value_columns"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("safe audit arguments omitted %q in %s", expected, text)
		}
	}
}

func mustJSONBytes(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
