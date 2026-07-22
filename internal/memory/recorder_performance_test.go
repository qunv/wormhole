package memory

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type recorderBenchmarkDelayProvider struct{}

func (*recorderBenchmarkDelayProvider) Name() string { return "recorder-benchmark" }
func (*recorderBenchmarkDelayProvider) Capabilities() Capabilities {
	return Capabilities{Observe: true}
}
func (*recorderBenchmarkDelayProvider) Health(context.Context) HealthResult {
	return HealthResult{}
}
func (*recorderBenchmarkDelayProvider) Search(context.Context, SearchRequest) (SearchResult, error) {
	return SearchResult{}, nil
}
func (*recorderBenchmarkDelayProvider) Context(context.Context, ContextRequest) (ContextResult, error) {
	return ContextResult{}, nil
}
func (*recorderBenchmarkDelayProvider) Remember(context.Context, RememberRequest) (RememberResult, error) {
	return RememberResult{}, nil
}
func (*recorderBenchmarkDelayProvider) Observe(ctx context.Context, _ ObservationRequest) error {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*recorderBenchmarkDelayProvider) Forget(context.Context, ForgetRequest) (ForgetResult, error) {
	return ForgetResult{}, nil
}
func (*recorderBenchmarkDelayProvider) Close() error { return nil }

type concurrentRecorderBenchmarkDelayProvider struct {
	recorderBenchmarkDelayProvider
}

func (*concurrentRecorderBenchmarkDelayProvider) ConcurrencySafe() bool { return true }

func BenchmarkRecorderSingleWorkerDelivery(b *testing.B) {
	benchmarkRecorderDelivery(b, &recorderBenchmarkDelayProvider{}, 1)
}

func BenchmarkRecorderFourWorkerDelivery(b *testing.B) {
	benchmarkRecorderDelivery(b, &concurrentRecorderBenchmarkDelayProvider{}, 4)
}

func benchmarkRecorderDelivery(b *testing.B, provider Provider, workers int) {
	recorder := NewRecorderWithConfig(provider, RecorderConfig{
		QueueSize: max(b.N, 64), Workers: workers,
		DeliveryTimeout: time.Second, MaxAttempts: 1,
	})
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if !recorder.Record(ObservationRequest{SessionID: fmt.Sprintf("session-%d", index%32)}) {
			b.Fatal("benchmark recorder dropped an observation")
		}
	}
	recorder.Close()
}
