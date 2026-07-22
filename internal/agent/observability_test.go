package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRuntimeMetricsTrackOutcomesAndAuditCorrelation(t *testing.T) {
	runtime := newIsolatedRuntime(t, "metrics", t.TempDir(), t.TempDir(), "full")
	if _, err := runtime.HandleSession(context.Background(), "session-secret", "ping", map[string]any{"message": "ok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.HandleSession(context.Background(), "session-secret", "missing_tool", nil); err == nil {
		t.Fatal("unknown tool unexpectedly succeeded")
	}
	runtime.Config.Policy = "strict"
	if _, err := runtime.HandleSession(context.Background(), "session-secret", "write_file", map[string]any{"path": "blocked.txt", "content": "blocked"}); err == nil {
		t.Fatal("strict policy unexpectedly allowed write_file")
	}

	metrics := runtime.RuntimeMetrics(true, 10)
	if metrics["started_calls"] != uint64(3) || metrics["completed_calls"] != uint64(3) {
		t.Fatalf("unexpected call counts: %#v", metrics)
	}
	if metrics["succeeded"] != uint64(1) || metrics["failed"] != uint64(2) || metrics["policy_rejected"] != uint64(1) {
		t.Fatalf("unexpected outcomes: %#v", metrics)
	}
	if metrics["in_flight"] != int64(0) || metrics["max_in_flight"].(int64) < 1 {
		t.Fatalf("unexpected in-flight metrics: %#v", metrics)
	}
	recent := metrics["recent_calls"].([]map[string]any)
	if len(recent) != 3 {
		t.Fatalf("recent calls = %d, want 3", len(recent))
	}
	seenCalls := map[string]bool{}
	for _, entry := range recent {
		callID, _ := entry["call_id"].(string)
		if callID == "" || seenCalls[callID] {
			t.Fatalf("invalid or duplicate call ID: %#v", entry)
		}
		seenCalls[callID] = true
		raw, _ := json.Marshal(entry)
		text := string(raw)
		for _, forbidden := range []string{"session-secret", "tool_input", "error"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("recent metrics leaked %q: %s", forbidden, text)
			}
		}
	}

	if err := runtime.FlushAudit(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(runtime.Store.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("audit lines = %d, want 3", len(lines))
	}
	statuses := map[string]bool{}
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		callID, _ := record["call_id"].(string)
		if !seenCalls[callID] {
			t.Fatalf("audit call ID %q not present in recent metrics", callID)
		}
		if _, ok := record["duration_us"].(float64); !ok {
			t.Fatalf("audit record missing duration: %#v", record)
		}
		statuses[record["status"].(string)] = true
	}
	for _, status := range []string{"succeeded", "failed", "policy_rejected"} {
		if !statuses[status] {
			t.Fatalf("audit statuses missing %q: %#v", status, statuses)
		}
	}
}

func TestRuntimeMetricsCountAuditFailuresWithoutFailingTool(t *testing.T) {
	runtime := newIsolatedRuntime(t, "audit-failure", t.TempDir(), t.TempDir(), "full")
	directory := filepath.Join(t.TempDir(), "audit-directory")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime.Store.AuditPath = directory
	if _, err := runtime.Handle(context.Background(), "ping", nil); err != nil {
		t.Fatalf("audit failure changed tool result: %v", err)
	}
	metrics := runtime.RuntimeMetrics(false, 1)
	audit := metrics["audit"].(map[string]any)
	if audit["write_failures"] != uint64(1) || audit["last_failure_at"] == nil {
		t.Fatalf("audit failure was not observable: %#v", audit)
	}
	recent := metrics["recent_calls"].([]map[string]any)
	if len(recent) != 1 || recent[0]["audit_write_failed"] != true {
		t.Fatalf("recent call omitted audit failure: %#v", recent)
	}
	doctor, err := runtime.workspaceDoctor(context.Background(), map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	foundAuditWarning := false
	for _, check := range doctor.(map[string]any)["checks"].([]map[string]any) {
		if check["id"] == "audit" && check["status"] == "warn" {
			foundAuditWarning = true
		}
	}
	if !foundAuditWarning {
		t.Fatalf("workspace doctor omitted audit warning: %#v", doctor)
	}
}

func TestRuntimeMetricsToolReturnsBoundedDiagnostics(t *testing.T) {
	runtime := newIsolatedRuntime(t, "metrics-tool", t.TempDir(), t.TempDir(), "full")
	value, err := runtime.Handle(context.Background(), "runtime_metrics", map[string]any{
		"include_tools": false, "recent_limit": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	metrics := value.(map[string]any)
	if metrics["started_calls"] != uint64(1) || metrics["in_flight"] != int64(1) {
		t.Fatalf("metrics tool did not include its active call: %#v", metrics)
	}
	if metrics["tools"] != nil || metrics["recent_calls"] != nil {
		t.Fatalf("bounded options were ignored: %#v", metrics)
	}
	completed := runtime.RuntimeMetrics(false, 0)
	if completed["completed_calls"] != uint64(1) || completed["succeeded"] != uint64(1) {
		t.Fatalf("metrics call completion was not recorded: %#v", completed)
	}
}

type panicMetricsModule struct{}

func (*panicMetricsModule) Name() string { return "panic_probe" }
func (*panicMetricsModule) Specs() []ToolSpec {
	return []ToolSpec{{
		Name: "metrics_panic", Title: "Metrics panic", Description: "Panic for lifecycle testing.",
		ReadOnly: true, Schema: object(nil),
	}}
}
func (*panicMetricsModule) Handle(context.Context, CallIdentity, string, map[string]any) (any, error) {
	values := []int{}
	return values[1], nil
}
func (*panicMetricsModule) Health(context.Context) any { return map[string]any{"available": true} }
func (*panicMetricsModule) Close() error               { return nil }

func TestRuntimeMetricsFinalizePanickingCalls(t *testing.T) {
	runtime := newIsolatedRuntime(t, "panic-metrics", t.TempDir(), t.TempDir(), "full")
	if err := runtime.RegisterModule(&panicMetricsModule{}); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("expected tool panic")
			}
		}()
		_, _ = runtime.Handle(context.Background(), "metrics_panic", nil)
	}()
	metrics := runtime.RuntimeMetrics(true, 1)
	if metrics["completed_calls"] != uint64(1) || metrics["failed"] != uint64(1) || metrics["in_flight"] != int64(0) {
		t.Fatalf("panicking call was not finalized: %#v", metrics)
	}
	recent := metrics["recent_calls"].([]map[string]any)
	if len(recent) != 1 || recent[0]["status"] != "failed" {
		t.Fatalf("panic outcome missing from recent calls: %#v", recent)
	}
	if err := runtime.FlushAudit(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(runtime.Store.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"error":"tool call panicked"`) {
		t.Fatalf("panic audit was not sanitized: %s", raw)
	}
}

func TestRuntimeCallIDsAreUniqueAcrossRuntimes(t *testing.T) {
	first := newIsolatedRuntime(t, "first-metrics", t.TempDir(), t.TempDir(), "full")
	second := newIsolatedRuntime(t, "second-metrics", t.TempDir(), t.TempDir(), "full")
	firstCall := first.beginToolCall("ping")
	secondCall := second.beginToolCall("ping")
	first.finishToolCall(firstCall, toolCallOutcome{})
	second.finishToolCall(secondCall, toolCallOutcome{})
	if firstCall.ID == secondCall.ID {
		t.Fatalf("cross-runtime call IDs collided: %q", firstCall.ID)
	}
}

type blockingMetricsModule struct {
	started chan struct{}
	release chan struct{}
}

func (*blockingMetricsModule) Name() string { return "metrics_probe" }
func (*blockingMetricsModule) Specs() []ToolSpec {
	return []ToolSpec{{
		Name: "metrics_block", Title: "Metrics block", Description: "Block for metrics testing.",
		ReadOnly: true, Schema: object(nil),
	}}
}
func (m *blockingMetricsModule) Handle(ctx context.Context, _ CallIdentity, _ string, _ map[string]any) (any, error) {
	m.started <- struct{}{}
	select {
	case <-m.release:
		return map[string]any{"ok": true}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (*blockingMetricsModule) Health(context.Context) any { return map[string]any{"available": true} }
func (*blockingMetricsModule) Close() error               { return nil }

func TestRuntimeMetricsTrackConcurrentInFlightCalls(t *testing.T) {
	runtime := newIsolatedRuntime(t, "concurrent-metrics", t.TempDir(), t.TempDir(), "full")
	module := &blockingMetricsModule{started: make(chan struct{}, 8), release: make(chan struct{})}
	if err := runtime.RegisterModule(module); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runtime.Handle(context.Background(), "metrics_block", nil)
			errs <- err
		}()
	}
	for index := 0; index < 8; index++ {
		select {
		case <-module.started:
		case <-time.After(time.Second):
			t.Fatalf("only %d calls reached the module", index)
		}
	}
	metrics := runtime.RuntimeMetrics(true, 0)
	if metrics["in_flight"] != int64(8) || metrics["max_in_flight"].(int64) < 8 {
		t.Fatalf("concurrent calls not reflected: %#v", metrics)
	}
	close(module.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	metrics = runtime.RuntimeMetrics(true, 0)
	if metrics["completed_calls"] != uint64(8) || metrics["succeeded"] != uint64(8) || metrics["in_flight"] != int64(0) {
		t.Fatalf("unexpected completed metrics: %#v", metrics)
	}
}

func TestRuntimeMetricsClassifyCancellationAndBoundRecentCalls(t *testing.T) {
	runtime := newIsolatedRuntime(t, "bounded-metrics", t.TempDir(), t.TempDir(), "full")
	call := runtime.beginToolCall("ping")
	runtime.finishToolCall(call, toolCallOutcome{Err: context.Canceled})
	call = runtime.beginToolCall("ping")
	runtime.finishToolCall(call, toolCallOutcome{Err: context.DeadlineExceeded})
	for index := 0; index < 100; index++ {
		call = runtime.beginToolCall("ping")
		runtime.finishToolCall(call, toolCallOutcome{})
	}
	metrics := runtime.RuntimeMetrics(false, 100)
	if metrics["canceled"] != uint64(1) || metrics["deadline_exceeded"] != uint64(1) {
		t.Fatalf("cancellation classification failed: %#v", metrics)
	}
	if got := len(metrics["recent_calls"].([]map[string]any)); got != maxRecentToolCalls {
		t.Fatalf("recent calls = %d, want %d", got, maxRecentToolCalls)
	}
}
