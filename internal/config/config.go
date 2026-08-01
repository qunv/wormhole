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
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	DefaultPort                 = 8789
	DefaultTunnelVersion        = "v0.0.10"
	legacyLayoutMigrationMarker = ".legacy-layout-v1-complete"
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
	DeliveryWorkers   int            `json:"deliveryWorkers,omitempty"`
	DeliveryTimeoutMS int            `json:"deliveryTimeoutMs,omitempty"`
	RetryMaxAttempts  int            `json:"retryMaxAttempts,omitempty"`
	RetryBackoffMS    int            `json:"retryBackoffMs,omitempty"`
	HealthCacheMS     int            `json:"healthCacheMs,omitempty"`
}

type ToolExposureConfig struct {
	AllowedGroups []string `json:"allowedGroups,omitempty"`
	AllowedTools  []string `json:"allowedTools,omitempty"`
	DeniedTools   []string `json:"deniedTools,omitempty"`
}

// TunnelConfig describes one independently managed Secure MCP Tunnel. Each
// tunnel-client process exposes its selected local session endpoint as channel
// "main", because ChatGPT's current tunnel UI selects a tunnel ID but does not
// expose logical channel selection.
type TunnelConfig struct {
	Enabled       *bool  `json:"enabled,omitempty"`
	TunnelID      string `json:"tunnelId,omitempty"`
	Mode          string `json:"mode,omitempty"`
	Profile       string `json:"profile,omitempty"`
	RuntimeKeyEnv string `json:"runtimeKeyEnv,omitempty"`
	Organization  string `json:"organizationId,omitempty"`
}

func (c TunnelConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

type NamedTunnel struct {
	Name   string
	Config TunnelConfig
	Legacy bool
}

var (
	toolModulePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	tunnelNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
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

	NoTunnel      bool                    `json:"noTunnel,omitempty"`
	TunnelBin     string                  `json:"tunnelBin,omitempty"`
	TunnelID      string                  `json:"tunnelId,omitempty"`
	Organization  string                  `json:"organizationId,omitempty"`
	Profile       string                  `json:"profile,omitempty"`
	ProfileDir    string                  `json:"profileDir,omitempty"`
	RuntimeKeyEnv string                  `json:"runtimeKeyEnv,omitempty"`
	Tunnels       map[string]TunnelConfig `json:"tunnels,omitempty"`

	Memory     MemoryConfig               `json:"memory,omitempty"`
	MCPServers map[string]MCPServerConfig `json:"mcpServers,omitempty"`
	Tools      ToolExposureConfig         `json:"tools,omitempty"`

	MaxReadChars           int `json:"maxReadChars,omitempty"`
	ReadDefault            int `json:"readDefault,omitempty"`
	MaxBatchReadChars      int `json:"maxBatchReadChars,omitempty"`
	MaxCommandOutput       int `json:"maxCommandOutput,omitempty"`
	CommandOutput          int `json:"commandOutputDefault,omitempty"`
	MaxBodyBytes           int `json:"maxBodyBytes,omitempty"`
	MaxProcesses           int `json:"maxProcesses,omitempty"`
	MaxConcurrentToolCalls int `json:"maxConcurrentToolCalls,omitempty"`
	GitStatusCacheMS       int `json:"gitStatusCacheMs,omitempty"`

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
			QueueSize:       128, DeliveryWorkers: 4, DeliveryTimeoutMS: 2_000, RetryMaxAttempts: 3,
			RetryBackoffMS: 100, HealthCacheMS: 5_000,
		},
		MCPServers:             map[string]MCPServerConfig{},
		MaxReadChars:           200_000,
		ReadDefault:            30_000,
		MaxBatchReadChars:      500_000,
		MaxCommandOutput:       200_000,
		CommandOutput:          20_000,
		MaxBodyBytes:           16 * 1024 * 1024,
		MaxProcesses:           24,
		MaxConcurrentToolCalls: 16,
		GitStatusCacheMS:       2_000,
		Audit:                  true,
		AuditArgs:              true,
		Workspace:              home,
	}
}

func ConfigPath() string {
	if value := strings.TrimSpace(os.Getenv("CODEBRIDGE_CONFIG_PATH")); value != "" {
		return filepath.Clean(value)
	}
	return filepath.Join(AppConfigDir(), "config.json")
}

// AppHomeDir is the canonical root for persistent Codebridge files. Keeping
// configuration and runtime state below one directory makes installations
// easier to inspect, back up, and relocate.
func AppHomeDir() string {
	if value := strings.TrimSpace(os.Getenv("CODEBRIDGE_HOME")); value != "" {
		return filepath.Clean(value)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codebridge")
}

func AppConfigDir() string { return AppHomeDir() }

func AppDataDir() string {
	if value := strings.TrimSpace(os.Getenv("CODEBRIDGE_DATA_DIR")); value != "" {
		return filepath.Clean(value)
	}
	return filepath.Join(AppHomeDir(), "state")
}

// LegacyConfigDir and LegacyDataDir describe the pre-.codebridge defaults.
// They are exported for schema migrations that contain absolute legacy paths.
func LegacyConfigDir() string {
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

func LegacyDataDir() string {
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

func DotEnvPath() string    { return filepath.Join(AppConfigDir(), ".env") }
func AdminAuthPath() string { return filepath.Join(AppConfigDir(), "admin-auth.json") }
func PIDPath() string       { return filepath.Join(AppDataDir(), "processes.json") }
func ServerLogPath() string { return filepath.Join(AppDataDir(), "server.log") }
func TunnelLogPath() string { return TunnelLogPathFor("default") }
func TunnelLogPathFor(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "default" {
		return filepath.Join(AppDataDir(), "tunnel.log")
	}
	if !tunnelNamePattern.MatchString(name) {
		sum := sha256.Sum256([]byte(name))
		name = "invalid-" + hex.EncodeToString(sum[:4])
	}
	return filepath.Join(AppDataDir(), "tunnel-"+name+".log")
}

// LogPath is the legacy combined launcher log retained for migration and
// compatibility with older installations. New child processes write to the
// separate server and tunnel paths.
func LogPath() string { return filepath.Join(AppDataDir(), "launcher.log") }

// MigrateLegacyLayout copies files from the former OS-specific config/state
// directories into ~/.codebridge. Existing destinations are never replaced
// and legacy files are intentionally retained as a rollback-safe backup.
func MigrateLegacyLayout() error {
	if customLayoutConfigured() {
		return nil
	}
	markerPath := filepath.Join(AppHomeDir(), legacyLayoutMigrationMarker)
	if _, err := os.Stat(markerPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check legacy layout migration marker: %w", err)
	}
	legacyConfig, legacyData := LegacyConfigDir(), LegacyDataDir()
	if samePath(legacyConfig, AppConfigDir()) && samePath(legacyData, AppDataDir()) {
		return atomicWrite(markerPath, []byte("completed\n"), 0o600)
	}

	for _, name := range []string{"config.json", ".env", "workspaces.json"} {
		if err := copyPathMissing(filepath.Join(legacyConfig, name), filepath.Join(AppConfigDir(), name)); err != nil {
			return fmt.Errorf("migrate legacy config %s: %w", name, err)
		}
	}
	if err := copyLegacyWorkspaceDirs(filepath.Join(legacyConfig, "workspaces"), filepath.Join(AppConfigDir(), "workspaces"), true); err != nil {
		return fmt.Errorf("migrate legacy workspace configs: %w", err)
	}
	for _, name := range []string{
		"processes.json", "launcher.log", "audit.log", "profiles", "instances", tunnelExecutable(),
	} {
		if err := copyPathMissing(filepath.Join(legacyData, name), filepath.Join(AppDataDir(), name)); err != nil {
			return fmt.Errorf("migrate legacy state %s: %w", name, err)
		}
	}
	if err := copyLegacyWorkspaceDirs(filepath.Join(legacyData, "workspaces"), filepath.Join(AppDataDir(), "workspaces"), false); err != nil {
		return fmt.Errorf("migrate legacy workspace state: %w", err)
	}
	// The marker is written only after every migration step succeeds. Legacy
	// files remain as rollback-safe backups, but are never copied back into the
	// canonical tree after a user or state GC intentionally removes an entry.
	return atomicWrite(markerPath, []byte("completed\n"), 0o600)
}

func customLayoutConfigured() bool {
	for _, name := range []string{
		"CODEBRIDGE_HOME", "CODEBRIDGE_CONFIG_PATH", "CODEBRIDGE_DATA_DIR", "CODEBRIDGE_WORKSPACE_REGISTRY_PATH",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

func copyLegacyWorkspaceDirs(source, destination string, wantConfig bool) error {
	entries, err := os.ReadDir(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		_, configErr := os.Stat(filepath.Join(source, entry.Name(), "config.json"))
		if configErr != nil && !errors.Is(configErr, os.ErrNotExist) {
			return configErr
		}
		hasConfig := configErr == nil
		if wantConfig != hasConfig {
			continue
		}
		if err := copyPathMissing(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyPathMissing(source, destination string) error {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o700
		}
		if err := os.MkdirAll(destination, mode); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyPathMissing(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if _, err := os.Lstat(destination); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		return os.Symlink(target, destination)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o600
	}
	destinationFile, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = destinationFile.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(destinationFile, sourceFile); err != nil {
		return err
	}
	if err := destinationFile.Sync(); err != nil {
		return err
	}
	if err := destinationFile.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func samePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func Load() (Config, error) {
	if err := MigrateLegacyLayout(); err != nil {
		return Default(), err
	}
	cfg, err := loadFile(ConfigPath(), true)
	return cfg, err
}

// LoadFile loads one non-secret configuration file without applying ambient
// environment overrides. It is used by the multi-workspace daemon so a process
// level AGENT_WORKSPACE or PORT cannot repoint a registered workspace runtime.
func LoadFile(path string) (Config, error) {
	return loadFile(path, false)
}

func loadFile(path string, useEnvironment bool) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	if err == nil {
		object, parseErr := decodeConfigObject(raw, path)
		if parseErr != nil {
			return cfg, parseErr
		}
		cleanRaw, marshalErr := json.Marshal(object)
		if marshalErr != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, marshalErr)
		}
		if unmarshalErr := json.Unmarshal(cleanRaw, &cfg); unmarshalErr != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, unmarshalErr)
		}
	}
	migrateLegacyConfigPaths(&cfg)
	if useEnvironment {
		applyEnvironment(&cfg)
	}
	if err := validateTunnelMapKeys(cfg.Tunnels); err != nil {
		return cfg, err
	}
	normalize(&cfg)
	return cfg, cfg.Validate(false)
}

func migrateLegacyConfigPaths(cfg *Config) {
	legacyProfileDir := filepath.Join(LegacyDataDir(), "profiles")
	legacyTunnelBin := filepath.Join(LegacyDataDir(), tunnelExecutable())
	if cfg.ProfileDir != "" && samePath(cfg.ProfileDir, legacyProfileDir) {
		cfg.ProfileDir = filepath.Join(AppDataDir(), "profiles")
	}
	if cfg.TunnelBin != "" && samePath(cfg.TunnelBin, legacyTunnelBin) {
		cfg.TunnelBin = filepath.Join(AppDataDir(), tunnelExecutable())
	}
}

func Save(cfg Config) error {
	return SaveFile(ConfigPath(), cfg)
}

func SaveFile(path string, cfg Config) error {
	if err := validateTunnelMapKeys(cfg.Tunnels); err != nil {
		return err
	}
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
	return atomicWrite(path, append(raw, '\n'), 0o600)
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
		{"maxProcesses", c.MaxProcesses}, {"maxConcurrentToolCalls", c.MaxConcurrentToolCalls},
		{"gitStatusCacheMs", c.GitStatusCacheMS},
		{"memory.timeoutMs", c.Memory.TimeoutMS}, {"memory.tokenBudget", c.Memory.TokenBudget},
		{"memory.queueSize", c.Memory.QueueSize}, {"memory.deliveryWorkers", c.Memory.DeliveryWorkers},
		{"memory.deliveryTimeoutMs", c.Memory.DeliveryTimeoutMS},
		{"memory.retryMaxAttempts", c.Memory.RetryMaxAttempts}, {"memory.retryBackoffMs", c.Memory.RetryBackoffMS},
		{"memory.healthCacheMs", c.Memory.HealthCacheMS},
	}
	for _, limit := range limits {
		if limit.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", limit.name)
		}
	}
	if c.Memory.DeliveryWorkers > 32 {
		return fmt.Errorf("memory.deliveryWorkers must not exceed 32")
	}
	if c.MaxConcurrentToolCalls > 1024 {
		return fmt.Errorf("maxConcurrentToolCalls must not exceed 1024")
	}
	if c.GitStatusCacheMS > 60_000 {
		return fmt.Errorf("gitStatusCacheMs must not exceed 60000")
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
	if err := validateTunnelMapKeys(c.Tunnels); err != nil {
		return err
	}
	if err := validateTunnelProfileName("profile", c.Profile); err != nil {
		return err
	}
	seenTunnelIDs := map[string]string{}
	seenTunnelProfiles := map[string]string{}
	for rawName, tunnel := range c.Tunnels {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if tunnel.Mode != "fast" && tunnel.Mode != "full" {
			return fmt.Errorf("tunnels.%s.mode must be fast or full", name)
		}
		if tunnel.RuntimeKeyEnv != "" && !envNamePattern.MatchString(tunnel.RuntimeKeyEnv) {
			return fmt.Errorf("tunnels.%s.runtimeKeyEnv is not a valid environment variable name", name)
		}
		if err := validateTunnelProfileName("tunnels."+name+".profile", tunnel.Profile); err != nil {
			return err
		}
		if tunnel.IsEnabled() && tunnel.TunnelID != "" {
			if previous := seenTunnelIDs[tunnel.TunnelID]; previous != "" {
				return fmt.Errorf("tunnels.%s.tunnelId duplicates tunnels.%s.tunnelId", name, previous)
			}
			seenTunnelIDs[tunnel.TunnelID] = name
		}
		profile := canonicalTunnelProfileName(tunnel.Profile)
		if tunnel.IsEnabled() && profile != "" {
			if previous := seenTunnelProfiles[profile]; previous != "" {
				return fmt.Errorf("tunnels.%s.profile duplicates tunnels.%s.profile", name, previous)
			}
			seenTunnelProfiles[profile] = name
		}
	}
	for _, group := range c.Tools.AllowedGroups {
		if !toolModulePattern.MatchString(group) {
			return fmt.Errorf("tools.allowedGroups value %q must be a valid module name", group)
		}
	}
	for _, name := range c.Tools.AllowedTools {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("tools.allowedTools must not contain empty names")
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

func validateTunnelMapKeys(tunnels map[string]TunnelConfig) error {
	seen := map[string]string{}
	for rawName := range tunnels {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if !tunnelNamePattern.MatchString(name) {
			return fmt.Errorf("tunnels name %q must match %s", rawName, tunnelNamePattern.String())
		}
		if previous := seen[name]; previous != "" {
			return fmt.Errorf("tunnel names %q and %q normalize to the same value %q", previous, rawName, name)
		}
		seen[name] = rawName
	}
	return nil
}

func canonicalTunnelProfileName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != "" && filepath.Ext(value) == "" {
		value += ".yaml"
	}
	return filepath.Clean(value)
}

func validateTunnelProfileName(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if filepath.Base(value) != value || value == "." || value == ".." {
		return fmt.Errorf("%s must be a file name inside profileDir", field)
	}
	ext := strings.ToLower(filepath.Ext(value))
	if ext != "" && ext != ".yaml" && ext != ".yml" {
		return fmt.Errorf("%s extension must be .yaml or .yml", field)
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

// IdentityInputs contains process-wide hashes that are shared by every
// workspace runtime. Construct it once per daemon reconciliation so large
// binaries and embedded assets are not re-read for every named workspace.
type IdentityInputs struct {
	BinaryHash            string
	WidgetHash            string
	RuntimeKeyFingerprint string
}

// NewIdentityInputs computes the immutable process inputs used by ConfigID.
// Secret values are reduced to one-way fingerprints and are never persisted.
func NewIdentityInputs(binaryPath string, widget []byte, runtimeKey string) IdentityInputs {
	binaryHash := "missing"
	if raw, err := os.ReadFile(binaryPath); err == nil {
		binaryHash = bytesFingerprint(raw)
	}
	widgetSum := sha256.Sum256(widget)
	return IdentityInputs{
		BinaryHash:            binaryHash,
		WidgetHash:            hex.EncodeToString(widgetSum[:]),
		RuntimeKeyFingerprint: secretFingerprint(runtimeKey),
	}
}

// ConfigID preserves the historical convenience API for callers that do not
// own tunnel credentials. Daemon lifecycle code should reuse IdentityInputs.
func (c Config) ConfigID(binaryPath string, widget []byte) string {
	return c.ConfigIDWithInputs(NewIdentityInputs(binaryPath, widget, ""))
}

// ConfigIDWithInputs fingerprints the complete effective configuration. Raw
// bearer, approval, memory, upstream, and tunnel credentials are replaced by
// one-way fingerprints before the identity material is serialized.
func (c Config) ConfigIDWithInputs(inputs IdentityInputs) string {
	identityConfig := c
	identityConfig.Workspace = filepath.Clean(identityConfig.Workspace)
	identityConfig.ExtraRoots = append([]string(nil), identityConfig.ExtraRoots...)
	for index := range identityConfig.ExtraRoots {
		identityConfig.ExtraRoots[index] = filepath.Clean(identityConfig.ExtraRoots[index])
	}
	if identityConfig.TunnelBin != "" {
		identityConfig.TunnelBin = filepath.Clean(identityConfig.TunnelBin)
	}
	if identityConfig.ProfileDir != "" {
		identityConfig.ProfileDir = filepath.Clean(identityConfig.ProfileDir)
	}

	authFingerprint := secretFingerprint(identityConfig.AuthToken)
	approvalFingerprint := secretFingerprint(identityConfig.ApprovalToken)
	identityConfig.AuthToken = ""
	identityConfig.ApprovalToken = ""
	memorySecretFingerprint := ""
	if identityConfig.Memory.SecretEnv != "" {
		memorySecretFingerprint = secretFingerprint(os.Getenv(identityConfig.Memory.SecretEnv))
	}

	material, _ := json.Marshal(map[string]any{
		"config":                      identityConfig,
		"authTokenFingerprint":        authFingerprint,
		"approvalTokenFingerprint":    approvalFingerprint,
		"memorySecretFingerprint":     memorySecretFingerprint,
		"mcpServerSecretFingerprints": MCPServerSecretFingerprints(identityConfig.MCPServers),
		"binaryHash":                  inputs.BinaryHash,
		"widgetHash":                  inputs.WidgetHash,
		"runtimeKeyFingerprint":       inputs.RuntimeKeyFingerprint,
	})
	sum := sha256.Sum256(material)
	return hex.EncodeToString(sum[:8])
}

func secretFingerprint(value string) string {
	if value == "" {
		return ""
	}
	return bytesFingerprint([]byte(value))
}

func bytesFingerprint(value []byte) string {
	sum := sha256.Sum256(value)
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

// EffectiveTunnels returns enabled and disabled named tunnel definitions in a
// stable order. The legacy single-tunnel fields remain a compatibility fallback
// until an explicit tunnels map is configured.
func (c Config) EffectiveTunnels() []NamedTunnel {
	if len(c.Tunnels) == 0 {
		if strings.TrimSpace(c.TunnelID) == "" {
			return nil
		}
		return []NamedTunnel{{
			Name: "default", Legacy: true,
			Config: TunnelConfig{
				TunnelID: strings.TrimSpace(c.TunnelID), Mode: "full", Profile: c.Profile,
				RuntimeKeyEnv: c.RuntimeKeyEnv, Organization: c.Organization,
			},
		}}
	}
	names := make([]string, 0, len(c.Tunnels))
	for name := range c.Tunnels {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]NamedTunnel, 0, len(names))
	for _, name := range names {
		out = append(out, NamedTunnel{Name: name, Config: c.Tunnels[name]})
	}
	return out
}

func (c Config) EnabledTunnels() []NamedTunnel {
	all := c.EffectiveTunnels()
	out := make([]NamedTunnel, 0, len(all))
	for _, tunnel := range all {
		if tunnel.Config.IsEnabled() {
			out = append(out, tunnel)
		}
	}
	return out
}

func normalizeTunnels(c *Config) {
	if len(c.Tunnels) == 0 {
		return
	}
	normalized := make(map[string]TunnelConfig, len(c.Tunnels))
	for rawName, tunnel := range c.Tunnels {
		name := strings.ToLower(strings.TrimSpace(rawName))
		tunnel.TunnelID = strings.TrimSpace(tunnel.TunnelID)
		tunnel.Mode = strings.ToLower(strings.TrimSpace(tunnel.Mode))
		if tunnel.Mode == "" {
			tunnel.Mode = "full"
		}
		tunnel.Profile = strings.TrimSpace(tunnel.Profile)
		if tunnel.Profile == "" {
			base := strings.TrimSuffix(strings.TrimSpace(c.Profile), filepath.Ext(c.Profile))
			if base == "" {
				base = "codebridge"
			}
			tunnel.Profile = base + "-" + name
		}
		tunnel.RuntimeKeyEnv = strings.TrimSpace(tunnel.RuntimeKeyEnv)
		if tunnel.RuntimeKeyEnv == "" {
			tunnel.RuntimeKeyEnv = c.RuntimeKeyEnv
		}
		tunnel.Organization = strings.TrimSpace(tunnel.Organization)
		if tunnel.Organization == "" {
			tunnel.Organization = c.Organization
		}
		normalized[name] = tunnel
	}
	c.Tunnels = normalized
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
	normalizeTunnels(c)
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
	if c.Memory.DeliveryWorkers == 0 {
		c.Memory.DeliveryWorkers = 4
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
	for index, name := range c.Tools.AllowedTools {
		c.Tools.AllowedTools[index] = strings.TrimSpace(name)
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
	if c.MaxConcurrentToolCalls == 0 {
		c.MaxConcurrentToolCalls = 16
	}
	if c.GitStatusCacheMS == 0 {
		c.GitStatusCacheMS = 2_000
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
	intEnv("CODEBRIDGE_MEMORY_DELIVERY_WORKERS", &c.Memory.DeliveryWorkers)
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
	intEnv("CODEBRIDGE_MAX_CONCURRENT_TOOL_CALLS", &c.MaxConcurrentToolCalls)
	intEnv("CODEBRIDGE_GIT_STATUS_CACHE_MS", &c.GitStatusCacheMS)
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
