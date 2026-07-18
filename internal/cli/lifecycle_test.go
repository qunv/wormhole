package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
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
