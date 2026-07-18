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
}

type Recorder struct {
	provider  Provider
	queue     chan ObservationRequest
	stop      chan struct{}
	done      chan struct{}
	close     sync.Once
	gate      sync.RWMutex
	closed    bool
	config    RecorderConfig
	enqueued  atomic.Uint64
	dropped   atomic.Uint64
	delivered atomic.Uint64
	failed    atomic.Uint64
	retried   atomic.Uint64
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
	r := &Recorder{
		provider: provider,
		queue:    make(chan ObservationRequest, config.QueueSize),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		config:   config,
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
	}
}

func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.close.Do(func() {
		r.gate.Lock()
		r.closed = true
		close(r.stop)
		r.gate.Unlock()
		<-r.done
	})
}

func (r *Recorder) run() {
	defer close(r.done)
	for {
		select {
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
		case request := <-r.queue:
			r.deliver(request)
		default:
			return
		}
	}
}

func (r *Recorder) deliver(request ObservationRequest) {
	for attempt := 1; attempt <= r.config.MaxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), r.config.DeliveryTimeout)
		err := r.provider.Observe(ctx, request)
		cancel()
		if err == nil {
			r.delivered.Add(1)
			return
		}
		if attempt < r.config.MaxAttempts {
			r.retried.Add(1)
			time.Sleep(r.retryDelay(attempt))
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
