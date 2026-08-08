// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var scpGitURL = regexp.MustCompile(`^[^@]+@([^:]+):(.+)$`)

func ResolveProject(root, strategy string) string {
	if strategy == "path-hash" {
		return pathProject(root)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "config", "--get", "remote.origin.url")
	if raw, err := cmd.Output(); err == nil {
		if project := NormalizeGitOrigin(strings.TrimSpace(string(raw))); project != "" {
			return project
		}
	}
	return pathProject(root)
}

func NormalizeGitOrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return ""
	}
	if match := scpGitURL.FindStringSubmatch(origin); len(match) == 3 {
		origin = match[1] + "/" + match[2]
	} else {
		raw := strings.TrimPrefix(origin, "git+")
		if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
			origin = parsed.Hostname() + "/" + strings.TrimPrefix(parsed.Path, "/")
		} else {
			origin = strings.TrimPrefix(raw, "git@")
			origin = strings.Replace(origin, ":", "/", 1)
		}
	}
	origin = strings.TrimSuffix(strings.TrimSuffix(origin, "/"), ".git")
	origin = strings.Trim(origin, "/")
	if origin == "" {
		return ""
	}
	return "git:" + strings.ToLower(origin)
}

func pathProject(root string) string {
	canonical, err := filepath.Abs(root)
	if err != nil {
		canonical = filepath.Clean(root)
	}
	sum := sha256.Sum256([]byte(filepath.Clean(canonical)))
	return "workspace:" + hex.EncodeToString(sum[:8])
}
