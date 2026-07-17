package cli

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"testing"
)

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
