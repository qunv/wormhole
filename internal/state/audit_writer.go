// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// AuditWriterConfig bounds the in-memory queue and on-disk audit retention.
type AuditWriterConfig struct {
	QueueSize     int
	BatchSize     int
	FlushInterval time.Duration
	MaxBytes      int64
	MaxFiles      int
}

// AuditWriter batches append-only JSONL records for one audit path. Queue
// saturation falls back to a synchronous append so records are never silently
// dropped.
type AuditWriter struct {
	path   string
	config AuditWriterConfig
	queue  chan []byte
	flush  chan chan error
	stop   chan struct{}
	done   chan struct{}

	closeOnce sync.Once
	gate      sync.RWMutex
	closed    atomic.Bool

	enqueued       atomic.Uint64
	fallbackWrites atomic.Uint64
	batches        atomic.Uint64
	rotations      atomic.Uint64
	writeFailures  atomic.Uint64
	lastFailureMu  sync.Mutex
	lastFailureAt  time.Time
	lastFailure    string
}

func NewAuditWriter(path string, config AuditWriterConfig) *AuditWriter {
	if config.QueueSize <= 0 {
		config.QueueSize = 1024
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 64
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = 100 * time.Millisecond
	}
	if config.MaxBytes <= 0 {
		config.MaxBytes = 64 << 20
	}
	if config.MaxFiles <= 0 {
		config.MaxFiles = 5
	}
	writer := &AuditWriter{
		path: path, config: config,
		queue: make(chan []byte, config.QueueSize),
		flush: make(chan chan error), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go writer.run()
	return writer
}

func (w *AuditWriter) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

func (w *AuditWriter) Append(line []byte) error {
	if w == nil {
		return errors.New("audit writer is unavailable")
	}
	w.gate.RLock()
	defer w.gate.RUnlock()
	if w.closed.Load() {
		return errors.New("audit writer is closed")
	}
	copyLine := append([]byte(nil), line...)
	select {
	case w.queue <- copyLine:
		w.enqueued.Add(1)
		return nil
	default:
		w.fallbackWrites.Add(1)
		return w.writeBatch([][]byte{copyLine})
	}
}

func (w *AuditWriter) Flush(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.gate.Lock()
	defer w.gate.Unlock()
	if w.closed.Load() {
		select {
		case <-w.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ack := make(chan error, 1)
	select {
	case w.flush <- ack:
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-ack:
		return err
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *AuditWriter) Close() error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.gate.Lock()
		w.closed.Store(true)
		close(w.stop)
		w.gate.Unlock()
	})
	<-w.done
	w.lastFailureMu.Lock()
	defer w.lastFailureMu.Unlock()
	if w.lastFailure != "" {
		return errors.New(w.lastFailure)
	}
	return nil
}

func (w *AuditWriter) Stats() map[string]any {
	if w == nil {
		return map[string]any{"enabled": false}
	}
	result := map[string]any{
		"enabled": true, "closed": w.closed.Load(),
		"queue_depth": len(w.queue), "queue_capacity": cap(w.queue),
		"enqueued": w.enqueued.Load(), "fallback_writes": w.fallbackWrites.Load(),
		"batches": w.batches.Load(), "rotations": w.rotations.Load(),
		"write_failures": w.writeFailures.Load(),
		"max_bytes":      w.config.MaxBytes, "max_files": w.config.MaxFiles,
	}
	w.lastFailureMu.Lock()
	if !w.lastFailureAt.IsZero() {
		result["last_failure_at"] = w.lastFailureAt
		result["last_failure"] = w.lastFailure
	}
	w.lastFailureMu.Unlock()
	return result
}

func (w *AuditWriter) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.config.FlushInterval)
	defer ticker.Stop()
	batch := make([][]byte, 0, w.config.BatchSize)
	batchBytes := 0
	flushBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := w.writeBatch(batch)
		batch = batch[:0]
		batchBytes = 0
		return err
	}
	for {
		select {
		case line := <-w.queue:
			batch = append(batch, line)
			batchBytes += len(line)
			if len(batch) >= w.config.BatchSize || batchBytes >= 64<<10 {
				_ = flushBatch()
			}
		case ack := <-w.flush:
		drainFlush:
			for {
				select {
				case line := <-w.queue:
					batch = append(batch, line)
					batchBytes += len(line)
				default:
					break drainFlush
				}
			}
			ack <- flushBatch()
		case <-ticker.C:
			_ = flushBatch()
		case <-w.stop:
			for {
				select {
				case line := <-w.queue:
					batch = append(batch, line)
					batchBytes += len(line)
				default:
					_ = flushBatch()
					return
				}
			}
		}
	}
}

func (w *AuditWriter) writeBatch(lines [][]byte) error {
	if len(lines) == 0 {
		return nil
	}
	var data bytes.Buffer
	for _, line := range lines {
		_, _ = data.Write(line)
	}
	pathLock := statePathLock(w.path)
	pathLock.Lock()
	defer pathLock.Unlock()
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return w.recordFailure(err)
	}
	if err := w.rotateIfNeeded(int64(data.Len())); err != nil {
		return w.recordFailure(err)
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return w.recordFailure(err)
	}
	_, writeErr := file.Write(data.Bytes())
	closeErr := file.Close()
	if writeErr != nil {
		return w.recordFailure(writeErr)
	}
	if closeErr != nil {
		return w.recordFailure(closeErr)
	}
	w.batches.Add(1)
	return nil
}

func (w *AuditWriter) rotateIfNeeded(incoming int64) error {
	info, err := os.Stat(w.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err != nil || info.Size()+incoming <= w.config.MaxBytes {
		return nil
	}
	for index := w.config.MaxFiles - 1; index >= 1; index-- {
		from := fmt.Sprintf("%s.%d", w.path, index)
		to := fmt.Sprintf("%s.%d", w.path, index+1)
		if index == w.config.MaxFiles-1 {
			_ = os.Remove(to)
		}
		if err := os.Rename(from, to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	w.rotations.Add(1)
	return nil
}

func (w *AuditWriter) recordFailure(err error) error {
	w.writeFailures.Add(1)
	w.lastFailureMu.Lock()
	w.lastFailureAt = time.Now().UTC()
	w.lastFailure = err.Error()
	w.lastFailureMu.Unlock()
	return err
}
