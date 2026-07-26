// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

type RecorderConfig struct {
	QueueSize        int
	Workers          int
	DeliveryTimeout  time.Duration
	MaxAttempts      int
	RetryBackoff     time.Duration
	ShutdownTimeout  time.Duration
	FailureThreshold int
	FailureCooldown  time.Duration
}

type Recorder struct {
	provider Provider
	queues   []chan ObservationRequest
	stop     chan struct{}
	done     chan struct{}
	close    sync.Once
	gate     sync.RWMutex
	closed   bool
	config   RecorderConfig
	workers  int
	workerWG sync.WaitGroup

	breakerMu          sync.Mutex
	consecutiveFailure int
	circuitOpenUntil   time.Time

	deliveryCtx    context.Context
	cancelDelivery context.CancelFunc

	enqueued         atomic.Uint64
	dropped          atomic.Uint64
	delivered        atomic.Uint64
	failed           atomic.Uint64
	retried          atomic.Uint64
	abandoned        atomic.Uint64
	shutdownTimeouts atomic.Uint64
	retrySequence    atomic.Uint64
	breakerTrips     atomic.Uint64
	circuitSkipped   atomic.Uint64
}

func NewRecorder(provider Provider, queueSize int) *Recorder {
	return NewRecorderWithConfig(provider, RecorderConfig{QueueSize: queueSize})
}

func NewRecorderWithConfig(provider Provider, config RecorderConfig) *Recorder {
	if config.QueueSize <= 0 {
		config.QueueSize = 128
	}
	if config.Workers <= 0 {
		config.Workers = 4
	}
	if config.DeliveryTimeout <= 0 {
		config.DeliveryTimeout = 2 * time.Second
	}
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 3
	}
	if config.RetryBackoff <= 0 {
		config.RetryBackoff = 100 * time.Millisecond
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 5 * time.Second
	}
	if config.FailureThreshold <= 0 {
		config.FailureThreshold = 3
	}
	if config.FailureCooldown <= 0 {
		config.FailureCooldown = 10 * time.Second
	}
	workers := effectiveRecorderWorkers(provider, config.Workers, config.QueueSize)
	deliveryCtx, cancelDelivery := context.WithCancel(context.Background())
	r := &Recorder{
		provider:       provider,
		queues:         makeRecorderQueues(config.QueueSize, workers),
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
		config:         config,
		workers:        workers,
		deliveryCtx:    deliveryCtx,
		cancelDelivery: cancelDelivery,
	}
	for index := range r.queues {
		r.workerWG.Add(1)
		go r.runWorker(r.queues[index])
	}
	go func() {
		r.workerWG.Wait()
		r.cancelDelivery()
		close(r.done)
	}()
	return r
}

func effectiveRecorderWorkers(provider Provider, requested, queueSize int) int {
	workers := min(max(requested, 1), 32)
	workers = min(workers, max(queueSize, 1))
	concurrent := false
	if marker, ok := provider.(ConcurrencySafeProvider); ok {
		concurrent = marker.ConcurrencySafe()
	}
	if !concurrent {
		return 1
	}
	return workers
}

func makeRecorderQueues(queueSize, workers int) []chan ObservationRequest {
	queues := make([]chan ObservationRequest, workers)
	base, remainder := queueSize/workers, queueSize%workers
	for index := range queues {
		capacity := base
		if index < remainder {
			capacity++
		}
		queues[index] = make(chan ObservationRequest, max(capacity, 1))
	}
	return queues
}

func (r *Recorder) Record(request ObservationRequest) bool {
	if r == nil {
		return false
	}
	r.gate.RLock()
	defer r.gate.RUnlock()
	if r.closed {
		return false
	}
	queue := r.queues[r.shard(request.SessionID)]
	select {
	case queue <- request:
		r.enqueued.Add(1)
		return true
	default:
		r.dropped.Add(1)
		return false
	}
}

func (r *Recorder) shard(sessionID string) int {
	if len(r.queues) <= 1 || sessionID == "" {
		return 0
	}
	hash := uint32(2166136261)
	for index := 0; index < len(sessionID); index++ {
		hash ^= uint32(sessionID[index])
		hash *= 16777619
	}
	return int(hash % uint32(len(r.queues)))
}

func (r *Recorder) Stats() map[string]any {
	if r == nil {
		return map[string]any{"enabled": false}
	}
	depth, capacity := 0, 0
	shardCapacityMin, shardCapacityMax := 0, 0
	for index, queue := range r.queues {
		depth += len(queue)
		capacity += cap(queue)
		if index == 0 || cap(queue) < shardCapacityMin {
			shardCapacityMin = cap(queue)
		}
		if cap(queue) > shardCapacityMax {
			shardCapacityMax = cap(queue)
		}
	}
	r.breakerMu.Lock()
	openUntil := r.circuitOpenUntil
	consecutiveFailures := r.consecutiveFailure
	r.breakerMu.Unlock()
	result := map[string]any{
		"enabled": true, "workers": r.workers, "sharded": r.workers > 1,
		"queue_depth": depth, "queue_capacity": capacity,
		"shard_capacity_min": shardCapacityMin, "shard_capacity_max": shardCapacityMax,
		"enqueued": r.enqueued.Load(), "delivered": r.delivered.Load(),
		"failed": r.failed.Load(), "retried": r.retried.Load(), "dropped": r.dropped.Load(),
		"abandoned": r.abandoned.Load(), "shutdown_timeouts": r.shutdownTimeouts.Load(),
		"breaker_trips": r.breakerTrips.Load(), "circuit_skipped": r.circuitSkipped.Load(),
		"consecutive_failures": consecutiveFailures,
	}
	if time.Now().Before(openUntil) {
		result["circuit_open"] = true
		result["circuit_open_until"] = openUntil
	} else {
		result["circuit_open"] = false
	}
	return result
}

// Close stops accepting observations and drains the queues for at most the
// configured shutdown timeout. It is safe to call more than once.
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.config.ShutdownTimeout)
	defer cancel()
	_ = r.CloseContext(ctx)
}

// CloseContext stops accepting observations and waits for queued deliveries.
// When ctx expires, in-flight provider calls are cancelled and remaining queued
// observations are counted as abandoned instead of blocking daemon shutdown.
func (r *Recorder) CloseContext(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.close.Do(func() {
		r.gate.Lock()
		r.closed = true
		close(r.stop)
		r.gate.Unlock()
	})
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		r.shutdownTimeouts.Add(1)
		r.cancelDelivery()
		return ctx.Err()
	}
}

func (r *Recorder) runWorker(queue chan ObservationRequest) {
	defer r.workerWG.Done()
	for {
		select {
		case <-r.deliveryCtx.Done():
			r.abandonQueue(queue)
			return
		case <-r.stop:
			r.drainQueue(queue)
			return
		case request := <-queue:
			r.deliver(request)
		}
	}
}

func (r *Recorder) drainQueue(queue chan ObservationRequest) {
	for {
		select {
		case <-r.deliveryCtx.Done():
			r.abandonQueue(queue)
			return
		case request := <-queue:
			r.deliver(request)
		default:
			return
		}
	}
}

func (r *Recorder) abandonQueue(queue chan ObservationRequest) {
	for {
		select {
		case <-queue:
			r.abandoned.Add(1)
		default:
			return
		}
	}
}

func (r *Recorder) deliver(request ObservationRequest) {
	if r.circuitOpen() {
		r.circuitSkipped.Add(1)
		r.failed.Add(1)
		return
	}
	for attempt := 1; attempt <= r.config.MaxAttempts; attempt++ {
		if r.deliveryCtx.Err() != nil {
			r.abandoned.Add(1)
			return
		}
		ctx, cancel := context.WithTimeout(r.deliveryCtx, r.config.DeliveryTimeout)
		err := r.provider.Observe(ctx, request)
		cancel()
		if err == nil {
			r.clearBreaker()
			r.delivered.Add(1)
			return
		}
		if r.deliveryCtx.Err() != nil {
			r.abandoned.Add(1)
			return
		}
		if !retryableDeliveryError(err) {
			r.failed.Add(1)
			return
		}
		if r.recordDeliveryFailure() {
			r.failed.Add(1)
			return
		}
		if attempt < r.config.MaxAttempts {
			r.retried.Add(1)
			timer := time.NewTimer(r.retryDelay(attempt))
			select {
			case <-timer.C:
			case <-r.deliveryCtx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				r.abandoned.Add(1)
				return
			}
		}
	}
	r.failed.Add(1)
}

func (r *Recorder) circuitOpen() bool {
	r.breakerMu.Lock()
	defer r.breakerMu.Unlock()
	if r.circuitOpenUntil.IsZero() || !time.Now().Before(r.circuitOpenUntil) {
		r.circuitOpenUntil = time.Time{}
		return false
	}
	return true
}

func (r *Recorder) recordDeliveryFailure() bool {
	r.breakerMu.Lock()
	defer r.breakerMu.Unlock()
	r.consecutiveFailure++
	if r.consecutiveFailure < r.config.FailureThreshold {
		return false
	}
	r.consecutiveFailure = 0
	r.circuitOpenUntil = time.Now().Add(r.config.FailureCooldown)
	r.breakerTrips.Add(1)
	return true
}

func (r *Recorder) clearBreaker() {
	r.breakerMu.Lock()
	r.consecutiveFailure = 0
	r.circuitOpenUntil = time.Time{}
	r.breakerMu.Unlock()
}

func retryableDeliveryError(err error) bool {
	var marker interface{ Retryable() bool }
	if errors.As(err, &marker) {
		return marker.Retryable()
	}
	return true
}

func (r *Recorder) retryDelay(attempt int) time.Duration {
	delay := r.config.RetryBackoff
	for index := 1; index < attempt; index++ {
		delay *= 2
		if delay >= 2*time.Second {
			delay = 2 * time.Second
			break
		}
	}
	// Deterministic per-recorder jitter prevents synchronized retries without a
	// shared random-number lock or nondeterministic tests.
	sequence := r.retrySequence.Add(1)
	percent := 80 + int((sequence*1103515245+12345)%41)
	return time.Duration(int64(delay) * int64(percent) / 100)
}
