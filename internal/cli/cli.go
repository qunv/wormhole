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
	"time"

	"codebridge/internal/agent"
	"codebridge/internal/assets"
	"codebridge/internal/config"
	"codebridge/internal/figma"
	"codebridge/internal/server"
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
	applyOptions(&cfg, opts)
	switch opts.Command {
	case "", "run", "here":
		cwd, _ := os.Getwd()
		cfg.Workspace = detectWorkspace(cwd)
		opts.Save = true
		return a.start(ctx, cfg, opts)
	case "serve", "__serve":
		return a.serve(ctx, cfg)
	case "start":
		return a.start(ctx, cfg, opts)
	case "restart":
		_ = a.stop(cfg, opts)
		return a.start(ctx, cfg, opts)
	case "stop":
		return a.stop(cfg, opts)
	case "status":
		return a.status(cfg, opts)
	case "doctor":
		return a.doctor(ctx, cfg, opts)
	case "workspace":
		return a.workspace(cfg, opts)
	case "setup", "init":
		return a.setup(cfg, opts)
	case "url":
		fmt.Fprintf(a.Stdout, "http://127.0.0.1:%d/mcp\n", cfg.Port)
		return nil
	case "logs":
		return a.logs()
	case "profile":
		path, err := writeTunnelProfile(cfg)
		if err == nil {
			fmt.Fprintln(a.Stdout, path)
		}
		return err
	case "figma":
		return a.figma(ctx, cfg, opts)
	case "database", "db":
		return a.databaseCommand(ctx, cfg, opts)
	case "tunnel":
		return a.tunnelCommand(ctx, cfg, opts)
	case "keys":
		fmt.Fprintln(a.Stdout, "https://platform.openai.com/settings/organization/tunnels")
		fmt.Fprintln(a.Stdout, "https://platform.openai.com/settings/organization/api-keys")
		return nil
	case "config":
		return a.configCommand(cfg, opts)
	case "key":
		return a.keyCommand(opts)
	case "skills":
		return a.skillsCommand(ctx, cfg, opts)
	case "install-cli", "cli":
		return a.installCLI()
	default:
		return fmt.Errorf("unknown command %q; run %s help", opts.Command, strings.ToLower(a.Name))
	}
}

func (a App) serve(ctx context.Context, cfg config.Config) error {
	if err := cfg.Validate(true); err != nil {
		return err
	}
	executable, _ := os.Executable()
	runtime, err := agent.New(cfg, a.Version, a.Tier, cfg.ConfigID(executable, assets.Widget()))
	if err != nil {
		return err
	}
	defer runtime.Close()
	return server.New(runtime).ListenAndServe(ctx)
}

func (a App) usage() {
	fmt.Fprintf(a.Stdout, `%s — native Go CLI and local MCP coding agent

Usage:
  codebridge                    Start/repoint in the current git workspace
  codebridge setup              Configure local defaults
  codebridge start [options]    Start server and optional tunnel
  codebridge stop|restart       Manage background processes
  codebridge status [--json]    Show health and PID state
  codebridge doctor [--json]    Check local readiness
  codebridge workspace [path]   Show or set the default workspace
  codebridge figma [status|tools]
  codebridge database add|list|test|remove|doctor
  codebridge tunnel [status|install]
  codebridge keys                Print Tunnel/API-key setup URLs
  codebridge profile            Write the tunnel-client YAML profile
  codebridge logs               Print launcher log
  codebridge config get|set|path
  codebridge key set|delete     Manage the runtime key in the local env file
  codebridge skills [list|read]
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
	opts := options{RuntimeEnv: "CONTROL_PLANE_API_KEY"}
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

func (a App) keyCommand(opts options) error {
	sub := ""
	if len(opts.Rest) > 0 {
		sub = opts.Rest[0]
	}
	path := config.DotEnvPath()
	switch sub {
	case "set":
		value := opts.RuntimeKey
		if value == "" && len(opts.Rest) > 1 {
			value = opts.Rest[1]
		}
		if value == "" {
			fmt.Fprint(a.Stdout, "Runtime API key: ")
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
		if err := os.WriteFile(path, []byte(config.MergeDotEnv(string(existing), map[string]string{"CONTROL_PLANE_API_KEY": value})), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "Saved %s\n", path)
	case "delete":
		existing, _ := os.ReadFile(path)
		values := config.ParseDotEnv(string(existing))
		delete(values, "CONTROL_PLANE_API_KEY")
		var lines []string
		for key, value := range values {
			lines = append(lines, key+"="+value)
		}
		return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	default:
		return errors.New("usage: codebridge key set [value]|delete")
	}
	return nil
}

func (a App) figma(ctx context.Context, cfg config.Config, opts options) error {
	client := figma.Client{
		Endpoint: cfg.FigmaDesktopURL, Timeout: timeDurationMS(cfg.FigmaDesktopTimeoutMS),
		AllowRemote: cfg.FigmaDesktopAllowRemote, Version: a.Version,
	}
	sub := "status"
	if len(opts.Rest) > 0 {
		sub = opts.Rest[0]
	}
	var value any
	switch sub {
	case "status":
		value = client.Status(ctx)
	case "tools":
		tools, err := client.ListTools(ctx)
		if err != nil {
			return err
		}
		value = map[string]any{"count": len(tools), "tools": tools}
	default:
		return errors.New("usage: codebridge figma status|tools")
	}
	raw, _ := json.MarshalIndent(value, "", "  ")
	fmt.Fprintln(a.Stdout, string(raw))
	return nil
}

func (a App) skillsCommand(ctx context.Context, cfg config.Config, opts options) error {
	executable, _ := os.Executable()
	runtime, err := agent.New(cfg, a.Version, a.Tier, cfg.ConfigID(executable, assets.Widget()))
	if err != nil {
		return err
	}
	defer runtime.Close()
	sub := "list"
	if len(opts.Rest) > 0 {
		sub = opts.Rest[0]
	}
	args := map[string]any{}
	name := "list_skills"
	if sub == "read" {
		if len(opts.Rest) < 2 {
			return errors.New("usage: codebridge skills read <name>")
		}
		name, args = "read_skill", map[string]any{"name": opts.Rest[1]}
	}
	value, err := runtime.Handle(ctx, name, args)
	if err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(value, "", "  ")
	fmt.Fprintln(a.Stdout, string(raw))
	return nil
}

func timeDurationMS(value int) time.Duration { return time.Duration(value) * time.Millisecond }

func ternary[T any](condition bool, yes, no T) T {
	if condition {
		return yes
	}
	return no
}
