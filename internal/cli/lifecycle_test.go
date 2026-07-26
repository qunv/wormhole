package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"codebridge/internal/config"
)

func TestLifecycleCommandBackgroundDefaults(t *testing.T) {
	tests := map[string]bool{
		"": true, "run": true, "here": true, "restart": true,
		"start": false, "serve": false, "stop": false,
	}
	for command, want := range tests {
		t.Run(command, func(t *testing.T) {
			if got := runsInBackgroundByDefault(command); got != want {
				t.Fatalf("runsInBackgroundByDefault(%q) = %v, want %v", command, got, want)
			}
		})
	}
}

func TestRotatingLogWriterRetainsBoundedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.log")
	writer, err := newRotatingLogWriter(path, 32, 2)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 12; index++ {
		if _, err := writer.Write([]byte(strings.Repeat(string(rune('a'+index)), 11))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writer.Write([]byte("latest")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + ".1", path + ".2"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("missing retained log %s: %v", candidate, err)
		}
		if info.Size() > 32 {
			t.Fatalf("log %s grew to %d bytes", candidate, info.Size())
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("rotation exceeded retention: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "latest") {
		t.Fatalf("active log lost newest output: %q", raw)
	}
}

func TestReadFileTailBoundsLargeLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.log")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := readFileTail(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "6789" {
		t.Fatalf("tail = %q", raw)
	}
}

func TestLoggedChildRejectsUnexpectedLogPath(t *testing.T) {
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	err := (App{}).runLoggedChild(context.Background(), []string{"server", filepath.Join(t.TempDir(), "other.log"), "", os.Args[0]})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("unexpected validation result: %v", err)
	}
}

func TestChildLogPathSeparatesServerAndTunnel(t *testing.T) {
	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	if childLogPath("server") != config.ServerLogPath() {
		t.Fatalf("server log path = %s", childLogPath("server"))
	}
	if childLogPath("tunnel") != config.TunnelLogPath() {
		t.Fatalf("tunnel log path = %s", childLogPath("tunnel"))
	}
	if childLogPath("test") != config.LogPath() {
		t.Fatalf("fallback log path = %s", childLogPath("test"))
	}
}

func TestForegroundChildKeepsLogWriterOpen(t *testing.T) {
	if os.Getenv("CODEBRIDGE_TEST_FOREGROUND_CHILD") == "1" {
		_, _ = os.Stdout.WriteString("first line\n")
		time.Sleep(50 * time.Millisecond)
		_, _ = os.Stdout.WriteString("second line\n")
		return
	}

	t.Setenv("CODEBRIDGE_DATA_DIR", t.TempDir())
	var stdout, stderr bytes.Buffer
	app := App{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}
	cmd := exec.Command(os.Args[0], "-test.run=TestForegroundChildKeepsLogWriterOpen")
	cmd.Env = append(os.Environ(), "CODEBRIDGE_TEST_FOREGROUND_CHILD=1")
	child, err := app.startChild("test", cmd, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("foreground child failed: %v; stderr=%s", err, stderr.String())
	}
	for _, want := range []string{"first line", "second line"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q: %q", want, stdout.String())
		}
	}
	raw, err := os.ReadFile(config.LogPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first line", "second line"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("log missing %q: %q", want, raw)
		}
	}
}

func TestSaveMemorySecretKeepsOrClearsExistingValue(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CODEBRIDGE_HOME", base)
	path := config.DotEnvPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("CONTROL_PLANE_API_KEY=runtime\nCODEBRIDGE_MEMORY_SECRET=existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if err := saveMemorySecret(cfg, cfg.Memory.SecretEnv, "", false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.ParseDotEnv(string(raw))[cfg.Memory.SecretEnv]; got != "existing" {
		t.Fatalf("existing secret was overwritten: %q", got)
	}
	if err := saveMemorySecret(cfg, cfg.Memory.SecretEnv, "", true); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values := config.ParseDotEnv(string(raw))
	if _, exists := values[cfg.Memory.SecretEnv]; exists {
		t.Fatalf("memory secret was not cleared: %s", raw)
	}
	if values["CONTROL_PLANE_API_KEY"] != "runtime" {
		t.Fatalf("unrelated runtime key changed: %s", raw)
	}
}

func TestReadSecretInputFallsBackForNonTerminalReader(t *testing.T) {
	reader := strings.NewReader("secret value\n")
	buffered := bufio.NewReader(reader)
	var output bytes.Buffer
	value, err := readSecretInput(buffered, reader, &output)
	if err != nil {
		t.Fatal(err)
	}
	if value != "secret value" {
		t.Fatalf("secret input = %q", value)
	}
}

func TestReadHealthFallsBackToLegacyEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		http.NotFound(writer, nil)
	})
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"status": "ok", "pid": 321, "config_id": "legacy-config",
		})
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	health := readHealth(port)
	if health == nil {
		t.Fatal("expected legacy health response")
	}
	if numberValue(health["pid"]) != 321 || health["config_id"] != "legacy-config" {
		t.Fatalf("unexpected health response: %#v", health)
	}
}

func TestReadHealthRejectsUnidentifiedPublicHealth(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		http.NotFound(writer, nil)
	})
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"status": "ok"})
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	if health := readHealth(port); health != nil {
		t.Fatalf("unidentified public health must not be treated as Codebridge: %#v", health)
	}
}

func TestPortAvailableDetectsListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if portAvailable("127.0.0.1", port) {
		t.Fatalf("port %s should be busy", strconv.Itoa(port))
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if !portAvailable("127.0.0.1", port) {
		t.Fatalf("port %s should be available after close", strconv.Itoa(port))
	}
}

func TestStartupWaitTimeoutIncludesConfiguredDependencies(t *testing.T) {
	t.Setenv("CODEBRIDGE_WORKSPACE_REGISTRY_PATH", filepath.Join(t.TempDir(), "workspaces.json"))
	cfg := config.Default()
	cfg.Memory.Enabled = true
	cfg.Memory.Required = true
	cfg.Memory.TimeoutMS = 2_000
	cfg.MCPServers["one"] = config.MCPServerConfig{Command: "mcp-one", StartupTimeoutMS: 3_000}
	cfg.MCPServers["two"] = config.MCPServerConfig{StartupTimeoutMS: 4_000}
	disabled := false
	cfg.MCPServers["disabled"] = config.MCPServerConfig{Enabled: &disabled, StartupTimeoutMS: 30_000}

	if got, want := startupWaitTimeout(cfg), 34*time.Second; got != want {
		t.Fatalf("startupWaitTimeout() = %s, want %s", got, want)
	}
}

func TestStartupLogFollowerStreamsOnlyStartupLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launcher.log")
	if err := os.WriteFile(path, []byte("old line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	follower := &startupLogFollower{path: path, offset: fileSize(path)}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("noise\n[startup] mcp       connecting postgres\n[startup] mcp       connected postgres")
	_ = file.Close()

	var output bytes.Buffer
	follower.flush(&output, true)
	got := output.String()
	if strings.Contains(got, "noise") || strings.Contains(got, "old line") {
		t.Fatalf("follower leaked non-startup log lines: %q", got)
	}
	for _, want := range []string{"connecting postgres", "connected postgres"} {
		if !strings.Contains(got, want) {
			t.Fatalf("follower output missing %q: %q", want, got)
		}
	}
}

func TestWaitForHealthProgressDetectsProcessExit(t *testing.T) {
	exit := make(chan error, 1)
	exit <- os.ErrProcessDone
	startedAt := time.Now()
	var output bytes.Buffer
	_, err := waitForHealthProgress(context.Background(), 1, 5*time.Second, exit, false, nil, &output)
	if err == nil || !strings.Contains(err.Error(), "process exited") {
		t.Fatalf("unexpected startup wait error: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("process exit detection took too long: %s", elapsed)
	}
}

func TestSaveMemorySecretStoresOnlySecret(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CODEBRIDGE_HOME", base)
	path := config.DotEnvPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := strings.Join([]string{
		"CONTROL_PLANE_API_KEY=runtime-secret",
		"CODEBRIDGE_MEMORY_ENABLED=true",
		"CODEBRIDGE_MEMORY_PROVIDER=agentmemory",
		"CODEBRIDGE_MEMORY_ENDPOINT=http://127.0.0.1:3111",
		"CODEBRIDGE_MEMORY_SECRET=old-secret",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Memory.SecretEnv = "CUSTOM_MEMORY_SECRET"
	if err := saveMemorySecret(cfg, "CODEBRIDGE_MEMORY_SECRET", "new secret", false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values := config.ParseDotEnv(string(raw))
	if got := values["CONTROL_PLANE_API_KEY"]; got != "runtime-secret" {
		t.Fatalf("runtime secret changed: %q", got)
	}
	if got := values["CUSTOM_MEMORY_SECRET"]; got != "new secret" {
		t.Fatalf("memory secret = %q, want new secret", got)
	}
	for _, key := range []string{
		"CODEBRIDGE_MEMORY_ENABLED", "CODEBRIDGE_MEMORY_PROVIDER",
		"CODEBRIDGE_MEMORY_ENDPOINT", "CODEBRIDGE_MEMORY_SECRET",
	} {
		if _, exists := values[key]; exists {
			t.Fatalf("non-secret or old secret key %s remained in .env: %s", key, raw)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf(".env mode = %o, want 600", got)
		}
	}
}
