package memory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type alwaysFailRecorderProvider struct {
	recorderProvider
	calls atomic.Int64
}

func (p *alwaysFailRecorderProvider) Observe(context.Context, ObservationRequest) error {
	p.calls.Add(1)
	return errors.New("provider unavailable")
}

func TestRecorderCircuitBreakerSkipsUnavailableProvider(t *testing.T) {
	provider := &alwaysFailRecorderProvider{}
	recorder := NewRecorderWithConfig(provider, RecorderConfig{
		QueueSize: 8, Workers: 1, DeliveryTimeout: time.Second,
		MaxAttempts: 3, RetryBackoff: time.Millisecond,
		FailureThreshold: 2, FailureCooldown: time.Hour,
	})
	defer recorder.Close()

	if !recorder.Record(ObservationRequest{SessionID: "first"}) {
		t.Fatal("first observation was not accepted")
	}
	waitRecorderStat(t, recorder, "breaker_trips", 1)
	if got := provider.calls.Load(); got != 2 {
		t.Fatalf("provider calls before open = %d, want 2", got)
	}

	if !recorder.Record(ObservationRequest{SessionID: "second"}) {
		t.Fatal("second observation was not accepted")
	}
	waitRecorderStat(t, recorder, "circuit_skipped", 1)
	if got := provider.calls.Load(); got != 2 {
		t.Fatalf("provider called while circuit open: %d", got)
	}
}

func waitRecorderStat(t *testing.T, recorder *Recorder, key string, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got, _ := recorder.Stats()[key].(uint64); got >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("recorder stat %s did not reach %d: %#v", key, want, recorder.Stats())
}
