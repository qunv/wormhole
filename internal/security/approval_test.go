package security

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codebridge/internal/state"
)

func TestApprovalConsumeIsExactOnceUnderConcurrency(t *testing.T) {
	manager := newTestApprovalManager(t)
	record, err := manager.Request([]string{"delete:file.txt"}, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Decide(record.ID, "operator-token", "approved"); err != nil {
		t.Fatal(err)
	}

	var succeeded atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if manager.Consume("delete:file.txt") == nil {
				succeeded.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := succeeded.Load(); got != 1 {
		t.Fatalf("successful consumes = %d, want 1", got)
	}

	stored, err := manager.read(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "consumed" {
		t.Fatalf("status = %q, want consumed", stored.Status)
	}
	if len(stored.ConsumedActions) != 1 || stored.ConsumedActions[0] != "delete:file.txt" {
		t.Fatalf("consumed actions = %#v", stored.ConsumedActions)
	}
}

func TestMalformedApprovalExpiryFailsClosed(t *testing.T) {
	manager := newTestApprovalManager(t)
	record, err := manager.Request([]string{"run:risky"}, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Decide(record.ID, "operator-token", "approved"); err != nil {
		t.Fatal(err)
	}
	stored, err := manager.read(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.ExpiresAt = "invalid"
	if err := manager.write(stored); err != nil {
		t.Fatal(err)
	}

	if err := manager.Consume("run:risky"); err == nil {
		t.Fatal("malformed expiry must not authorize an action")
	}
	stored, err = manager.read(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "expired" {
		t.Fatalf("status = %q, want expired", stored.Status)
	}
}

func TestApprovalDecideReloadsPersistedRecord(t *testing.T) {
	manager := newTestApprovalManager(t)
	record, err := manager.Request([]string{"run:reload"}, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := manager.read(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Status = "denied"
	stored.DeniedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := manager.write(stored); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Decide(record.ID, "operator-token", "approved"); err == nil {
		t.Fatal("decision ignored externally persisted terminal status")
	}
}

func TestApprovalRecordSizeIsBounded(t *testing.T) {
	manager := newTestApprovalManager(t)
	id, err := uuid4()
	if err != nil {
		t.Fatal(err)
	}
	record := &ApprovalRecord{
		ID: id, Action: "large", Actions: []string{"large"}, Status: "pending",
		Created:   time.Now().UTC().Format(time.RFC3339Nano),
		ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
		Reason:    strings.Repeat("x", maxApprovalRecordSize),
	}
	if err := manager.write(record); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.read(id); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized approval record was accepted: %v", err)
	}
}

func TestApprovalConsumeUsesMostRecentlyApprovedRecord(t *testing.T) {
	manager := newTestApprovalManager(t)
	first, err := manager.Request([]string{"run:deploy"}, "first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Decide(first.ID, "operator-token", "approved"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	second, err := manager.Request([]string{"run:deploy"}, "second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Decide(second.ID, "operator-token", "approved"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Consume("run:deploy"); err != nil {
		t.Fatal(err)
	}
	firstStored, err := manager.read(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondStored, err := manager.read(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstStored.Status != "approved" || secondStored.Status != "consumed" {
		t.Fatalf("unexpected selection: first=%s second=%s", firstStored.Status, secondStored.Status)
	}
}

func TestApprovalManagerRestoresPersistedActiveIndex(t *testing.T) {
	workspace, dataDir := t.TempDir(), t.TempDir()
	store, err := state.NewAt(workspace, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	first := NewApprovalManager(store, "operator-token", time.Minute)
	record, err := first.Request([]string{"git:push"}, "persisted", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Decide(record.ID, "operator-token", "approved"); err != nil {
		t.Fatal(err)
	}
	restarted := NewApprovalManager(store, "operator-token", time.Minute)
	if err := restarted.Consume("git:push"); err != nil {
		t.Fatalf("persisted approval was not restored: %v", err)
	}
	stored, err := restarted.read(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "consumed" {
		t.Fatalf("status = %q, want consumed", stored.Status)
	}
}

func TestUUID4ReturnsValidVersion4Identifier(t *testing.T) {
	id, err := uuid4()
	if err != nil {
		t.Fatal(err)
	}
	if !approvalID.MatchString(id) {
		t.Fatalf("invalid UUIDv4: %q", id)
	}
}

func newTestApprovalManager(t *testing.T) *ApprovalManager {
	t.Helper()
	workspace := t.TempDir()
	store, err := state.NewAt(workspace, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewApprovalManager(store, "operator-token", time.Minute)
}
