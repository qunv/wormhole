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
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const (
	DefaultPort          = 8789
	DefaultTunnelVersion = "v0.0.10"
)

type MemoryConfig struct {
	Enabled           bool           `json:"enabled"`
	Provider          string         `json:"provider"`
	Endpoint          string         `json:"endpoint,omitempty"`
	SecretEnv         string         `json:"secretEnv,omitempty"`
	TimeoutMS         int            `json:"timeoutMs,omitempty"`
	CaptureMode       string         `json:"captureMode,omitempty"`
	TokenBudget       int            `json:"tokenBudget,omitempty"`
	AgentID           string         `json:"agentId,omitempty"`
	Required          bool           `json:"required,omitempty"`
	ProjectStrategy   string         `json:"projectStrategy,omitempty"`
	Options           map[string]any `json:"options,omitempty"`
	QueueSize         int            `json:"queueSize,omitempty"`
	DeliveryTimeoutMS int            `json:"deliveryTimeoutMs,omitempty"`
	RetryMaxAttempts  int            `json:"retryMaxAttempts,omitempty"`
	RetryBackoffMS    int            `json:"retryBackoffMs,omitempty"`
	HealthCacheMS     int            `json:"healthCacheMs,omitempty"`
}

type ToolExposureConfig struct {
	AllowedGroups []string `json:"allowedGroups,omitempty"`
	DeniedTools   []string `json:"deniedTools,omitempty"`
}

var (
	toolModulePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	envNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func validateMemoryOptions(value any, path string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, entry := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			for _, forbidden := range []string{"secret", "password", "token", "apikey", "authorization", "credential"} {
				if strings.Contains(normalized, forbidden) {
					return fmt.Errorf("%s.%s must reference a secret through memory.secretEnv instead of storing it in config.json", path, key)
				}
			}
			if err := validateMemoryOptions(entry, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for index, entry := range typed {
			if err := validateMemoryOptions(entry, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

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

	Memory     MemoryConfig               `json:"memory,omitempty"`
	MCPServers map[string]MCPServerConfig `json:"mcpServers,omitempty"`
	Tools      ToolExposureConfig         `json:"tools,omitempty"`

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
		Mode:          "safe",
		Policy:        "balanced",
		Port:          DefaultPort,
		Host:          "127.0.0.1",
		Profile:       "codebridge",
		ProfileDir:    filepath.Join(AppDataDir(), "profiles"),
		RuntimeKeyEnv: "CONTROL_PLANE_API_KEY",
		TunnelBin:     filepath.Join(AppDataDir(), tunnelExecutable()),
		Memory: MemoryConfig{
			Enabled: false, Provider: "none", Endpoint: "http://127.0.0.1:3111",
			SecretEnv: "CODEBRIDGE_MEMORY_SECRET", TimeoutMS: 3_000,
			CaptureMode: "selected", TokenBudget: 1_600, AgentID: "chatgpt-codebridge",
			ProjectStrategy: "git-origin",
			QueueSize:       128, DeliveryTimeoutMS: 2_000, RetryMaxAttempts: 3,
			RetryBackoffMS: 100, HealthCacheMS: 5_000,
		},
		MCPServers:        map[string]MCPServerConfig{},
		MaxReadChars:      200_000,
		ReadDefault:       30_000,
		MaxBatchReadChars: 500_000,
		MaxCommandOutput:  200_000,
		CommandOutput:     20_000,
		MaxBodyBytes:      16 * 1024 * 1024,
		MaxProcesses:      24,
		Audit:             true,
		AuditArgs:         true,
		Workspace:         home,
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

func DotEnvPath() string { return filepath.Join(AppConfigDir(), ".env") }
func PIDPath() string    { return filepath.Join(AppDataDir(), "processes.json") }
func LogPath() string    { return filepath.Join(AppDataDir(), "launcher.log") }

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
	if !isLoopbackHost(c.Host) && strings.TrimSpace(c.AuthToken) == "" {
		return fmt.Errorf("MCP bearer token is required when host %q is not loopback", c.Host)
	}
	limits := []struct {
		name  string
		value int
	}{
		{"maxReadChars", c.MaxReadChars}, {"readDefault", c.ReadDefault},
		{"maxBatchReadChars", c.MaxBatchReadChars}, {"maxCommandOutput", c.MaxCommandOutput},
		{"commandOutputDefault", c.CommandOutput}, {"maxBodyBytes", c.MaxBodyBytes},
		{"maxProcesses", c.MaxProcesses},
		{"memory.timeoutMs", c.Memory.TimeoutMS}, {"memory.tokenBudget", c.Memory.TokenBudget},
		{"memory.queueSize", c.Memory.QueueSize}, {"memory.deliveryTimeoutMs", c.Memory.DeliveryTimeoutMS},
		{"memory.retryMaxAttempts", c.Memory.RetryMaxAttempts}, {"memory.retryBackoffMs", c.Memory.RetryBackoffMS},
		{"memory.healthCacheMs", c.Memory.HealthCacheMS},
	}
	for _, limit := range limits {
		if limit.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", limit.name)
		}
	}
	if c.ReadDefault > c.MaxReadChars {
		return fmt.Errorf("readDefault must not exceed maxReadChars")
	}
	if c.CommandOutput > c.MaxCommandOutput {
		return fmt.Errorf("commandOutputDefault must not exceed maxCommandOutput")
	}
	if c.Memory.Enabled && (strings.TrimSpace(c.Memory.Provider) == "" || c.Memory.Provider == "none") {
		return fmt.Errorf("memory.provider is required when memory is enabled")
	}
	if c.Memory.CaptureMode != "off" && c.Memory.CaptureMode != "metadata" && c.Memory.CaptureMode != "selected" {
		return fmt.Errorf("memory.captureMode must be off, metadata, or selected")
	}
	if c.Memory.ProjectStrategy != "git-origin" && c.Memory.ProjectStrategy != "path-hash" {
		return fmt.Errorf("memory.projectStrategy must be git-origin or path-hash")
	}
	if err := validateMemoryOptions(c.Memory.Options, "memory.options"); err != nil {
		return err
	}
	if err := validateMCPServers(c); err != nil {
		return err
	}
	for _, group := range c.Tools.AllowedGroups {
		if !toolModulePattern.MatchString(group) {
			return fmt.Errorf("tools.allowedGroups value %q must be a valid module name", group)
		}
	}
	for _, name := range c.Tools.DeniedTools {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("tools.deniedTools must not contain empty names")
		}
	}
	if requireWorkspace {
		info, err := os.Stat(c.Workspace)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("workspace does not exist: %s", c.Workspace)
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c Config) ConfigID(binaryPath string, widget []byte) string {
	binaryHash := "missing"
	if raw, err := os.ReadFile(binaryPath); err == nil {
		sum := sha256.Sum256(raw)
		binaryHash = hex.EncodeToString(sum[:8])
	}
	secretFingerprint := ""
	if c.Memory.SecretEnv != "" {
		if secret := os.Getenv(c.Memory.SecretEnv); secret != "" {
			sum := sha256.Sum256([]byte(secret))
			secretFingerprint = hex.EncodeToString(sum[:8])
		}
	}
	mcpServerSecretFingerprints := MCPServerSecretFingerprints(c.MCPServers)
	material, _ := json.Marshal(map[string]any{
		"workspace":   filepath.Clean(c.Workspace),
		"extraRoots":  c.ExtraRoots,
		"mode":        c.Mode,
		"policy":      c.Policy,
		"port":        c.Port,
		"authEnabled": c.AuthToken != "",
		"binaryHash":  binaryHash,
		"widgetHash":  fmt.Sprintf("%x", sha256.Sum256(widget)),
		"memory": map[string]any{
			"config": c.Memory, "secretFingerprint": secretFingerprint,
		},
		"mcpServers": map[string]any{
			"config": c.MCPServers, "secretFingerprints": mcpServerSecretFingerprints,
		},
		"tools": c.Tools,
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

func RemoveDotEnvKeys(existing string, keys ...string) string {
	removed := make(map[string]bool, len(keys))
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			removed[key] = true
		}
	}
	lines := strings.Split(strings.TrimSuffix(existing, "\n"), "\n")
	if existing == "" {
		lines = nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		key, _, ok := strings.Cut(trimmed, "=")
		if ok && removed[strings.TrimSpace(key)] {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
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
	if c.Memory.Provider == "" {
		c.Memory.Provider = "none"
	}
	if c.Memory.Endpoint == "" {
		c.Memory.Endpoint = "http://127.0.0.1:3111"
	}
	if c.Memory.SecretEnv == "" {
		c.Memory.SecretEnv = "CODEBRIDGE_MEMORY_SECRET"
	}
	if c.Memory.TimeoutMS == 0 {
		c.Memory.TimeoutMS = 3_000
	}
	if c.Memory.CaptureMode == "" {
		c.Memory.CaptureMode = "selected"
	}
	if c.Memory.TokenBudget == 0 {
		c.Memory.TokenBudget = 1_600
	}
	if c.Memory.AgentID == "" {
		c.Memory.AgentID = "chatgpt-codebridge"
	}
	if c.Memory.ProjectStrategy == "" {
		c.Memory.ProjectStrategy = "git-origin"
	}
	if c.Memory.QueueSize == 0 {
		c.Memory.QueueSize = 128
	}
	if c.Memory.DeliveryTimeoutMS == 0 {
		c.Memory.DeliveryTimeoutMS = 2_000
	}
	if c.Memory.RetryMaxAttempts == 0 {
		c.Memory.RetryMaxAttempts = 3
	}
	if c.Memory.RetryBackoffMS == 0 {
		c.Memory.RetryBackoffMS = 100
	}
	if c.Memory.HealthCacheMS == 0 {
		c.Memory.HealthCacheMS = 5_000
	}
	normalizeMCPServers(c)
	for index, group := range c.Tools.AllowedGroups {
		c.Tools.AllowedGroups[index] = strings.ToLower(strings.TrimSpace(group))
	}
	for index, name := range c.Tools.DeniedTools {
		c.Tools.DeniedTools[index] = strings.TrimSpace(name)
	}
	c.Memory.Provider = strings.ToLower(strings.TrimSpace(c.Memory.Provider))
	c.Memory.CaptureMode = strings.ToLower(strings.TrimSpace(c.Memory.CaptureMode))
	c.Memory.ProjectStrategy = strings.ToLower(strings.TrimSpace(c.Memory.ProjectStrategy))
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
	stringEnv("CODEBRIDGE_MEMORY_PROVIDER", &c.Memory.Provider)
	stringEnv("CODEBRIDGE_MEMORY_ENDPOINT", &c.Memory.Endpoint)
	stringEnv("CODEBRIDGE_MEMORY_SECRET_ENV", &c.Memory.SecretEnv)
	stringEnv("CODEBRIDGE_MEMORY_CAPTURE", &c.Memory.CaptureMode)
	stringEnv("CODEBRIDGE_MEMORY_AGENT_ID", &c.Memory.AgentID)
	stringEnv("CODEBRIDGE_MEMORY_PROJECT_STRATEGY", &c.Memory.ProjectStrategy)
	intEnv("PORT", &c.Port)
	intEnv("CODEBRIDGE_MEMORY_TIMEOUT_MS", &c.Memory.TimeoutMS)
	intEnv("CODEBRIDGE_MEMORY_TOKEN_BUDGET", &c.Memory.TokenBudget)
	intEnv("CODEBRIDGE_MEMORY_QUEUE_SIZE", &c.Memory.QueueSize)
	intEnv("CODEBRIDGE_MEMORY_DELIVERY_TIMEOUT_MS", &c.Memory.DeliveryTimeoutMS)
	intEnv("CODEBRIDGE_MEMORY_RETRY_MAX_ATTEMPTS", &c.Memory.RetryMaxAttempts)
	intEnv("CODEBRIDGE_MEMORY_RETRY_BACKOFF_MS", &c.Memory.RetryBackoffMS)
	intEnv("CODEBRIDGE_MEMORY_HEALTH_CACHE_MS", &c.Memory.HealthCacheMS)
	intEnv("AGENT_MAX_READ_CHARS", &c.MaxReadChars)
	intEnv("AGENT_READ_DEFAULT", &c.ReadDefault)
	intEnv("AGENT_MAX_BATCH_READ_CHARS", &c.MaxBatchReadChars)
	intEnv("AGENT_MAX_COMMAND_OUTPUT", &c.MaxCommandOutput)
	intEnv("AGENT_CMD_OUTPUT_DEFAULT", &c.CommandOutput)
	intEnv("AGENT_MAX_BODY_BYTES", &c.MaxBodyBytes)
	c.Memory.Enabled = envBool("CODEBRIDGE_MEMORY_ENABLED", c.Memory.Enabled)
	c.Memory.Required = envBool("CODEBRIDGE_MEMORY_REQUIRED", c.Memory.Required)
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
