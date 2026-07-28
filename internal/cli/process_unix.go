//go:build !windows

package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func prepareDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func processIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid process ID")
	}
	if runtime.GOOS == "linux" {
		return linuxProcessIdentity(pid)
	}
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "command=")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "TZ=UTC", "COLUMNS=4096")
	raw, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("inspect process %d: %w", pid, err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", fmt.Errorf("process %d was not found", pid)
	}
	return processIdentityDigest(strconv.Itoa(pid), value), nil
}

func linuxProcessIdentity(pid int) (string, error) {
	prefix := "/proc/" + strconv.Itoa(pid)
	statRaw, err := os.ReadFile(prefix + "/stat")
	if err != nil {
		return "", fmt.Errorf("inspect process %d stat: %w", pid, err)
	}
	closingParen := bytes.LastIndexByte(statRaw, ')')
	if closingParen < 0 || closingParen+1 >= len(statRaw) {
		return "", fmt.Errorf("inspect process %d stat: malformed process record", pid)
	}
	fields := strings.Fields(string(statRaw[closingParen+1:]))
	// The remainder starts at field 3 (state); process start time is field 22.
	if len(fields) <= 19 {
		return "", fmt.Errorf("inspect process %d stat: missing start time", pid)
	}
	executable, err := os.Readlink(prefix + "/exe")
	if err != nil {
		return "", fmt.Errorf("inspect process %d executable: %w", pid, err)
	}
	commandLine, err := os.ReadFile(prefix + "/cmdline")
	if err != nil {
		return "", fmt.Errorf("inspect process %d command line: %w", pid, err)
	}
	return processIdentityDigest(strconv.Itoa(pid), fields[19], executable, string(commandLine)), nil
}

func processIdentityDigest(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

func stopPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}
