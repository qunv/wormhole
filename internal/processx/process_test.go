package processx

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProcessHelper(t *testing.T) {
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "long":
		for index := 0; index < 20; index++ {
			fmt.Fprintf(os.Stdout, "line-%d\n", index)
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(10 * time.Second)
	case "quick":
		fmt.Fprintln(os.Stdout, "done")
	}
}

func TestRunTimeoutTerminatesProcessGroup(t *testing.T) {
	started := time.Now()
	result := Run(nilContext(), helperCommand("long"), t.TempDir(), "", 100*time.Millisecond, 10_000)
	if !result.TimedOut || result.ExitCode != -1 {
		t.Fatalf("unexpected timeout result: %#v", result)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}

func TestRegistryConcurrentOutputStopAndWait(t *testing.T) {
	registry := NewRegistry(2)
	t.Cleanup(registry.StopAll)
	proc, err := registry.Start(helperCommand("long"), t.TempDir(), "", "helper")
	if err != nil {
		t.Fatal(err)
	}
	waitForProcessStatus(t, registry, proc.ID, processRunning, time.Second)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := 0; index < 100; index++ {
				_, _ = registry.Output(proc.ID, 128)
				_ = registry.List()
			}
		}()
	}
	if err := registry.Stop(proc.ID); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	waitForProcessStatus(t, registry, proc.ID, processStopped, 3*time.Second)

	output, err := registry.Output(proc.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if output["status"] != processStopped {
		t.Fatalf("status = %v, want stopped", output["status"])
	}
	if output["exit_code"] == nil {
		t.Fatalf("exit code was not recorded: %#v", output)
	}
}

func TestRegistryRetainsBoundedCompletedHistory(t *testing.T) {
	registry := NewRegistry(2)
	registry.retention = 3
	t.Cleanup(registry.StopAll)
	for index := 0; index < 8; index++ {
		proc, err := registry.Start(helperCommand("quick"), t.TempDir(), "", "")
		if err != nil {
			t.Fatal(err)
		}
		waitForProcessStatus(t, registry, proc.ID, processExited, 3*time.Second)
	}
	registry.mu.RLock()
	count := len(registry.items)
	registry.mu.RUnlock()
	if count > registry.retention {
		t.Fatalf("retained %d processes, limit %d", count, registry.retention)
	}
}

func TestLockedBufferHeadAndTailLimits(t *testing.T) {
	head := &lockedBuffer{limit: 5, keepHead: true}
	_, _ = head.Write([]byte("abcdef"))
	_, _ = head.Write([]byte("gh"))
	if got := head.String(); got != "abcde" || !head.Truncated() {
		t.Fatalf("head buffer = %q truncated=%v", got, head.Truncated())
	}

	tail := &lockedBuffer{limit: 5}
	_, _ = tail.Write([]byte("abc"))
	_, _ = tail.Write([]byte("defg"))
	if got := tail.String(); got != "cdefg" || !tail.Truncated() {
		t.Fatalf("tail buffer = %q truncated=%v", got, tail.Truncated())
	}
	_, _ = tail.Write([]byte("hijk"))
	if got := tail.String(); got != "ghijk" {
		t.Fatalf("wrapped tail buffer = %q", got)
	}
	_, _ = tail.Write([]byte("0123456789"))
	if got := tail.String(); got != "56789" {
		t.Fatalf("large-write tail buffer = %q", got)
	}
	if got := tail.TailString(3); got != "789" {
		t.Fatalf("bounded tail read = %q", got)
	}
}

func BenchmarkLockedBufferRollingTailAfterFull(b *testing.B) {
	buffer := &lockedBuffer{limit: 200_000}
	chunk := make([]byte, 4096)
	_, _ = buffer.Write(make([]byte, 200_000))
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_, _ = buffer.Write(chunk)
	}
}

func BenchmarkLockedBufferTailRead128(b *testing.B) {
	buffer := &lockedBuffer{limit: 200_000}
	_, _ = buffer.Write(make([]byte, 200_000))
	_, _ = buffer.Write(make([]byte, 4096))
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = buffer.TailString(128)
	}
}

func helperCommand(mode string) string {
	executable := os.Args[0]
	if runtime.GOOS == "windows" {
		executable = `"` + strings.ReplaceAll(executable, `"`, `""`) + `"`
	} else {
		executable = `'` + strings.ReplaceAll(executable, `'`, `'"'"'`) + `'`
	}
	return fmt.Sprintf("%s -test.run=^TestProcessHelper$ -- %s", executable, mode)
}

func waitForProcessStatus(t *testing.T, registry *Registry, id, expected string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output, err := registry.Output(id, 0)
		if err == nil && output["status"] == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	output, err := registry.Output(id, 0)
	t.Fatalf("process did not reach %q: output=%#v err=%v", expected, output, err)
}

func nilContext() context.Context {
	return context.Background()
}
