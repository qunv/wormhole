// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type recorderProvider struct {
	observed chan ObservationRequest
}

func (*recorderProvider) Name() string                        { return "test" }
func (*recorderProvider) Capabilities() Capabilities          { return Capabilities{Observe: true} }
func (*recorderProvider) Health(context.Context) HealthResult { return HealthResult{Available: true} }
func (*recorderProvider) Search(context.Context, SearchRequest) (SearchResult, error) {
	return SearchResult{}, nil
}
func (*recorderProvider) Context(context.Context, ContextRequest) (ContextResult, error) {
	return ContextResult{}, nil
}
func (*recorderProvider) Remember(context.Context, RememberRequest) (RememberResult, error) {
	return RememberResult{}, nil
}
func (p *recorderProvider) Observe(_ context.Context, request ObservationRequest) error {
	p.observed <- request
	return nil
}
func (*recorderProvider) Forget(context.Context, ForgetRequest) (ForgetResult, error) {
	return ForgetResult{}, nil
}
func (*recorderProvider) Close() error { return nil }

func TestRecorderDeliversObservation(t *testing.T) {
	provider := &recorderProvider{observed: make(chan ObservationRequest, 1)}
	recorder := NewRecorder(provider, 4)
	defer recorder.Close()
	request := ObservationRequest{HookType: "PostToolUse", SessionID: "session-1"}
	if !recorder.Record(request) {
		t.Fatal("observation was not queued")
	}
	select {
	case got := <-provider.observed:
		if got.SessionID != request.SessionID {
			t.Fatalf("session ID = %q", got.SessionID)
		}
	case <-time.After(time.Second):
		t.Fatal("observation was not delivered")
	}
}

func TestRecorderDrainsQueuedObservationsOnClose(t *testing.T) {
	provider := &recorderProvider{observed: make(chan ObservationRequest, 4)}
	recorder := NewRecorderWithConfig(provider, RecorderConfig{
		QueueSize: 4, DeliveryTimeout: time.Second, MaxAttempts: 1,
	})
	for index := 0; index < 3; index++ {
		if !recorder.Record(ObservationRequest{SessionID: string(rune('a' + index))}) {
			t.Fatalf("observation %d was not queued", index)
		}
	}
	recorder.Close()
	if got := len(provider.observed); got != 3 {
		t.Fatalf("delivered observations = %d, want 3", got)
	}
	if stats := recorder.Stats(); stats["delivered"] != uint64(3) {
		t.Fatalf("unexpected recorder stats: %#v", stats)
	}
}

type flakyRecorderProvider struct {
	attempts atomic.Int32
}

func (*flakyRecorderProvider) Name() string               { return "flaky" }
func (*flakyRecorderProvider) Capabilities() Capabilities { return Capabilities{Observe: true} }
func (*flakyRecorderProvider) Health(context.Context) HealthResult {
	return HealthResult{Available: true}
}
func (*flakyRecorderProvider) Search(context.Context, SearchRequest) (SearchResult, error) {
	return SearchResult{}, nil
}
func (*flakyRecorderProvider) Context(context.Context, ContextRequest) (ContextResult, error) {
	return ContextResult{}, nil
}
func (*flakyRecorderProvider) Remember(context.Context, RememberRequest) (RememberResult, error) {
	return RememberResult{}, nil
}
func (p *flakyRecorderProvider) Observe(context.Context, ObservationRequest) error {
	if p.attempts.Add(1) < 3 {
		return errors.New("temporary failure")
	}
	return nil
}
func (*flakyRecorderProvider) Forget(context.Context, ForgetRequest) (ForgetResult, error) {
	return ForgetResult{}, nil
}
func (*flakyRecorderProvider) Close() error { return nil }

func TestRecorderRetriesTransientFailures(t *testing.T) {
	provider := &flakyRecorderProvider{}
	recorder := NewRecorderWithConfig(provider, RecorderConfig{
		QueueSize: 1, DeliveryTimeout: time.Second, MaxAttempts: 3, RetryBackoff: time.Millisecond,
	})
	if !recorder.Record(ObservationRequest{SessionID: "retry"}) {
		t.Fatal("observation was not queued")
	}
	recorder.Close()
	stats := recorder.Stats()
	if stats["delivered"] != uint64(1) || stats["retried"] != uint64(2) || stats["failed"] != uint64(0) {
		t.Fatalf("unexpected retry stats: %#v", stats)
	}
}

type blockingRecorderProvider struct {
	release chan struct{}
	started chan struct{}
}

func (*blockingRecorderProvider) Name() string               { return "blocking" }
func (*blockingRecorderProvider) Capabilities() Capabilities { return Capabilities{Observe: true} }
func (*blockingRecorderProvider) Health(context.Context) HealthResult {
	return HealthResult{Available: true}
}
func (*blockingRecorderProvider) Search(context.Context, SearchRequest) (SearchResult, error) {
	return SearchResult{}, nil
}
func (*blockingRecorderProvider) Context(context.Context, ContextRequest) (ContextResult, error) {
	return ContextResult{}, nil
}
func (*blockingRecorderProvider) Remember(context.Context, RememberRequest) (RememberResult, error) {
	return RememberResult{}, nil
}
func (p *blockingRecorderProvider) Observe(context.Context, ObservationRequest) error {
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	<-p.release
	return nil
}
func (*blockingRecorderProvider) Forget(context.Context, ForgetRequest) (ForgetResult, error) {
	return ForgetResult{}, nil
}
func (*blockingRecorderProvider) Close() error { return nil }

func TestRecorderCountsDroppedObservationsWhenQueueIsFull(t *testing.T) {
	provider := &blockingRecorderProvider{release: make(chan struct{})}
	recorder := NewRecorderWithConfig(provider, RecorderConfig{QueueSize: 1, MaxAttempts: 1, DeliveryTimeout: time.Second})
	if !recorder.Record(ObservationRequest{SessionID: "first"}) {
		t.Fatal("first observation was not queued")
	}
	time.Sleep(10 * time.Millisecond)
	if !recorder.Record(ObservationRequest{SessionID: "second"}) {
		t.Fatal("second observation was not queued")
	}
	if recorder.Record(ObservationRequest{SessionID: "third"}) {
		t.Fatal("third observation should have been dropped")
	}
	close(provider.release)
	recorder.Close()
	if got := recorder.Stats()["dropped"]; got != uint64(1) {
		t.Fatalf("dropped = %#v, want 1", got)
	}
}

type shutdownRecorderProvider struct {
	started chan struct{}
}

func (*shutdownRecorderProvider) Name() string               { return "shutdown" }
func (*shutdownRecorderProvider) Capabilities() Capabilities { return Capabilities{Observe: true} }
func (*shutdownRecorderProvider) Health(context.Context) HealthResult {
	return HealthResult{Available: true}
}
func (*shutdownRecorderProvider) Search(context.Context, SearchRequest) (SearchResult, error) {
	return SearchResult{}, nil
}
func (*shutdownRecorderProvider) Context(context.Context, ContextRequest) (ContextResult, error) {
	return ContextResult{}, nil
}
func (*shutdownRecorderProvider) Remember(context.Context, RememberRequest) (RememberResult, error) {
	return RememberResult{}, nil
}
func (p *shutdownRecorderProvider) Observe(ctx context.Context, _ ObservationRequest) error {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}
func (*shutdownRecorderProvider) Forget(context.Context, ForgetRequest) (ForgetResult, error) {
	return ForgetResult{}, nil
}
func (*shutdownRecorderProvider) Close() error { return nil }

func TestRecorderShutdownDeadlineDropsRemainingQueue(t *testing.T) {
	provider := &shutdownRecorderProvider{started: make(chan struct{}, 1)}
	recorder := NewRecorderWithConfig(provider, RecorderConfig{
		QueueSize: 4, DeliveryTimeout: time.Second, MaxAttempts: 3,
		RetryBackoff: time.Second, ShutdownTimeout: 40 * time.Millisecond,
	})
	if !recorder.Record(ObservationRequest{SessionID: "active"}) {
		t.Fatal("active observation was not queued")
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start delivery")
	}
	for index := 0; index < 3; index++ {
		if !recorder.Record(ObservationRequest{SessionID: "queued"}) {
			t.Fatalf("queued observation %d was not accepted", index)
		}
	}
	started := time.Now()
	recorder.Close()
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("recorder shutdown exceeded deadline: %s", elapsed)
	}
	deadline := time.Now().Add(time.Second)
	stats := recorder.Stats()
	for stats["abandoned"].(uint64) < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		stats = recorder.Stats()
	}
	if stats["abandoned"].(uint64) != 4 || stats["failed"].(uint64) != 0 || stats["shutdown_timeouts"].(uint64) != 1 {
		t.Fatalf("unexpected shutdown stats: %#v", stats)
	}
}

func TestRecorderCloseContextHonorsCallerDeadline(t *testing.T) {
	provider := &blockingRecorderProvider{release: make(chan struct{}), started: make(chan struct{}, 1)}
	recorder := NewRecorderWithConfig(provider, RecorderConfig{
		QueueSize: 1, DeliveryTimeout: time.Second, MaxAttempts: 1, ShutdownTimeout: time.Second,
	})
	if !recorder.Record(ObservationRequest{SessionID: "blocked"}) {
		t.Fatal("observation was not queued")
	}
	<-provider.started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := recorder.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext error = %v, want deadline exceeded", err)
	}
	close(provider.release)
	recorder.Close()
}
