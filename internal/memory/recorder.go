// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type RecorderConfig struct {
	QueueSize       int
	DeliveryTimeout time.Duration
	MaxAttempts     int
	RetryBackoff    time.Duration
	ShutdownTimeout time.Duration
}

type Recorder struct {
	provider Provider
	queue    chan ObservationRequest
	stop     chan struct{}
	done     chan struct{}
	close    sync.Once
	gate     sync.RWMutex
	closed   bool
	config   RecorderConfig

	deliveryCtx    context.Context
	cancelDelivery context.CancelFunc

	enqueued         atomic.Uint64
	dropped          atomic.Uint64
	delivered        atomic.Uint64
	failed           atomic.Uint64
	retried          atomic.Uint64
	abandoned        atomic.Uint64
	shutdownTimeouts atomic.Uint64
}

func NewRecorder(provider Provider, queueSize int) *Recorder {
	return NewRecorderWithConfig(provider, RecorderConfig{QueueSize: queueSize})
}

func NewRecorderWithConfig(provider Provider, config RecorderConfig) *Recorder {
	if config.QueueSize <= 0 {
		config.QueueSize = 128
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
	deliveryCtx, cancelDelivery := context.WithCancel(context.Background())
	r := &Recorder{
		provider:       provider,
		queue:          make(chan ObservationRequest, config.QueueSize),
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
		config:         config,
		deliveryCtx:    deliveryCtx,
		cancelDelivery: cancelDelivery,
	}
	go r.run()
	return r
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
	select {
	case r.queue <- request:
		r.enqueued.Add(1)
		return true
	default:
		r.dropped.Add(1)
		return false
	}
}

func (r *Recorder) Stats() map[string]any {
	if r == nil {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled": true, "queue_depth": len(r.queue), "queue_capacity": cap(r.queue),
		"enqueued": r.enqueued.Load(), "delivered": r.delivered.Load(),
		"failed": r.failed.Load(), "retried": r.retried.Load(), "dropped": r.dropped.Load(),
		"abandoned": r.abandoned.Load(), "shutdown_timeouts": r.shutdownTimeouts.Load(),
	}
}

// Close stops accepting observations and drains the queue for at most the
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

func (r *Recorder) run() {
	defer close(r.done)
	defer r.cancelDelivery()
	for {
		select {
		case <-r.deliveryCtx.Done():
			r.abandonQueued()
			return
		case <-r.stop:
			r.drain()
			return
		case request := <-r.queue:
			r.deliver(request)
		}
	}
}

func (r *Recorder) drain() {
	for {
		select {
		case <-r.deliveryCtx.Done():
			r.abandonQueued()
			return
		case request := <-r.queue:
			r.deliver(request)
		default:
			return
		}
	}
}

func (r *Recorder) abandonQueued() {
	for {
		select {
		case <-r.queue:
			r.abandoned.Add(1)
		default:
			return
		}
	}
}

func (r *Recorder) deliver(request ObservationRequest) {
	for attempt := 1; attempt <= r.config.MaxAttempts; attempt++ {
		if r.deliveryCtx.Err() != nil {
			r.abandoned.Add(1)
			return
		}
		ctx, cancel := context.WithTimeout(r.deliveryCtx, r.config.DeliveryTimeout)
		err := r.provider.Observe(ctx, request)
		cancel()
		if err == nil {
			r.delivered.Add(1)
			return
		}
		if r.deliveryCtx.Err() != nil {
			r.abandoned.Add(1)
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

func (r *Recorder) retryDelay(attempt int) time.Duration {
	delay := r.config.RetryBackoff
	for index := 1; index < attempt; index++ {
		delay *= 2
		if delay >= 2*time.Second {
			return 2 * time.Second
		}
	}
	return delay
}
