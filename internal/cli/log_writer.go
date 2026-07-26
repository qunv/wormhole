// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codebridge/internal/config"
)

const (
	childLogMaxBytes = int64(32 << 20)
	childLogMaxFiles = 4
)

type rotatingLogWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	file     *os.File
	size     int64
}

func newRotatingLogWriter(path string, maxBytes int64, maxFiles int) (*rotatingLogWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	writer := &rotatingLogWriter{
		path: path, maxBytes: maxBytes, maxFiles: maxFiles,
		file: file, size: info.Size(),
	}
	if maxBytes > 0 && writer.size >= maxBytes {
		if err := writer.rotateLocked(); err != nil {
			_ = writer.Close()
			return nil, err
		}
	}
	return writer, nil
}

func (w *rotatingLogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}

	written := 0
	for len(data) > 0 {
		if w.maxBytes > 0 && w.size >= w.maxBytes {
			if err := w.rotateLocked(); err != nil {
				return written, err
			}
		}
		chunk := len(data)
		if w.maxBytes > 0 {
			remaining := w.maxBytes - w.size
			if remaining < int64(chunk) {
				chunk = int(remaining)
			}
		}
		if chunk <= 0 {
			continue
		}
		n, err := w.file.Write(data[:chunk])
		written += n
		w.size += int64(n)
		data = data[n:]
		if err != nil {
			return written, err
		}
		if n != chunk {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func (w *rotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *rotatingLogWriter) rotateLocked() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}

	if w.maxFiles <= 0 {
		return w.openTruncatedLocked()
	}
	if err := os.Remove(w.path + fmt.Sprintf(".%d", w.maxFiles)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return w.openTruncatedLocked()
	}
	for index := w.maxFiles - 1; index >= 1; index-- {
		from := w.path + fmt.Sprintf(".%d", index)
		to := w.path + fmt.Sprintf(".%d", index+1)
		if err := os.Rename(from, to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return w.openTruncatedLocked()
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return w.openTruncatedLocked()
	}
	return w.openAppendLocked()
}

func (w *rotatingLogWriter) openAppendLocked() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w.file = file
	w.size = 0
	return nil
}

func (w *rotatingLogWriter) openTruncatedLocked() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_TRUNC|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w.file = file
	w.size = 0
	return nil
}

func readFileTail(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(file, maxBytes))
}

func childLogPath(label string) string {
	switch label {
	case "server":
		return config.ServerLogPath()
	case "tunnel":
		return config.TunnelLogPath()
	default:
		return config.LogPath()
	}
}

func commandText(command string, args []string) string {
	return strings.Join(append([]string{command}, args...), " ")
}

// runLoggedChild owns the child output pipes for the lifetime of a detached
// process. Rotation therefore closes and reopens only Codebridge-owned files;
// the server or tunnel never holds a stale descriptor to a renamed log.
func (a App) runLoggedChild(ctx context.Context, args []string) error {
	if len(args) < 4 {
		return errors.New("invalid internal child invocation")
	}
	label, logPath, cwd, command := args[0], args[1], args[2], args[3]
	if label != "server" && label != "tunnel" {
		return fmt.Errorf("invalid internal child label %q", label)
	}
	if filepath.Clean(logPath) != filepath.Clean(childLogPath(label)) {
		return errors.New("internal child log path does not match configured state directory")
	}
	commandArgs := args[4:]
	writer, err := newRotatingLogWriter(logPath, childLogMaxBytes, childLogMaxFiles)
	if err != nil {
		return fmt.Errorf("open %s log: %w", label, err)
	}
	defer writer.Close()
	_, _ = fmt.Fprintf(writer, "[%s] [%s] %s\n", time.Now().UTC().Format(time.RFC3339), label, commandText(command, commandArgs))

	child := exec.Command(command, commandArgs...)
	child.Dir = cwd
	child.Env = os.Environ()
	child.Stdin = nil
	child.Stdout, child.Stderr = writer, writer
	if err := child.Start(); err != nil {
		_, _ = fmt.Fprintf(writer, "[%s] [%s] start failed: %v\n", time.Now().UTC().Format(time.RFC3339), label, err)
		return err
	}

	exit := make(chan error, 1)
	go func() { exit <- child.Wait() }()
	select {
	case err := <-exit:
		return err
	case <-ctx.Done():
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-exit:
			return nil
		case <-timer.C:
			_ = child.Process.Kill()
			<-exit
			return nil
		}
	}
}
