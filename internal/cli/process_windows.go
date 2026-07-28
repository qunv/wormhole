//go:build windows

package cli

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func prepareDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000}
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	raw, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	records, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil {
		return false
	}
	want := strconv.Itoa(pid)
	for _, record := range records {
		if len(record) >= 2 && strings.TrimSpace(record[1]) == want {
			return true
		}
	}
	return false
}

func processIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid process ID")
	}
	script := fmt.Sprintf("$p=Get-CimInstance Win32_Process -Filter 'ProcessId = %d'; if ($null -eq $p) { exit 3 }; Write-Output ($p.CreationDate + '|' + $p.ExecutablePath + '|' + $p.CommandLine)", pid)
	raw, err := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return "", fmt.Errorf("inspect process %d: %w", pid, err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", fmt.Errorf("process %d was not found", pid)
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16]), nil
}

func stopPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}
