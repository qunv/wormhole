// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package processx

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	processRunning  = "running"
	processStopping = "stopping"
	processStopped  = "stopped"
	processExited   = "exited"
)

type Result struct {
	ExitCode        int    `json:"exit_code"`
	TimedOut        bool   `json:"timed_out"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

func Run(ctx context.Context, command, cwd, shell string, timeout time.Duration, maxOutput int) Result {
	name, args := ShellCommand(command, shell)
	return execute(ctx, name, args, cwd, timeout, maxOutput)
}

func Capture(ctx context.Context, name string, args []string, cwd string, timeout time.Duration) Result {
	return execute(ctx, name, args, cwd, timeout, 2_000_000)
}

func execute(ctx context.Context, name string, args []string, cwd string, timeout time.Duration, maxOutput int) Result {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return Result{ExitCode: -1, TimedOut: errors.Is(err, context.DeadlineExceeded), Stderr: err.Error()}
	}

	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	prepareBackground(cmd)
	stdout := &lockedBuffer{limit: maxOutput, keepHead: true}
	stderr := &lockedBuffer{limit: maxOutput, keepHead: true}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return Result{ExitCode: -1, Stderr: err.Error()}
	}

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-wait:
	case <-runCtx.Done():
		_ = killBackground(cmd)
		select {
		case runErr = <-wait:
		case <-time.After(500 * time.Millisecond):
			_ = forceKillBackground(cmd)
			runErr = <-wait
		}
	}

	result := Result{
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.ExitCode = -1
		result.TimedOut = true
		return result
	}
	if runErr == nil {
		return result
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		result.ExitCode = exit.ExitCode()
	} else {
		result.ExitCode = -1
		if result.Stderr == "" {
			result.Stderr = runErr.Error()
		}
	}
	return result
}

func ShellCommand(command, shell string) (string, []string) {
	if shell == "" {
		if runtime.GOOS == "windows" {
			shell = "cmd"
		} else {
			shell = "sh"
		}
	}
	switch shell {
	case "cmd":
		return "cmd.exe", []string{"/d", "/s", "/c", command}
	case "powershell":
		return "powershell.exe", []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command}
	case "bash", "zsh", "sh":
		return shell, []string{"-lc", command}
	default:
		return shell, []string{"-lc", command}
	}
}

func Trim(value string, headLines, tailLines, maxChars int) string {
	if headLines > 0 || tailLines > 0 {
		lines := strings.Split(value, "\n")
		switch {
		case headLines > 0 && len(lines) > headLines:
			lines = lines[:headLines]
		case tailLines > 0 && len(lines) > tailLines:
			lines = lines[len(lines)-tailLines:]
		}
		value = strings.Join(lines, "\n")
	}
	if maxChars > 0 && len(value) > maxChars {
		return value[:maxChars]
	}
	return value
}

type Process struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Command   string `json:"command"`
	CWD       string `json:"cwd"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`

	mu       sync.RWMutex
	status   string
	exitCode *int
	cmd      *exec.Cmd
	stdout   *lockedBuffer
	stderr   *lockedBuffer
}

func (p *Process) state() (string, *int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var exitCode *int
	if p.exitCode != nil {
		value := *p.exitCode
		exitCode = &value
	}
	return p.status, exitCode
}

func (p *Process) beginStop() (*exec.Cmd, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status != processRunning {
		return nil, false
	}
	p.status = processStopping
	return p.cmd, true
}

func (p *Process) stopFailed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status == processStopping {
		p.status = processRunning
	}
}

func (p *Process) finish(exitCode int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status == processStopping || p.status == processStopped {
		p.status = processStopped
	} else {
		p.status = processExited
	}
	p.exitCode = &exitCode
}

type Registry struct {
	Max int

	mu        sync.RWMutex
	items     map[string]*Process
	order     []string
	starting  int
	running   int
	retention int
	closed    bool
}

func NewRegistry(maxProcesses int) *Registry {
	if maxProcesses <= 0 {
		maxProcesses = 24
	}
	return &Registry{
		Max:       maxProcesses,
		items:     map[string]*Process{},
		retention: max(64, maxProcesses*4),
	}
}

func (r *Registry) Start(command, cwd, shell, displayName string) (*Process, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("process registry is closed")
	}
	if r.starting+r.running >= r.Max {
		r.mu.Unlock()
		return nil, fmt.Errorf("too many running processes (max %d)", r.Max)
	}
	r.starting++
	r.mu.Unlock()

	name, args := ShellCommand(command, shell)
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	prepareBackground(cmd)
	stdout := &lockedBuffer{limit: 200_000}
	stderr := &lockedBuffer{limit: 200_000}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		r.mu.Lock()
		r.starting--
		r.mu.Unlock()
		return nil, err
	}

	id := fmt.Sprintf("proc-%d-%d", time.Now().UnixMilli(), cmd.Process.Pid)
	if displayName == "" {
		displayName = id
	}
	proc := &Process{
		ID: id, Name: displayName, Command: command, CWD: cwd, PID: cmd.Process.Pid,
		StartedAt: time.Now().UTC().Format(time.RFC3339), status: processRunning, cmd: cmd,
		stdout: stdout, stderr: stderr,
	}

	r.mu.Lock()
	r.starting--
	if r.closed {
		r.mu.Unlock()
		_ = forceKillBackground(cmd)
		_ = cmd.Wait()
		return nil, errors.New("process registry is closed")
	}
	r.items[id] = proc
	r.order = append(r.order, id)
	r.running++
	r.pruneLocked()
	r.mu.Unlock()

	go r.wait(proc)
	return proc, nil
}

func (r *Registry) wait(proc *Process) {
	err := proc.cmd.Wait()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			code = -1
		}
	}
	proc.finish(code)

	r.mu.Lock()
	if r.running > 0 {
		r.running--
	}
	r.pruneLocked()
	r.mu.Unlock()
}

func (r *Registry) List() []map[string]any {
	r.mu.RLock()
	processes := make([]*Process, 0, len(r.order))
	for _, id := range r.order {
		if proc := r.items[id]; proc != nil {
			processes = append(processes, proc)
		}
	}
	r.mu.RUnlock()

	out := make([]map[string]any, 0, len(processes))
	for _, proc := range processes {
		status, exitCode := proc.state()
		out = append(out, map[string]any{
			"id": proc.ID, "name": proc.Name, "command": proc.Command, "cwd": proc.CWD,
			"pid": proc.PID, "status": status, "exit_code": exitCode, "started_at": proc.StartedAt,
		})
	}
	return out
}

func (r *Registry) Output(id string, tail int) (map[string]any, error) {
	r.mu.RLock()
	proc := r.items[id]
	r.mu.RUnlock()
	if proc == nil {
		return nil, fmt.Errorf("no process with id %s", id)
	}
	stdout, stderr := proc.stdout.TailString(tail), proc.stderr.TailString(tail)
	status, exitCode := proc.state()
	return map[string]any{
		"id": id, "status": status, "exit_code": exitCode,
		"stdout": stdout, "stderr": stderr,
	}, nil
}

func (r *Registry) Stop(id string) error {
	r.mu.RLock()
	proc := r.items[id]
	r.mu.RUnlock()
	if proc == nil {
		return fmt.Errorf("no process with id %s", id)
	}
	cmd, shouldStop := proc.beginStop()
	if !shouldStop {
		return nil
	}
	if err := killBackground(cmd); err != nil {
		proc.stopFailed()
		return err
	}
	return nil
}

func (r *Registry) StopAll() {
	r.mu.Lock()
	r.closed = true
	ids := append([]string(nil), r.order...)
	r.mu.Unlock()
	for _, id := range ids {
		_ = r.Stop(id)
	}
}

func (r *Registry) pruneLocked() {
	if len(r.items) <= r.retention {
		return
	}
	kept := r.order[:0]
	for _, id := range r.order {
		proc := r.items[id]
		if proc == nil {
			continue
		}
		status, _ := proc.state()
		if len(r.items) > r.retention && status != processRunning && status != processStopping {
			delete(r.items, id)
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept
}

type lockedBuffer struct {
	mu        sync.Mutex
	data      []byte
	start     int
	limit     int
	keepHead  bool
	truncated bool
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(data)
	if b.limit <= 0 {
		b.data = append(b.data, data...)
		return written, nil
	}
	if b.keepHead {
		remaining := max(0, b.limit-len(b.data))
		if len(data) > remaining {
			b.truncated = true
		}
		b.data = append(b.data, data[:min(remaining, len(data))]...)
		return written, nil
	}
	if len(data) >= b.limit {
		b.truncated = b.truncated || len(b.data) > 0 || len(data) > b.limit
		if cap(b.data) < b.limit {
			b.data = make([]byte, b.limit)
		} else {
			b.data = b.data[:b.limit]
		}
		copy(b.data, data[len(data)-b.limit:])
		b.start = 0
		return written, nil
	}
	if len(b.data) < b.limit {
		fill := min(b.limit-len(b.data), len(data))
		b.data = append(b.data, data[:fill]...)
		data = data[fill:]
		if len(data) == 0 {
			return written, nil
		}
	}
	b.truncated = true
	for len(data) > 0 {
		chunk := min(len(data), b.limit-b.start)
		copy(b.data[b.start:b.start+chunk], data[:chunk])
		b.start = (b.start + chunk) % b.limit
		data = data[chunk:]
	}
	return written, nil
}

func (b *lockedBuffer) String() string { return b.TailString(0) }

func (b *lockedBuffer) TailString(tail int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	size := len(b.data)
	if size == 0 {
		return ""
	}
	if tail <= 0 || tail > size {
		tail = size
	}
	logicalStart := size - tail
	physicalStart := logicalStart
	if b.start != 0 && size == b.limit && !b.keepHead {
		physicalStart = (b.start + logicalStart) % size
	}
	if physicalStart+tail <= size {
		return string(b.data[physicalStart : physicalStart+tail])
	}
	first := size - physicalStart
	var ordered strings.Builder
	ordered.Grow(tail)
	_, _ = ordered.Write(b.data[physicalStart:])
	_, _ = ordered.Write(b.data[:tail-first])
	return ordered.String()
}

func (b *lockedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
