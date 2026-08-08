// Wormhole
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

	"wormhole/internal/config"
)

const maxTunnelDownloadBytes = 200 << 20

var tunnelReleaseBase = "https://github.com/openai/tunnel-client/releases/download"

func (a App) tunnelCommand(ctx context.Context, cfg config.Config, opts options) error {
	sub := "status"
	if len(opts.Rest) > 0 {
		sub = opts.Rest[0]
	}
	switch sub {
	case "status", "list":
		_, err := os.Stat(cfg.TunnelBin)
		fmt.Fprintf(a.Stdout, "Path: %s\nInstalled: %v\nVersion: %s\n", cfg.TunnelBin, err == nil, config.DefaultTunnelVersion)
		state := readState()
		migrateTunnelProcessState(&state)
		health := readHealth(cfg.Port)
		_, _, serverOwned := ownedHealthProcess(state, health, cfg.Port)
		serverStateValid := health == nil && state.Port == cfg.Port && processMatches(state.ServerPID, state.ServerIdentity)
		configured := cfg.EffectiveTunnels()
		if len(configured) == 0 {
			fmt.Fprintln(a.Stdout, "Tunnels: none configured")
			return nil
		}
		for _, tunnel := range configured {
			process := state.Tunnels[tunnel.Name]
			_, _, alive := ownedNamedTunnelProcess(tunnel.Name, process, serverOwned || serverStateValid)
			fmt.Fprintf(a.Stdout, "%s: mode=%s enabled=%t profile=%s key_env=%s status=%s\n",
				tunnel.Name, tunnel.Config.Mode, tunnel.Config.IsEnabled(), tunnel.Config.Profile,
				tunnel.Config.RuntimeKeyEnv, ternary(alive, fmt.Sprintf("running pid=%d", process.PID), "offline"))
		}
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
		return errors.New("usage: wormhole tunnel status|list|install")
	}
}

func downloadTunnelClient(ctx context.Context, destination string) (string, error) {
	tunnelOS := runtime.GOOS
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return "", fmt.Errorf("unsupported tunnel-client architecture: %s", arch)
	}
	version := os.Getenv("TUNNEL_CLIENT_VERSION")
	if version == "" {
		version = config.DefaultTunnelVersion
	}
	asset := fmt.Sprintf("tunnel-client-%s-%s-%s.zip", version, tunnelOS, arch)
	baseURL := fmt.Sprintf("%s/%s", strings.TrimRight(tunnelReleaseBase, "/"), version)
	client := &http.Client{Timeout: 2 * time.Minute}

	sums, err := fetch(ctx, client, baseURL+"/SHA256SUMS.txt")
	if err != nil {
		return "", fmt.Errorf("download tunnel-client checksums: %w", err)
	}
	data, err := fetch(ctx, client, baseURL+"/"+asset)
	if err != nil {
		return "", err
	}
	if err := verifyChecksum(data, string(sums), asset); err != nil {
		return "", err
	}

	tempDir, err := os.MkdirTemp("", "wormhole-tunnel-*")
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
	if err := installTunnelBinary(destination, input); err != nil {
		return "", err
	}
	return destination, nil
}

func verifyChecksum(data []byte, checksumText, asset string) error {
	expected := checksumFor(checksumText, asset)
	if expected == "" {
		return fmt.Errorf("checksum entry not found for %s", asset)
	}
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("invalid SHA256 checksum for %s", asset)
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("invalid SHA256 checksum for %s: %w", asset, err)
	}
	actual := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(actual[:]), expected) {
		return fmt.Errorf("SHA256 mismatch for %s", asset)
	}
	return nil
}

func installTunnelBinary(destination string, input io.Reader) error {
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".tunnel-client-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o755); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := io.Copy(temp, input); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, destination)
}

func fetch(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "wormhole-setup")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s failed: HTTP %d", url, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxTunnelDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTunnelDownloadBytes {
		return nil, fmt.Errorf("GET %s exceeded %d bytes", url, maxTunnelDownloadBytes)
	}
	return data, nil
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
