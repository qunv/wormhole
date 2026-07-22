package security

import (
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
