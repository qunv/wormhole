// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"codebridge/internal/adminauth"
	"codebridge/internal/agent"
	"codebridge/internal/assets"
	"codebridge/internal/config"
	"codebridge/internal/server"
	"codebridge/internal/workspaceregistry"

	"golang.org/x/term"
)

type App struct {
	Name    string
	Version string
	Tier    string
	Stdout  io.Writer
	Stderr  io.Writer
	Stdin   io.Reader
}

type options struct {
	Command      string
	Rest         []string
	Workspace    string
	ExtraRoots   []string
	Mode         string
	Policy       string
	Host         string
	Port         int
	AuthToken    string
	Background   bool
	NoTunnel     bool
	TunnelBin    string
	TunnelID     string
	Organization string
	Profile      string
	ProfileDir   string
	RuntimeKey   string
	RuntimeEnv   string
	Save         bool
	JSON         bool
	Force        bool
	DryRun       bool
}

func (a App) Run(ctx context.Context, argv []string) error {
	if a.Stdout == nil {
		a.Stdout = os.Stdout
	}
	if a.Stderr == nil {
		a.Stderr = os.Stderr
	}
	if a.Stdin == nil {
		a.Stdin = os.Stdin
	}
	if len(argv) > 0 && argv[0] == "__child" {
		return a.runLoggedChild(ctx, argv[1:])
	}
	if err := config.MigrateLegacyLayout(); err != nil {
		return fmt.Errorf("migrate legacy Codebridge layout: %w", err)
	}
	_ = config.LoadDotEnv(config.DotEnvPath(), false)
	opts, err := parse(argv)
	if err != nil {
		return err
	}
	if runsInBackgroundByDefault(opts.Command) {
		opts.Background = true
	}
	if opts.Command == "help" || opts.Command == "--help" || opts.Command == "-h" {
		a.usage()
		return nil
	}
	if opts.Command == "version" || opts.Command == "--version" {
		fmt.Fprintf(a.Stdout, "%s %s (%s)\n", a.Name, a.Version, a.Tier)
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	defaultConfig := cfg
	applyOptions(&cfg, opts)
	switch opts.Command {
	case "", "run", "here":
		cwd, _ := os.Getwd()
		if opts.Workspace != "" {
			cfg.Workspace = detectWorkspace(cfg.Workspace)
			opts.Save = true
			return a.start(ctx, cfg, opts)
		}
		if root, isGit := detectGitWorkspace(cwd); isGit {
			entry, created, enabled, ensureErr := a.ensureAutoWorkspace(cfg, root, opts)
			if ensureErr != nil {
				return ensureErr
			}
			a.printAutoWorkspace(entry, created, enabled, cfg.Port)
			opts.Save = true
			return a.start(ctx, cfg, opts)
		}
		// For non-Git directories, a bare invocation uses the current folder.
		cfg.Workspace = detectWorkspace(cwd)
		opts.Save = true
		return a.start(ctx, cfg, opts)
	case "serve", "__serve":
		return a.serve(ctx, cfg)
	case "start":
		return a.start(ctx, cfg, opts)
	case "__admin-restart-helper":
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(900 * time.Millisecond):
		}
		stopConfig := cfg
		if oldPort, parseErr := strconv.Atoi(strings.TrimSpace(os.Getenv("CODEBRIDGE_ADMIN_OLD_PORT"))); parseErr == nil && oldPort > 0 && oldPort <= 65535 {
			stopConfig.Port = oldPort
		}
		opts.Background = true
		return withLifecycleLock(ctx, "admin-restart", func() error {
			if err := a.stopUnlocked(stopConfig, opts); err != nil {
				return err
			}
			latest, err := config.Load()
			if err != nil {
				return err
			}
			return a.startUnlocked(ctx, latest, opts)
		})
	case "restart":
		if opts.Workspace == "" {
			cwd, _ := os.Getwd()
			if root, isGit := detectGitWorkspace(cwd); isGit {
				entry, created, enabled, ensureErr := a.ensureAutoWorkspace(cfg, root, opts)
				if ensureErr != nil {
					return ensureErr
				}
				a.printAutoWorkspace(entry, created, enabled, cfg.Port)
			}
		}
		return withLifecycleLock(ctx, "restart", func() error {
			if err := a.stopUnlocked(cfg, opts); err != nil {
				return err
			}
			return a.startUnlocked(ctx, cfg, opts)
		})
	case "stop":
		return a.stop(cfg, opts)
	case "status":
		return a.status(cfg, opts)
	case "doctor":
		return a.doctor(ctx, cfg, opts)
	case "state":
		return a.stateCommand(cfg, opts)
	case "workspace":
		return a.workspaceCommand(ctx, defaultConfig, opts)
	case "setup", "init":
		return a.setup(cfg, opts)
	case "url":
		fmt.Fprintf(a.Stdout, "http://127.0.0.1:%d/mcp\n", cfg.Port)
		return nil
	case "logs":
		return a.logs(cfg)
	case "profile":
		paths, err := writeTunnelProfiles(cfg)
		if err == nil {
			for _, path := range paths {
				fmt.Fprintln(a.Stdout, path)
			}
		}
		return err
	case "tunnel":
		return a.tunnelCommand(ctx, cfg, opts)
	case "admin":
		return a.adminCommand(cfg, opts)
	case "ui":
		fmt.Fprintf(a.Stdout, "http://127.0.0.1:%d/admin/\n", cfg.Port)
		return nil
	case "keys":
		fmt.Fprintln(a.Stdout, "https://platform.openai.com/settings/organization/tunnels")
		fmt.Fprintln(a.Stdout, "https://platform.openai.com/settings/organization/api-keys")
		return nil
	case "config":
		return a.configCommand(cfg, opts)
	case "key":
		return a.keyCommand(opts)
	case "install-cli", "cli":
		return a.installCLI()
	default:
		return fmt.Errorf("unknown command %q; run %s help", opts.Command, strings.ToLower(a.Name))
	}
}

func serveConfigID(cfg config.Config, inputs config.IdentityInputs) (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CODEBRIDGE_DAEMON_CONFIG_ID")); configured != "" {
		return configured, nil
	}
	return daemonConfigIDWithInputs(cfg, inputs)
}

func (a App) serve(ctx context.Context, cfg config.Config) error {
	if err := cfg.Validate(true); err != nil {
		return err
	}
	executable, _ := os.Executable()
	widget := assets.Widget()
	identityInputs := config.NewIdentityInputs(executable, widget, runtimeKeyIdentityMaterial(cfg, ""))
	if fingerprint := strings.TrimSpace(os.Getenv("CODEBRIDGE_RUNTIME_KEY_FINGERPRINT")); fingerprint != "" {
		identityInputs.RuntimeKeyFingerprint = fingerprint
	}
	configID, err := serveConfigID(cfg, identityInputs)
	if err != nil {
		return err
	}
	var reporterMu sync.Mutex
	reporter := func(stage, message string) {
		reporterMu.Lock()
		defer reporterMu.Unlock()
		fmt.Fprintf(a.Stdout, "[startup] %-9s %s\n", stage, message)
	}
	reporter("boot", fmt.Sprintf("Codebridge %s pid=%d", a.Version, os.Getpid()))
	if readHealth(cfg.Port) == nil {
		a.startupStateGC(reporter)
	} else {
		reporter("state", "gc skipped because another daemon is active")
	}

	namedConfigs, err := loadNamedWorkspaceConfigs(cfg)
	if err != nil {
		reporter("failed", err.Error())
		return err
	}
	primaryID := workspaceregistry.IDFromPath(cfg.Workspace)
	type runtimeStartup struct {
		id       string
		dataDir  string
		config   config.Config
		configID string
		primary  bool
	}
	startups := []runtimeStartup{{
		id: primaryID, config: cfg, configID: configID, primary: true,
	}}
	for _, item := range namedConfigs {
		id := item.Registration.ID
		if id == primaryID {
			if sameWorkspacePath(item.Config.Workspace, cfg.Workspace) {
				continue
			}
			err := fmt.Errorf("workspace id %q conflicts with the primary workspace %s", id, cfg.Workspace)
			reporter("failed", err.Error())
			return err
		}
		startups = append(startups, runtimeStartup{
			id: id, dataDir: item.Registration.DataDir, config: item.Config,
			configID: item.Config.ConfigIDWithInputs(identityInputs),
		})
	}

	shared := agent.NewSharedServices(a.Version)
	type runtimeStartupResult struct {
		runtime *agent.Runtime
		err     error
	}
	results := make([]runtimeStartupResult, len(startups))
	var startupWG sync.WaitGroup
	for index, startup := range startups {
		startupWG.Add(1)
		go func(index int, startup runtimeStartup) {
			defer startupWG.Done()
			workspaceReporter := reporter
			if !startup.primary {
				workspaceReporter = func(stage, message string) {
					reporter(stage, "workspace="+startup.id+" "+message)
				}
			}
			results[index].runtime, results[index].err = agent.NewWorkspaceContextWithSharedServices(
				ctx, startup.id, startup.dataDir, startup.config,
				a.Version, a.Tier, startup.configID, shared, workspaceReporter,
			)
		}(index, startup)
	}
	startupWG.Wait()

	var startupErr error
	for index, result := range results {
		if result.err == nil || startupErr != nil {
			continue
		}
		startup := startups[index]
		startupErr = result.err
		if startup.primary {
			reporter("failed", result.err.Error())
		} else {
			reporter("failed", fmt.Sprintf("workspace=%s %v", startup.id, result.err))
		}
	}
	if startupErr != nil {
		for _, result := range results {
			if result.runtime != nil {
				result.runtime.Close()
			}
		}
		_ = shared.Close()
		return startupErr
	}

	primaryRuntime := results[0].runtime
	namedRuntimes := make(map[string]*agent.Runtime, len(results)-1)
	for index := 1; index < len(results); index++ {
		namedRuntimes[startups[index].id] = results[index].runtime
	}
	defer func() {
		for index := len(results) - 1; index >= 0; index-- {
			results[index].runtime.Close()
		}
		_ = shared.Close()
	}()
	reporter("server", fmt.Sprintf("opening http://%s:%d with %d workspace endpoint(s)", cfg.Host, cfg.Port, len(results)))
	return server.NewMulti(primaryRuntime, namedRuntimes).ListenAndServe(ctx)
}

func (a App) usage() {
	fmt.Fprintf(a.Stdout, `%s — native Go CLI and local MCP coding agent

Usage:
  codebridge                    Start and auto-register the current Git repo
  codebridge setup              Configure local defaults
  codebridge start [options]    Start server and optional tunnel
  codebridge stop|restart       Manage background processes
  codebridge status [--json]    Show health and PID state
  codebridge doctor [--json]    Check local readiness
  codebridge state gc [--dry-run] [--json]
  codebridge workspace [path]   Show or set the primary workspace
  codebridge workspace add <id> <path> [--extra-root <path>] [--force]
  codebridge workspace list [--json]
  codebridge workspace start|stop|status <id>
  codebridge workspace compact <id> [--dry-run] [--json]
  codebridge workspace remove <id> [--force]
  codebridge tunnel [status|list|install]
  codebridge admin               Print the local Admin UI URL
  codebridge admin set-password [username]
  codebridge admin status        Show local Admin account status
  codebridge keys                Print Tunnel/API-key setup URLs
  codebridge profile            Write all enabled tunnel-client YAML profiles
  codebridge logs               Print bounded server and tunnel logs
  codebridge config get|set|path
  codebridge key set|delete     Manage the runtime key in the local env file
  codebridge install-cli        Install this binary in the user bin directory
  codebridge serve              Run the MCP server in the foreground

Options:
  --workspace <path>      Workspace root
  --extra-root <path>     Additional allowed root; repeatable
  --mode safe|full
  --policy strict|balanced|full
  --host <host>           Default 127.0.0.1
  --port <port>           Default 8789
  --auth-token <token>    Runtime-only MCP bearer token
  --background            Run detached
  --no-tunnel             Start only the local MCP server
  --tunnel-bin <path>
  --tunnel-id <id>
  --runtime-key-env <name>
  --runtime-key <key>     Runtime-only; never saved to config.json
  --save                  Persist non-secret options
  --dry-run               Report state cleanup without deleting
  --json
`, a.Name)
}

func runsInBackgroundByDefault(command string) bool {
	switch command {
	case "", "run", "here", "restart":
		return true
	default:
		return false
	}
}

func parse(argv []string) (options, error) {
	opts := options{}
	index := 0
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") {
		opts.Command = argv[0]
		index = 1
	}
	next := func(flag string) (string, error) {
		if index >= len(argv) {
			return "", fmt.Errorf("missing value for %s", flag)
		}
		value := argv[index]
		index++
		return value, nil
	}
	for index < len(argv) {
		arg := argv[index]
		index++
		switch arg {
		case "--help", "-h":
			opts.Command = "help"
		case "--version":
			opts.Command = "version"
		case "--workspace":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.Workspace = value
		case "--extra-root", "--extra-roots":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.ExtraRoots = append(opts.ExtraRoots, filepath.SplitList(value)...)
		case "--mode":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.Mode = value
		case "--policy":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.Policy = value
		case "--host":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.Host = value
		case "--port":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.Port, err = strconv.Atoi(value)
			if err != nil {
				return opts, errors.New("port must be a number")
			}
		case "--auth-token":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.AuthToken = value
		case "--background", "--daemon":
			opts.Background = true
		case "--no-tunnel":
			opts.NoTunnel = true
		case "--tunnel-bin":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.TunnelBin = value
		case "--tunnel-id":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.TunnelID = value
		case "--organization-id":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.Organization = value
		case "--profile":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.Profile = value
		case "--profile-dir":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.ProfileDir = value
		case "--runtime-key":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.RuntimeKey = value
		case "--runtime-key-env":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.RuntimeEnv = value
		case "--save":
			opts.Save = true
		case "--json":
			opts.JSON = true
		case "--force":
			opts.Force = true
		case "--dry-run":
			opts.DryRun = true
		default:
			opts.Rest = append(opts.Rest, arg)
		}
	}
	return opts, nil
}

func applyOptions(cfg *config.Config, opts options) {
	if opts.Workspace != "" {
		cfg.Workspace = opts.Workspace
	}
	if len(opts.ExtraRoots) > 0 {
		cfg.ExtraRoots = opts.ExtraRoots
	}
	if opts.Mode != "" {
		cfg.Mode = opts.Mode
	}
	if opts.Policy != "" {
		cfg.Policy = opts.Policy
	}
	if opts.Host != "" {
		cfg.Host = opts.Host
	}
	if opts.Port != 0 {
		cfg.Port = opts.Port
	}
	if opts.AuthToken != "" {
		cfg.AuthToken = opts.AuthToken
	}
	if opts.NoTunnel {
		cfg.NoTunnel = true
	}
	if opts.TunnelBin != "" {
		cfg.TunnelBin = opts.TunnelBin
	}
	if opts.TunnelID != "" {
		cfg.TunnelID = opts.TunnelID
	}
	if opts.Organization != "" {
		cfg.Organization = opts.Organization
	}
	if opts.Profile != "" {
		cfg.Profile = opts.Profile
	}
	if opts.ProfileDir != "" {
		cfg.ProfileDir = opts.ProfileDir
	}
	if opts.RuntimeEnv != "" {
		cfg.RuntimeKeyEnv = opts.RuntimeEnv
	}
}

func (a App) workspace(cfg config.Config, opts options) error {
	if len(opts.Rest) == 0 {
		fmt.Fprintln(a.Stdout, cfg.Workspace)
		return nil
	}
	root, err := filepath.Abs(opts.Rest[0])
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("workspace does not exist: %s", root)
	}
	cfg.Workspace = root
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Workspace: %s\n", root)
	return nil
}

func (a App) configCommand(cfg config.Config, opts options) error {
	sub := "path"
	if len(opts.Rest) > 0 {
		sub = opts.Rest[0]
	}
	switch sub {
	case "path":
		fmt.Fprintln(a.Stdout, config.ConfigPath())
	case "get":
		raw, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Fprintln(a.Stdout, string(raw))
	case "set":
		if len(opts.Rest) < 3 {
			return errors.New("usage: codebridge config set <key> <value>")
		}
		key, value := opts.Rest[1], opts.Rest[2]
		switch key {
		case "workspace":
			cfg.Workspace = value
		case "mode":
			cfg.Mode = value
		case "policy":
			cfg.Policy = value
		case "port":
			number, err := strconv.Atoi(value)
			if err != nil {
				return err
			}
			cfg.Port = number
		case "noTunnel":
			cfg.NoTunnel = value == "true" || value == "1"
		case "tunnelId":
			cfg.TunnelID = value
		case "tunnelBin":
			cfg.TunnelBin = value
		default:
			return fmt.Errorf("unsupported config key: %s", key)
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "Updated %s\n", key)
	default:
		return errors.New("usage: codebridge config get|set|path")
	}
	return nil
}

func (a App) adminCommand(cfg config.Config, opts options) error {
	sub := "url"
	if len(opts.Rest) > 0 {
		sub = opts.Rest[0]
	}
	switch sub {
	case "url":
		fmt.Fprintf(a.Stdout, "http://127.0.0.1:%d/admin/\n", cfg.Port)
		return nil
	case "status":
		credential, err := adminauth.LoadCredentials(config.AdminAuthPath())
		if errors.Is(err, adminauth.ErrNotConfigured) {
			fmt.Fprintf(a.Stdout, "Admin account: not configured\nCredential file: %s\n", config.AdminAuthPath())
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "Admin account: configured\nUsername: %s\nCredential file: %s\n", credential.Username, config.AdminAuthPath())
		return nil
	case "set-password", "reset-password":
		username := "admin"
		if credential, err := adminauth.LoadCredentials(config.AdminAuthPath()); err == nil {
			username = credential.Username
		} else if !errors.Is(err, adminauth.ErrNotConfigured) {
			return err
		}
		if len(opts.Rest) > 1 {
			username = opts.Rest[1]
		}
		reader := bufio.NewReader(a.Stdin)
		password, err := a.readAdminPassword(reader, "New admin password: ")
		if err != nil {
			return err
		}
		confirmation, err := a.readAdminPassword(reader, "Confirm admin password: ")
		if err != nil {
			return err
		}
		if password != confirmation {
			return errors.New("admin password confirmation does not match")
		}
		credential, err := adminauth.SetCredentials(config.AdminAuthPath(), username, password)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "Admin account %s updated. Existing browser sessions are invalidated.\nSaved %s\n", credential.Username, config.AdminAuthPath())
		return nil
	default:
		return errors.New("usage: codebridge admin [url|status|set-password [username]|reset-password [username]]")
	}
}

func (a App) readAdminPassword(reader *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(a.Stdout, prompt)
	if file, ok := a.Stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		raw, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(a.Stdout)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	fmt.Fprintln(a.Stdout)
	return strings.TrimRight(line, "\r\n"), nil
}

func (a App) keyCommand(opts options) error {
	sub := ""
	if len(opts.Rest) > 0 {
		sub = opts.Rest[0]
	}
	path := config.DotEnvPath()
	target := strings.TrimSpace(opts.RuntimeEnv)
	if target == "" {
		target = "CONTROL_PLANE_API_KEY"
	}
	switch sub {
	case "set":
		value := opts.RuntimeKey
		if value == "" && len(opts.Rest) > 1 {
			value = opts.Rest[1]
		}
		if value == "" {
			fmt.Fprintf(a.Stdout, "%s: ", target)
			line, _ := bufio.NewReader(a.Stdin).ReadString('\n')
			value = strings.TrimSpace(line)
		}
		if value == "" {
			return errors.New("runtime key is required")
		}
		existing, _ := os.ReadFile(path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := atomicWriteFile(path, []byte(config.MergeDotEnv(string(existing), map[string]string{target: value})), 0o600); err != nil {
			return err
		}
		_ = os.Setenv(target, value)
		fmt.Fprintf(a.Stdout, "Saved %s in %s\n", target, path)
	case "delete":
		existing, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		cleaned := config.RemoveDotEnvKeys(string(existing), target)
		_ = os.Unsetenv(target)
		if cleaned == "" {
			return os.Remove(path)
		}
		return atomicWriteFile(path, []byte(cleaned), 0o600)
	default:
		return errors.New("usage: codebridge key set [value]|delete [--runtime-key-env NAME]")
	}
	return nil
}

func ternary[T any](condition bool, yes, no T) T {
	if condition {
		return yes
	}
	return no
}
