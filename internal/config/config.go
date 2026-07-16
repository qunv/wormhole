// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	DefaultPort            = 8789
	DefaultTunnelVersion   = "v0.0.10"
	DefaultFigmaDesktopURL = "http://127.0.0.1:3845/mcp"
)

type Config struct {
	Workspace      string   `json:"workspace"`
	ExtraRoots     []string `json:"extraRoots,omitempty"`
	Mode           string   `json:"mode"`
	Policy         string   `json:"policy"`
	Port           int      `json:"port"`
	Host           string   `json:"host"`
	AuthToken      string   `json:"authToken,omitempty"`
	ApprovalToken  string   `json:"approvalToken,omitempty"`
	AllowedOrigins []string `json:"allowedOrigins,omitempty"`

	NoTunnel      bool   `json:"noTunnel,omitempty"`
	TunnelBin     string `json:"tunnelBin,omitempty"`
	TunnelID      string `json:"tunnelId,omitempty"`
	Organization  string `json:"organizationId,omitempty"`
	Profile       string `json:"profile,omitempty"`
	ProfileDir    string `json:"profileDir,omitempty"`
	RuntimeKeyEnv string `json:"runtimeKeyEnv,omitempty"`

	FigmaDesktopURL         string `json:"figmaDesktopMcpUrl,omitempty"`
	FigmaDesktopTimeoutMS   int    `json:"figmaDesktopTimeoutMs,omitempty"`
	FigmaDesktopAllowRemote bool   `json:"figmaDesktopAllowRemote,omitempty"`

	MaxReadChars      int `json:"maxReadChars,omitempty"`
	ReadDefault       int `json:"readDefault,omitempty"`
	MaxBatchReadChars int `json:"maxBatchReadChars,omitempty"`
	MaxCommandOutput  int `json:"maxCommandOutput,omitempty"`
	CommandOutput     int `json:"commandOutputDefault,omitempty"`
	MaxBodyBytes      int `json:"maxBodyBytes,omitempty"`
	MaxProcesses      int `json:"maxProcesses,omitempty"`

	Audit     bool `json:"audit"`
	AuditArgs bool `json:"auditArgs"`
	HTTPLog   bool `json:"httpLog,omitempty"`
}

func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Mode:                  "safe",
		Policy:                "balanced",
		Port:                  DefaultPort,
		Host:                  "127.0.0.1",
		Profile:               "codebridge",
		ProfileDir:            filepath.Join(AppDataDir(), "profiles"),
		RuntimeKeyEnv:         "CONTROL_PLANE_API_KEY",
		TunnelBin:             filepath.Join(AppDataDir(), tunnelExecutable()),
		FigmaDesktopURL:       DefaultFigmaDesktopURL,
		FigmaDesktopTimeoutMS: 30_000,
		MaxReadChars:          200_000,
		ReadDefault:           30_000,
		MaxBatchReadChars:     500_000,
		MaxCommandOutput:      200_000,
		CommandOutput:         20_000,
		MaxBodyBytes:          16 * 1024 * 1024,
		MaxProcesses:          24,
		Audit:                 true,
		AuditArgs:             true,
		Workspace:             home,
	}
}

func ConfigPath() string {
	if value := os.Getenv("CODEBRIDGE_CONFIG_PATH"); value != "" {
		return value
	}
	return filepath.Join(AppConfigDir(), "config.json")
}

func AppConfigDir() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, "Codebridge")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Codebridge")
	default:
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "codebridge")
	}
}

func AppDataDir() string {
	if value := os.Getenv("CODEBRIDGE_DATA_DIR"); value != "" {
		return value
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, "Codebridge")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Codebridge")
	default:
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			base = filepath.Join(home, ".local", "state")
		}
		return filepath.Join(base, "codebridge")
	}
}

func PIDPath() string { return filepath.Join(AppDataDir(), "processes.json") }
func LogPath() string { return filepath.Join(AppDataDir(), "launcher.log") }

func Load() (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(ConfigPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	if err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config: %w", err)
		}
	}
	applyEnvironment(&cfg)
	normalize(&cfg)
	return cfg, cfg.Validate(false)
}

func Save(cfg Config) error {
	normalize(&cfg)
	if err := cfg.Validate(false); err != nil {
		return err
	}
	cfg.AuthToken = ""
	cfg.ApprovalToken = ""
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(ConfigPath(), append(raw, '\n'), 0o600)
}

func (c Config) Validate(requireWorkspace bool) error {
	if c.Mode != "safe" && c.Mode != "full" {
		return fmt.Errorf("mode must be safe or full")
	}
	if c.Policy != "strict" && c.Policy != "balanced" && c.Policy != "full" {
		return fmt.Errorf("policy must be strict, balanced, or full")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if requireWorkspace {
		info, err := os.Stat(c.Workspace)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("workspace does not exist: %s", c.Workspace)
		}
	}
	return nil
}

func (c Config) ConfigID(binaryPath string, widget []byte) string {
	binaryHash := "missing"
	if raw, err := os.ReadFile(binaryPath); err == nil {
		sum := sha256.Sum256(raw)
		binaryHash = hex.EncodeToString(sum[:8])
	}
	material, _ := json.Marshal(map[string]any{
		"workspace":       filepath.Clean(c.Workspace),
		"extraRoots":      c.ExtraRoots,
		"mode":            c.Mode,
		"policy":          c.Policy,
		"port":            c.Port,
		"authEnabled":     c.AuthToken != "",
		"binaryHash":      binaryHash,
		"widgetHash":      fmt.Sprintf("%x", sha256.Sum256(widget)),
		"figmaDesktopURL": c.FigmaDesktopURL,
	})
	sum := sha256.Sum256(material)
	return hex.EncodeToString(sum[:8])
}

func ParseDotEnv(text string) map[string]string {
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		values[key] = strings.ReplaceAll(value, `\n`, "\n")
	}
	return values
}

func MergeDotEnv(existing string, updates map[string]string) string {
	lines := strings.Split(strings.TrimSuffix(existing, "\n"), "\n")
	if existing == "" {
		lines = nil
	}
	seen := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		if value, exists := updates[strings.TrimSpace(key)]; exists {
			prefix := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = prefix + strings.TrimSpace(key) + "=" + formatEnv(value)
			seen[strings.TrimSpace(key)] = true
		}
	}
	for key, value := range updates {
		if !seen[key] {
			lines = append(lines, key+"="+formatEnv(value))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func LoadDotEnv(path string, override bool) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for key, value := range ParseDotEnv(string(raw)) {
		if override || os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
	return nil
}

func normalize(c *Config) {
	if c.Mode == "" {
		c.Mode = "safe"
	}
	if c.Policy == "" {
		c.Policy = "balanced"
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.RuntimeKeyEnv == "" {
		c.RuntimeKeyEnv = "CONTROL_PLANE_API_KEY"
	}
	if c.Profile == "" {
		c.Profile = "codebridge"
	}
	if c.ProfileDir == "" {
		c.ProfileDir = filepath.Join(AppDataDir(), "profiles")
	}
	if c.TunnelBin == "" {
		c.TunnelBin = filepath.Join(AppDataDir(), tunnelExecutable())
	}
	if c.FigmaDesktopURL == "" {
		c.FigmaDesktopURL = DefaultFigmaDesktopURL
	}
	if c.FigmaDesktopTimeoutMS == 0 {
		c.FigmaDesktopTimeoutMS = 30_000
	}
	if c.MaxReadChars == 0 {
		c.MaxReadChars = 200_000
	}
	if c.ReadDefault == 0 {
		c.ReadDefault = 30_000
	}
	if c.MaxBatchReadChars == 0 {
		c.MaxBatchReadChars = 500_000
	}
	if c.MaxCommandOutput == 0 {
		c.MaxCommandOutput = 200_000
	}
	if c.CommandOutput == 0 {
		c.CommandOutput = 20_000
	}
	if c.MaxBodyBytes == 0 {
		c.MaxBodyBytes = 16 * 1024 * 1024
	}
	if c.MaxProcesses == 0 {
		c.MaxProcesses = 24
	}
	if c.Workspace != "" {
		if abs, err := filepath.Abs(c.Workspace); err == nil {
			c.Workspace = abs
		}
	}
	for i, root := range c.ExtraRoots {
		if abs, err := filepath.Abs(root); err == nil {
			c.ExtraRoots[i] = abs
		}
	}
}

func applyEnvironment(c *Config) {
	stringEnv("AGENT_WORKSPACE", &c.Workspace)
	stringEnv("AGENT_MODE", &c.Mode)
	stringEnv("AGENT_POLICY", &c.Policy)
	stringEnv("AGENT_HOST", &c.Host)
	stringEnv("MCP_AUTH_TOKEN", &c.AuthToken)
	stringEnv("AGENT_APPROVAL_TOKEN", &c.ApprovalToken)
	stringEnv("CONTROL_PLANE_TUNNEL_ID", &c.TunnelID)
	stringEnv("OPENAI_ORGANIZATION", &c.Organization)
	stringEnv("TUNNEL_BIN", &c.TunnelBin)
	stringEnv("TUNNEL_PROFILE", &c.Profile)
	stringEnv("TUNNEL_PROFILE_DIR", &c.ProfileDir)
	stringEnv("FIGMA_DESKTOP_MCP_URL", &c.FigmaDesktopURL)
	intEnv("PORT", &c.Port)
	intEnv("FIGMA_DESKTOP_TIMEOUT_MS", &c.FigmaDesktopTimeoutMS)
	intEnv("AGENT_MAX_READ_CHARS", &c.MaxReadChars)
	intEnv("AGENT_READ_DEFAULT", &c.ReadDefault)
	intEnv("AGENT_MAX_BATCH_READ_CHARS", &c.MaxBatchReadChars)
	intEnv("AGENT_MAX_COMMAND_OUTPUT", &c.MaxCommandOutput)
	intEnv("AGENT_CMD_OUTPUT_DEFAULT", &c.CommandOutput)
	intEnv("AGENT_MAX_BODY_BYTES", &c.MaxBodyBytes)
	c.FigmaDesktopAllowRemote = envBool("FIGMA_DESKTOP_ALLOW_REMOTE", c.FigmaDesktopAllowRemote)
	c.Audit = !envIs("AGENT_AUDIT", "0", !c.Audit)
	c.AuditArgs = !envIs("AGENT_AUDIT_ARGS", "0", !c.AuditArgs)
	c.HTTPLog = envBool("AGENT_HTTP_LOG", c.HTTPLog)
	if raw := os.Getenv("AGENT_EXTRA_ROOTS_JSON"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &c.ExtraRoots)
	} else if raw := os.Getenv("AGENT_EXTRA_ROOTS"); raw != "" {
		c.ExtraRoots = splitExtraRoots(raw)
	}
	if raw := os.Getenv("MCP_ALLOWED_ORIGINS"); raw != "" {
		c.AllowedOrigins = splitComma(raw)
	}
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func stringEnv(name string, target *string) {
	if value := os.Getenv(name); value != "" {
		*target = value
	}
}

func intEnv(name string, target *int) {
	if value := os.Getenv(name); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			*target = parsed
		}
	}
}

func envBool(name string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envIs(name, expected string, fallback bool) bool {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	return value == expected
}

func splitExtraRoots(value string) []string {
	separator := ";"
	if runtime.GOOS != "windows" && !strings.Contains(value, ";") {
		separator = string(os.PathListSeparator)
	}
	var out []string
	for _, item := range strings.Split(value, separator) {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func splitComma(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func formatEnv(value string) string {
	if !strings.ContainsAny(value, " \t\r\n\"'#") {
		return value
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return `"` + value + `"`
}

func tunnelExecutable() string {
	if runtime.GOOS == "windows" {
		return "tunnel-client.exe"
	}
	return "tunnel-client"
}
