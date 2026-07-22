package security

import (
	"fmt"
	"testing"
	"time"

	"codebridge/internal/state"
)

func BenchmarkApprovalConsumeMissingWith1000Records(b *testing.B) {
	store, err := state.NewAt(b.TempDir(), b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	manager := NewApprovalManager(store, "operator-token", time.Minute)
	now := time.Now().UTC()
	for index := 0; index < 1_000; index++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012x", index)
		record := &ApprovalRecord{
			ID: id, Action: fmt.Sprintf("old-%d", index), Actions: []string{fmt.Sprintf("old-%d", index)},
			Status: "denied", Created: now.Format(time.RFC3339Nano),
			ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), DeniedAt: now.Format(time.RFC3339Nano),
		}
		if err := manager.write(record); err != nil {
			b.Fatal(err)
		}
	}
	_ = manager.Consume("warm-index")
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = manager.Consume("missing-action")
	}
}
