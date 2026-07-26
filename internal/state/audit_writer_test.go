package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuditWriterFlushesBatchedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writer := NewAuditWriter(path, AuditWriterConfig{
		QueueSize: 32, BatchSize: 4, FlushInterval: time.Hour,
		MaxBytes: 1 << 20, MaxFiles: 2,
	})
	t.Cleanup(func() { _ = writer.Close() })
	for index := 0; index < 10; index++ {
		if err := writer.Append([]byte(fmt.Sprintf("record-%d\n", index))); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 10 || lines[0] != "record-0" || lines[9] != "record-9" {
		t.Fatalf("unexpected audit records: %#v", lines)
	}
	stats := writer.Stats()
	if stats["enqueued"] != uint64(10) || stats["write_failures"] != uint64(0) {
		t.Fatalf("unexpected audit stats: %#v", stats)
	}
}

func TestAuditWriterQueuePressurePreservesFIFOWithoutCallerDiskWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	pathLock := statePathLock(path)
	pathLock.Lock()

	writer := NewAuditWriter(path, AuditWriterConfig{
		QueueSize: 1, BatchSize: 1, FlushInterval: time.Hour,
		MaxBytes: 1 << 20, MaxFiles: 2,
	})
	if err := writer.Append([]byte("first\n")); err != nil {
		pathLock.Unlock()
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(writer.queue) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(writer.queue) != 0 {
		pathLock.Unlock()
		t.Fatal("audit worker did not start the first blocked write")
	}
	if err := writer.Append([]byte("second\n")); err != nil {
		pathLock.Unlock()
		t.Fatal(err)
	}

	thirdDone := make(chan error, 1)
	go func() { thirdDone <- writer.Append([]byte("third\n")) }()
	select {
	case err := <-thirdDone:
		pathLock.Unlock()
		t.Fatalf("pressured append returned before queue drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	pathLock.Unlock()

	select {
	case err := <-thirdDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pressured append did not resume after the writer drained")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != "first\nsecond\nthird\n" {
		t.Fatalf("audit order = %q", got)
	}
	stats := writer.Stats()
	if stats["backpressure_waits"] != uint64(1) || stats["fallback_writes"] != uint64(0) {
		t.Fatalf("unexpected queue-pressure stats: %#v", stats)
	}
}

func TestAuditWriterRotatesAndRetainsBoundedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writer := NewAuditWriter(path, AuditWriterConfig{
		QueueSize: 8, BatchSize: 1, FlushInterval: time.Hour,
		MaxBytes: 16, MaxFiles: 2,
	})
	for index := 0; index < 6; index++ {
		if err := writer.Append([]byte(fmt.Sprintf("record-%d\n", index))); err != nil {
			t.Fatal(err)
		}
		if err := writer.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("first rotated audit file missing: %v", err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("rotation exceeded retention: %v", err)
	}
	if writer.Stats()["rotations"].(uint64) == 0 {
		t.Fatal("audit rotation was not recorded")
	}
}

func TestAuditWriterCloseReturnsFinalWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit-directory")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	writer := NewAuditWriter(path, AuditWriterConfig{
		QueueSize: 8, BatchSize: 8, FlushInterval: time.Hour,
		MaxBytes: 1 << 20, MaxFiles: 2,
	})
	if err := writer.Append([]byte("record\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err == nil {
		t.Fatal("close hid final audit write failure")
	}
	if writer.Stats()["write_failures"] != uint64(1) {
		t.Fatalf("write failure was not counted: %#v", writer.Stats())
	}
}

func TestAuditWriterCloseDoesNotLoseAcceptedConcurrentRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writer := NewAuditWriter(path, AuditWriterConfig{
		QueueSize: 256, BatchSize: 16, FlushInterval: time.Hour,
		MaxBytes: 1 << 20, MaxFiles: 2,
	})
	var accepted int
	var acceptedMu sync.Mutex
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for index := 0; index < 100; index++ {
				if err := writer.Append([]byte(fmt.Sprintf("%d-%d\n", worker, index))); err == nil {
					acceptedMu.Lock()
					accepted++
					acceptedMu.Unlock()
				}
			}
		}(worker)
	}
	wg.Wait()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := len(strings.Split(strings.TrimSpace(string(raw)), "\n"))
	if got != accepted {
		t.Fatalf("persisted %d accepted records, want %d", got, accepted)
	}
}
