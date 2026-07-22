package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestVerifyChecksumRequiresValidMatchingEntry(t *testing.T) {
	data := []byte("archive")
	asset := "tunnel.zip"
	sum := sha256.Sum256(data)
	valid := fmt.Sprintf("%x  %s\n", sum, asset)
	if err := verifyChecksum(data, valid, asset); err != nil {
		t.Fatalf("valid checksum rejected: %v", err)
	}
	for name, checksumText := range map[string]string{
		"missing":  fmt.Sprintf("%x  other.zip\n", sum),
		"invalid":  "not-a-sha  tunnel.zip\n",
		"mismatch": fmt.Sprintf("%064x  %s\n", 1, asset),
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifyChecksum(data, checksumText, asset); err == nil {
				t.Fatal("expected checksum verification to fail")
			}
		})
	}
}

func TestDownloadTunnelClientFailsClosedWhenChecksumsUnavailable(t *testing.T) {
	var assetRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/SHA256SUMS.txt") {
			http.Error(writer, "missing", http.StatusNotFound)
			return
		}
		assetRequests.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	setTunnelReleaseBase(t, server.URL)
	t.Setenv("TUNNEL_CLIENT_VERSION", "vtest")
	destination := filepath.Join(t.TempDir(), "tunnel-client")

	if _, err := downloadTunnelClient(context.Background(), destination); err == nil || !strings.Contains(err.Error(), "checksums") {
		t.Fatalf("unexpected error: %v", err)
	}
	if assetRequests.Load() != 0 {
		t.Fatalf("asset was downloaded before checksum metadata: %d request(s)", assetRequests.Load())
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination should not exist after verification failure: %v", err)
	}
}

func TestDownloadTunnelClientVerifiesAndInstallsAtomically(t *testing.T) {
	version := "vtest"
	asset := fmt.Sprintf("tunnel-client-%s-%s-%s.zip", version, runtime.GOOS, runtime.GOARCH)
	binaryName := "tunnel-client"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	archive := tunnelArchive(t, binaryName, []byte("verified binary"))
	sum := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/SHA256SUMS.txt"):
			fmt.Fprintf(writer, "%x  %s\n", sum, asset)
		case strings.HasSuffix(request.URL.Path, "/"+asset):
			_, _ = writer.Write(archive)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	setTunnelReleaseBase(t, server.URL)
	t.Setenv("TUNNEL_CLIENT_VERSION", version)
	destination := filepath.Join(t.TempDir(), binaryName)

	installed, err := downloadTunnelClient(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if installed != destination {
		t.Fatalf("installed path = %q, want %q", installed, destination)
	}
	raw, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "verified binary" {
		t.Fatalf("installed content = %q", raw)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(destination)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("installed mode = %o, want 755", got)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tunnel-client-") {
			t.Fatalf("temporary install file remained: %s", entry.Name())
		}
	}
}

func tunnelArchive(t *testing.T, binaryName string, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create(filepath.ToSlash(filepath.Join("release", binaryName)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func setTunnelReleaseBase(t *testing.T, value string) {
	t.Helper()
	previous := tunnelReleaseBase
	tunnelReleaseBase = value
	t.Cleanup(func() { tunnelReleaseBase = previous })
}
