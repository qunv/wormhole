// Codebridge
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

type Result struct {
	ExitCode       int    `json:"exit_code"`
	TimedOut       bool   `json:"timed_out"`
	Stdout         string `json:"stdout"`
	Stderr         string `json:"stderr"`
	StdoutTruncated bool  `json:"stdout_truncated"`
	StderrTruncated bool  `json:"stderr_truncated"`
}

func (b *lockedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

func Run(ctx context.Context, command, cwd, shell string, timeout time.Duration, maxOutput int) Result {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	name, args := ShellCommand(command, shell)
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Dir = cwd
	stdout, stderr := &lockedBuffer{limit: maxOutput, keepHead: true}, &lockedBuffer{limit: maxOutput, keepHead: true}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), StdoutTruncated: stdout.Truncated(), StderrTruncated: stderr.Truncated()}
	if runCtx.Err() == context.DeadlineExceeded {
		result.ExitCode = -1
		result.TimedOut = true
		return result
	}
	if err == nil {
		return result
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitCode()
	} else {
		result.ExitCode = -1
		if result.Stderr == "" {
			result.Stderr = err.Error()
		}
	}
	return result
}

func Capture(ctx context.Context, name string, args []string, cwd string, timeout time.Duration) Result {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Dir = cwd
	stdout, stderr := &lockedBuffer{limit: 2_000_000, keepHead: true}, &lockedBuffer{limit: 2_000_000, keepHead: true}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), StdoutTruncated: stdout.Truncated(), StderrTruncated: stderr.Truncated()}
	if runCtx.Err() == context.DeadlineExceeded {
		result.ExitCode, result.TimedOut = -1, true
		return result
	}
	if err == nil {
		return result
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitCode()
	} else {
		result.ExitCode = -1
		if result.Stderr == "" {
			result.Stderr = err.Error()
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
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code"`
	StartedAt string `json:"started_at"`

	cmd    *exec.Cmd
	stdout *lockedBuffer
	stderr *lockedBuffer
}

type Registry struct {
	Max   int
	mu    sync.RWMutex
	items map[string]*Process
}

func NewRegistry(max int) *Registry {
	if max <= 0 {
		max = 24
	}
	return &Registry{Max: max, items: map[string]*Process{}}
}

func (r *Registry) Start(command, cwd, shell, displayName string) (*Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	running := 0
	for _, proc := range r.items {
		if proc.Status == "running" {
			running++
		}
	}
	if running >= r.Max {
		return nil, fmt.Errorf("too many running processes (max %d)", r.Max)
	}
	name, args := ShellCommand(command, shell)
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	prepareBackground(cmd)
	stdout, stderr := &lockedBuffer{limit: 200_000}, &lockedBuffer{limit: 200_000}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	id := fmt.Sprintf("proc-%d-%d", time.Now().UnixMilli(), cmd.Process.Pid)
	if displayName == "" {
		displayName = id
	}
	proc := &Process{
		ID: id, Name: displayName, Command: command, CWD: cwd, PID: cmd.Process.Pid,
		Status: "running", StartedAt: time.Now().UTC().Format(time.RFC3339), cmd: cmd,
		stdout: stdout, stderr: stderr,
	}
	r.items[id] = proc
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else {
				code = -1
			}
		}
		r.mu.Lock()
		proc.Status, proc.ExitCode = "exited", &code
		r.mu.Unlock()
	}()
	return proc, nil
}

func (r *Registry) List() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]map[string]any, 0, len(r.items))
	for _, proc := range r.items {
		out = append(out, map[string]any{
			"id": proc.ID, "name": proc.Name, "command": proc.Command, "cwd": proc.CWD,
			"pid": proc.PID, "status": proc.Status, "exit_code": proc.ExitCode, "started_at": proc.StartedAt,
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
	stdout, stderr := proc.stdout.String(), proc.stderr.String()
	if tail > 0 {
		if len(stdout) > tail {
			stdout = stdout[len(stdout)-tail:]
		}
		if len(stderr) > tail {
			stderr = stderr[len(stderr)-tail:]
		}
	}
	return map[string]any{"id": id, "status": proc.Status, "exit_code": proc.ExitCode, "stdout": stdout, "stderr": stderr}, nil
}

func (r *Registry) Stop(id string) error {
	r.mu.RLock()
	proc := r.items[id]
	r.mu.RUnlock()
	if proc == nil {
		return fmt.Errorf("no process with id %s", id)
	}
	if proc.Status != "running" {
		return nil
	}
	if err := killBackground(proc.cmd); err != nil {
		return err
	}
	r.mu.Lock()
	proc.Status = "stopped"
	r.mu.Unlock()
	return nil
}

func (r *Registry) StopAll() {
	r.mu.RLock()
	ids := make([]string, 0, len(r.items))
	for id := range r.items {
		ids = append(ids, id)
	}
	r.mu.RUnlock()
	for _, id := range ids {
		_ = r.Stop(id)
	}
}

type lockedBuffer struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	keepHead  bool
	truncated bool
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit > 0 && len(b.data)+len(data) > b.limit {
		b.truncated = true
		if b.keepHead {
			remaining := max(0, b.limit-len(b.data))
			b.data = append(b.data, data[:min(remaining, len(data))]...)
			return len(data), nil
		}
	}
	b.data = append(b.data, data...)
	if b.limit > 0 && len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(data), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.data...))
}
