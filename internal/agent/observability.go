// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const maxRecentToolCalls = 64

var (
	runtimeTrackerSequence atomic.Uint64
	errToolCallPanicked    = errors.New("tool call panicked")
)

type trackedToolCall struct {
	ID      string
	Tool    string
	Module  string
	Started time.Time
}

type toolCallOutcome struct {
	Err                  error
	PolicyRejected       bool
	AuditErr             error
	ObservationAttempted bool
	ObservationAccepted  bool
	Duration             time.Duration
}

type runtimeCallTracker struct {
	mu        sync.Mutex
	startedAt time.Time
	prefix    string
	nextID    uint64

	started          uint64
	completed        uint64
	succeeded        uint64
	failed           uint64
	policyRejected   uint64
	canceled         uint64
	deadlineExceeded uint64
	inFlight         int64
	maxInFlight      int64
	totalDuration    time.Duration
	maxDuration      time.Duration
	latencyBuckets   [5]uint64

	auditWriteFailures uint64
	lastAuditFailureAt time.Time

	observationAttempted uint64
	observationEnqueued  uint64
	observationDropped   uint64

	tools       map[string]*toolCallCounter
	recent      [maxRecentToolCalls]recentToolCall
	recentCount int
	recentNext  int
}

type toolCallCounter struct {
	module           string
	started          uint64
	completed        uint64
	succeeded        uint64
	failed           uint64
	policyRejected   uint64
	canceled         uint64
	deadlineExceeded uint64
	inFlight         int64
	maxInFlight      int64
	totalDuration    time.Duration
	maxDuration      time.Duration
	lastCallAt       time.Time
	lastFailureAt    time.Time
}

type recentToolCall struct {
	ID                   string
	Tool                 string
	Module               string
	Status               string
	StartedAt            time.Time
	Duration             time.Duration
	AuditWriteFailed     bool
	ObservationAttempted bool
	ObservationAccepted  bool
}

func newRuntimeCallTracker() *runtimeCallTracker {
	now := time.Now().UTC()
	return &runtimeCallTracker{
		startedAt: now,
		prefix:    fmt.Sprintf("%016x-%08x", uint64(now.UnixNano()), runtimeTrackerSequence.Add(1)),
		tools:     map[string]*toolCallCounter{},
	}
}

func (r *Runtime) metricsTracker() *runtimeCallTracker {
	r.metricsOnce.Do(func() {
		r.metrics = newRuntimeCallTracker()
	})
	return r.metrics
}

func (r *Runtime) beginToolCall(name string) trackedToolCall {
	tool := name
	module := r.ToolModuleName(name)
	if _, ok := r.ToolSpec(name); !ok {
		tool = "_unknown"
		module = ""
	}
	tracker := r.metricsTracker()
	now := time.Now().UTC()
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.nextID++
	tracker.started++
	tracker.inFlight++
	if tracker.inFlight > tracker.maxInFlight {
		tracker.maxInFlight = tracker.inFlight
	}
	counter := tracker.toolLocked(tool, module)
	counter.started++
	counter.inFlight++
	counter.lastCallAt = now
	if counter.inFlight > counter.maxInFlight {
		counter.maxInFlight = counter.inFlight
	}
	return trackedToolCall{
		ID:   fmt.Sprintf("call-%s-%016x", tracker.prefix, tracker.nextID),
		Tool: tool, Module: module, Started: now,
	}
}

func (r *Runtime) finishToolCall(call trackedToolCall, outcome toolCallOutcome) {
	tracker := r.metricsTracker()
	finishedAt := time.Now().UTC()
	duration := outcome.Duration
	if duration <= 0 {
		duration = finishedAt.Sub(call.Started)
	}
	status := classifyToolCallStatus(outcome.Err, outcome.PolicyRejected)

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.completed++
	tracker.inFlight--
	tracker.totalDuration += duration
	if duration > tracker.maxDuration {
		tracker.maxDuration = duration
	}
	tracker.latencyBuckets[latencyBucket(duration)]++
	counter := tracker.toolLocked(call.Tool, call.Module)
	counter.completed++
	counter.inFlight--
	counter.totalDuration += duration
	if duration > counter.maxDuration {
		counter.maxDuration = duration
	}
	if outcome.Err == nil {
		tracker.succeeded++
		counter.succeeded++
	} else {
		tracker.failed++
		counter.failed++
		counter.lastFailureAt = finishedAt
		switch status {
		case "policy_rejected":
			tracker.policyRejected++
			counter.policyRejected++
		case "canceled":
			tracker.canceled++
			counter.canceled++
		case "deadline_exceeded":
			tracker.deadlineExceeded++
			counter.deadlineExceeded++
		}
	}
	if outcome.AuditErr != nil {
		tracker.auditWriteFailures++
		tracker.lastAuditFailureAt = finishedAt
	}
	if outcome.ObservationAttempted {
		tracker.observationAttempted++
		if outcome.ObservationAccepted {
			tracker.observationEnqueued++
		} else {
			tracker.observationDropped++
		}
	}
	tracker.recent[tracker.recentNext] = recentToolCall{
		ID: call.ID, Tool: call.Tool, Module: call.Module, Status: status,
		StartedAt: call.Started, Duration: duration, AuditWriteFailed: outcome.AuditErr != nil,
		ObservationAttempted: outcome.ObservationAttempted, ObservationAccepted: outcome.ObservationAccepted,
	}
	tracker.recentNext = (tracker.recentNext + 1) % maxRecentToolCalls
	if tracker.recentCount < maxRecentToolCalls {
		tracker.recentCount++
	}
}

func (t *runtimeCallTracker) toolLocked(name, module string) *toolCallCounter {
	counter := t.tools[name]
	if counter == nil {
		counter = &toolCallCounter{module: module}
		t.tools[name] = counter
	} else if counter.module == "" && module != "" {
		counter.module = module
	}
	return counter
}

func classifyToolCallStatus(err error, policyRejected bool) string {
	if err == nil {
		return "succeeded"
	}
	if policyRejected {
		return "policy_rejected"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return "failed"
}

func latencyBucket(duration time.Duration) int {
	switch {
	case duration < 10*time.Millisecond:
		return 0
	case duration < 100*time.Millisecond:
		return 1
	case duration < time.Second:
		return 2
	case duration < 10*time.Second:
		return 3
	default:
		return 4
	}
}

type runtimeMetricsSnapshot struct {
	startedAt            time.Time
	started              uint64
	completed            uint64
	succeeded            uint64
	failed               uint64
	policyRejected       uint64
	canceled             uint64
	deadlineExceeded     uint64
	inFlight             int64
	maxInFlight          int64
	totalDuration        time.Duration
	maxDuration          time.Duration
	latencyBuckets       [5]uint64
	auditWriteFailures   uint64
	lastAuditFailureAt   time.Time
	observationAttempted uint64
	observationEnqueued  uint64
	observationDropped   uint64
	tools                map[string]toolCallCounter
	recent               []recentToolCall
}

func (t *runtimeCallTracker) snapshot(includeTools bool, recentLimit int) runtimeMetricsSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	snapshot := runtimeMetricsSnapshot{
		startedAt: t.startedAt, started: t.started, completed: t.completed,
		succeeded: t.succeeded, failed: t.failed, policyRejected: t.policyRejected,
		canceled: t.canceled, deadlineExceeded: t.deadlineExceeded,
		inFlight: t.inFlight, maxInFlight: t.maxInFlight,
		totalDuration: t.totalDuration, maxDuration: t.maxDuration,
		latencyBuckets:     t.latencyBuckets,
		auditWriteFailures: t.auditWriteFailures, lastAuditFailureAt: t.lastAuditFailureAt,
		observationAttempted: t.observationAttempted,
		observationEnqueued:  t.observationEnqueued, observationDropped: t.observationDropped,
	}
	if includeTools {
		snapshot.tools = make(map[string]toolCallCounter, len(t.tools))
		for name, counter := range t.tools {
			snapshot.tools[name] = *counter
		}
	}
	if recentLimit > 0 {
		recentLimit = min(recentLimit, maxRecentToolCalls, t.recentCount)
		snapshot.recent = make([]recentToolCall, recentLimit)
		start := (t.recentNext - recentLimit + maxRecentToolCalls) % maxRecentToolCalls
		for index := range recentLimit {
			snapshot.recent[index] = t.recent[(start+index)%maxRecentToolCalls]
		}
	}
	return snapshot
}

// RuntimeMetrics returns bounded, argument-free diagnostics for this workspace
// runtime. It never includes session IDs, tool arguments, results, or error text.
func (r *Runtime) RuntimeMetrics(includeTools bool, recentLimit int) map[string]any {
	now := time.Now().UTC()
	repositoryCache := r.repositoryCacheStats()
	snapshot := r.metricsTracker().snapshot(includeTools, recentLimit)
	auditMetrics := map[string]any{
		"enabled": r.Config.Audit, "write_failures": snapshot.auditWriteFailures,
	}
	if r.AuditWriter != nil {
		writerStats := r.AuditWriter.Stats()
		for _, key := range []string{"queue_depth", "queue_capacity", "enqueued", "backpressure_waits", "fallback_writes", "batches", "rotations", "write_failures", "max_bytes", "max_files"} {
			if value, ok := writerStats[key]; ok {
				auditMetrics["writer_"+key] = value
			}
		}
	}
	result := map[string]any{
		"started_at":     snapshot.startedAt,
		"uptime_seconds": max(int64(0), int64(now.Sub(snapshot.startedAt)/time.Second)),
		"started_calls":  snapshot.started, "completed_calls": snapshot.completed,
		"succeeded": snapshot.succeeded, "failed": snapshot.failed,
		"policy_rejected": snapshot.policyRejected, "canceled": snapshot.canceled,
		"deadline_exceeded": snapshot.deadlineExceeded,
		"in_flight":         snapshot.inFlight, "max_in_flight": snapshot.maxInFlight,
		"latency_us": durationSummary(snapshot.totalDuration, snapshot.maxDuration, snapshot.completed),
		"latency_buckets": map[string]uint64{
			"lt_10ms": snapshot.latencyBuckets[0], "lt_100ms": snapshot.latencyBuckets[1],
			"lt_1s": snapshot.latencyBuckets[2], "lt_10s": snapshot.latencyBuckets[3],
			"gte_10s": snapshot.latencyBuckets[4],
		},
		"audit": auditMetrics,
		"memory_observations": map[string]uint64{
			"attempted": snapshot.observationAttempted, "enqueued": snapshot.observationEnqueued,
			"dropped": snapshot.observationDropped,
		},
		"repository_cache": repositoryCache,
	}
	if !snapshot.lastAuditFailureAt.IsZero() {
		result["audit"].(map[string]any)["last_failure_at"] = snapshot.lastAuditFailureAt
	}
	if includeTools {
		names := make([]string, 0, len(snapshot.tools))
		for name := range snapshot.tools {
			names = append(names, name)
		}
		sort.Strings(names)
		tools := make([]map[string]any, 0, len(names))
		for _, name := range names {
			counter := snapshot.tools[name]
			entry := map[string]any{
				"tool": name, "module": counter.module,
				"started_calls": counter.started, "completed_calls": counter.completed,
				"succeeded": counter.succeeded, "failed": counter.failed,
				"policy_rejected": counter.policyRejected, "canceled": counter.canceled,
				"deadline_exceeded": counter.deadlineExceeded,
				"in_flight":         counter.inFlight, "max_in_flight": counter.maxInFlight,
				"latency_us": durationSummary(counter.totalDuration, counter.maxDuration, counter.completed),
			}
			if !counter.lastCallAt.IsZero() {
				entry["last_call_at"] = counter.lastCallAt
			}
			if !counter.lastFailureAt.IsZero() {
				entry["last_failure_at"] = counter.lastFailureAt
			}
			tools = append(tools, entry)
		}
		result["tools"] = tools
	}
	if len(snapshot.recent) > 0 {
		recent := make([]map[string]any, 0, len(snapshot.recent))
		for _, call := range snapshot.recent {
			entry := map[string]any{
				"call_id": call.ID, "tool": call.Tool, "module": call.Module,
				"status": call.Status, "started_at": call.StartedAt,
				"duration_us":        max(int64(0), call.Duration.Microseconds()),
				"audit_write_failed": call.AuditWriteFailed,
			}
			if call.ObservationAttempted {
				entry["memory_observation"] = ternary(call.ObservationAccepted, "enqueued", "dropped")
			}
			recent = append(recent, entry)
		}
		result["recent_calls"] = recent
	}
	return result
}

func durationSummary(total, maximum time.Duration, completed uint64) map[string]int64 {
	average := int64(0)
	if completed > 0 {
		average = total.Microseconds() / int64(completed)
	}
	return map[string]int64{
		"total":   max(int64(0), total.Microseconds()),
		"average": max(int64(0), average),
		"max":     max(int64(0), maximum.Microseconds()),
	}
}
