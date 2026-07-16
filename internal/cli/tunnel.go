// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"codebridge/internal/config"
)

const tunnelReleaseBase = "https://github.com/openai/tunnel-client/releases/download"

func (a App) tunnelCommand(ctx context.Context, cfg config.Config, opts options) error {
	sub := "status"
	if len(opts.Rest) > 0 {
		sub = opts.Rest[0]
	}
	switch sub {
	case "status":
		_, err := os.Stat(cfg.TunnelBin)
		fmt.Fprintf(a.Stdout, "Path: %s\nInstalled: %v\nVersion: %s\n", cfg.TunnelBin, err == nil, config.DefaultTunnelVersion)
		return nil
	case "install":
		path, err := downloadTunnelClient(ctx, cfg.TunnelBin)
		if err != nil {
			return err
		}
		cfg.TunnelBin = path
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "Installed tunnel-client: %s\n", path)
		return nil
	default:
		return errors.New("usage: codebridge tunnel status|install")
	}
}

func downloadTunnelClient(ctx context.Context, destination string) (string, error) {
	tunnelOS := runtime.GOOS
	if tunnelOS == "windows" {
		tunnelOS = "windows"
	}
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return "", fmt.Errorf("unsupported tunnel-client architecture: %s", arch)
	}
	version := os.Getenv("TUNNEL_CLIENT_VERSION")
	if version == "" {
		version = config.DefaultTunnelVersion
	}
	asset := fmt.Sprintf("tunnel-client-%s-%s-%s.zip", version, tunnelOS, arch)
	url := fmt.Sprintf("%s/%s/%s", tunnelReleaseBase, version, asset)
	client := &http.Client{Timeout: 2 * time.Minute}
	data, err := fetch(ctx, client, url)
	if err != nil {
		return "", err
	}
	if sums, sumErr := fetch(ctx, client, fmt.Sprintf("%s/%s/SHA256SUMS.txt", tunnelReleaseBase, version)); sumErr == nil {
		expected := checksumFor(string(sums), asset)
		if expected != "" {
			actual := sha256.Sum256(data)
			if hex.EncodeToString(actual[:]) != expected {
				return "", fmt.Errorf("SHA256 mismatch for %s", asset)
			}
		}
	}
	tempDir, err := os.MkdirTemp("", "codebridge-tunnel-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tempDir)
	archivePath := filepath.Join(tempDir, asset)
	if err := os.WriteFile(archivePath, data, 0o600); err != nil {
		return "", err
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	want := "tunnel-client"
	if runtime.GOOS == "windows" {
		want += ".exe"
	}
	var source *zip.File
	for _, file := range reader.File {
		base := filepath.Base(file.Name)
		if base == want || base == "tunnel-client" || base == "tunnel-client.exe" {
			source = file
			break
		}
	}
	if source == nil {
		return "", fmt.Errorf("%s was not found inside %s", want, asset)
	}
	input, err := source.Open()
	if err != nil {
		return "", err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return "", err
	}
	if err := output.Close(); err != nil {
		return "", err
	}
	return destination, nil
}

func fetch(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "codebridge-setup")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s failed: HTTP %d", url, response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, 200<<20))
}

func checksumFor(text, asset string) string {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == asset {
			return strings.ToLower(fields[0])
		}
	}
	return ""
}
