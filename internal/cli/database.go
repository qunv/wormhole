// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"codebridge/internal/config"
	databasefactory "codebridge/internal/database/factory"
)

type databaseAddOptions struct {
	Alias              string
	Driver             string
	Environment        string
	CredentialProvider string
	CredentialName     string
	CredentialEnv      string
	FileRoot           string
	AllowedSchemas     []string
	DeniedTables       []string
	MaskColumns        []string
	Required           bool
	NoPrompt           bool
}

func (a App) databaseCommand(ctx context.Context, cfg config.Config, opts options) error {
	sub := "list"
	if len(opts.Rest) > 0 {
		sub = opts.Rest[0]
	}
	switch sub {
	case "list":
		return a.databaseList(cfg, opts.JSON)
	case "add":
		parsed, err := parseDatabaseAddOptions(opts.Rest[1:])
		if err != nil {
			return err
		}
		return a.databaseAdd(cfg, opts, parsed)
	case "test":
		if len(opts.Rest) < 2 {
			return errors.New("usage: codebridge database test <alias> [--json]")
		}
		return a.databaseTest(ctx, cfg, opts.Rest[1], opts.JSON)
	case "doctor":
		return a.databaseDoctor(ctx, cfg, opts.JSON)
	case "remove":
		if len(opts.Rest) < 2 {
			return errors.New("usage: codebridge database remove <alias>")
		}
		return a.databaseRemove(cfg, opts.Rest[1])
	default:
		return errors.New("usage: codebridge database add|list|test|remove|doctor")
	}
}

func (a App) databaseList(cfg config.Config, asJSON bool) error {
	aliases := make([]string, 0, len(cfg.Database.Connections))
	for alias := range cfg.Database.Connections {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	items := make([]map[string]any, 0, len(aliases))
	for _, alias := range aliases {
		connection := cfg.Database.Connections[alias]
		items = append(items, safeDatabaseConfig(alias, connection))
	}
	value := map[string]any{"enabled": cfg.Database.Enabled, "connections": items}
	if asJSON {
		return printJSON(a.Stdout, value)
	}
	if len(items) == 0 {
		fmt.Fprintln(a.Stdout, "No database connections configured.")
		return nil
	}
	for _, item := range items {
		fmt.Fprintf(a.Stdout, "%s  driver=%s environment=%s access=%s credential=%s:%s\n",
			item["alias"], item["driver"], item["environment"], item["access"],
			item["credential_provider"], item["credential_name"])
	}
	return nil
}

func (a App) databaseAdd(cfg config.Config, global options, add databaseAddOptions) error {
	if strings.TrimSpace(add.Alias) == "" {
		return errors.New("usage: codebridge database add <alias> [--driver postgres|mysql|sqlite] [--environment dev] [--credential-env NAME | --credential-file PATH] [--file-root PATH]")
	}
	if _, exists := cfg.Database.Connections[add.Alias]; exists && !global.Force {
		return fmt.Errorf("database alias %q already exists; pass --force to replace it", add.Alias)
	}
	reader := bufio.NewReader(a.Stdin)
	prompt := func(label, current string) string {
		if add.NoPrompt {
			return current
		}
		fmt.Fprintf(a.Stdout, "%s [%s]: ", label, current)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, os.ErrClosed) {
			return current
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return current
		}
		return line
	}

	if add.Driver == "" {
		add.Driver = prompt("Driver", "postgres")
	}
	if add.Environment == "" {
		add.Environment = prompt("Environment", "dev")
	}
	if add.CredentialProvider == "" {
		add.CredentialProvider = "env"
	}
	if add.CredentialName == "" && add.CredentialEnv != "" {
		add.CredentialName = add.CredentialEnv
	}
	if add.CredentialName == "" {
		if add.CredentialProvider == "env" {
			add.CredentialName = prompt("Credential environment variable", credentialEnvForAlias(add.Alias))
		} else {
			add.CredentialName = prompt("Credential reference name/path", "")
		}
	}
	if strings.EqualFold(add.Driver, "sqlite") && add.FileRoot == "" {
		add.FileRoot = prompt("SQLite file root", cfg.Workspace)
	}
	if len(add.AllowedSchemas) == 0 && !add.NoPrompt {
		defaults := "public"
		switch strings.ToLower(strings.TrimSpace(add.Driver)) {
		case "mysql":
			defaults = ""
		case "sqlite":
			defaults = "main"
		}
		add.AllowedSchemas = splitCSV(prompt("Allowed schemas (comma separated)", defaults))
	}
	if strings.EqualFold(add.Driver, "sqlite") && len(add.AllowedSchemas) == 0 {
		add.AllowedSchemas = []string{"main"}
	}

	connection := config.DatabaseConnectionConfig{
		Driver: strings.ToLower(strings.TrimSpace(add.Driver)), Environment: strings.ToLower(strings.TrimSpace(add.Environment)),
		CredentialRef: config.CredentialReference{
			Provider: strings.ToLower(strings.TrimSpace(add.CredentialProvider)),
			Name:     strings.TrimSpace(add.CredentialName),
		},
		FileRoot: strings.TrimSpace(add.FileRoot),
		Required: add.Required,
		Access: config.DatabaseAccessConfig{
			Mode: "read-only", AllowedSchemas: add.AllowedSchemas,
			DeniedTables: add.DeniedTables, MaskColumns: add.MaskColumns,
		},
		Limits: config.DatabaseLimitsConfig{
			QueryTimeoutMS: 10_000, MaxRows: 500, MaxResultBytes: 1 << 20,
			MaxCellBytes: 64 << 10, MaxConcurrentQueries: 4,
		},
		Pool: config.DatabasePoolConfig{MaxOpen: 5, MaxIdle: 2, MaxLifetimeSeconds: 1800},
	}
	if cfg.Database.Connections == nil {
		cfg.Database.Connections = map[string]config.DatabaseConnectionConfig{}
	}
	cfg.Database.Enabled = true
	cfg.Database.Connections[add.Alias] = connection
	if err := config.Save(cfg); err != nil {
		return err
	}

	secret := ""
	if connection.CredentialRef.Provider == "env" && os.Getenv(connection.CredentialRef.Name) == "" && !add.NoPrompt {
		fmt.Fprintf(a.Stdout, "Database credential/DSN for %s (Enter leaves env unchanged): ", connection.CredentialRef.Name)
		value, err := readSecretInput(reader, a.Stdin, a.Stdout)
		if err != nil {
			return fmt.Errorf("read database credential: %w", err)
		}
		secret = value
	}
	if secret != "" {
		if err := saveDatabaseCredential(connection.CredentialRef.Name, secret); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.Stdout, "Saved database alias %s (%s/%s).\n", add.Alias, connection.Driver, connection.Environment)
	fmt.Fprintf(a.Stdout, "Credential reference: %s:%s\n", connection.CredentialRef.Provider, connection.CredentialRef.Name)
	return nil
}

func (a App) databaseRemove(cfg config.Config, alias string) error {
	connection, exists := cfg.Database.Connections[alias]
	if !exists {
		return fmt.Errorf("unknown database alias %q", alias)
	}
	delete(cfg.Database.Connections, alias)
	if len(cfg.Database.Connections) == 0 {
		cfg.Database.Enabled = false
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	if connection.CredentialRef.Provider == "env" && connection.CredentialRef.Name != "" {
		path := config.DotEnvPath()
		raw, err := os.ReadFile(path)
		if err == nil {
			cleaned := config.RemoveDotEnvKeys(string(raw), connection.CredentialRef.Name)
			if cleaned == "" {
				_ = os.Remove(path)
			} else if err := os.WriteFile(path, []byte(cleaned), 0o600); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(a.Stdout, "Removed database alias %s.\n", alias)
	return nil
}

func (a App) databaseTest(ctx context.Context, cfg config.Config, alias string, asJSON bool) error {
	connection, exists := cfg.Database.Connections[alias]
	if !exists {
		return fmt.Errorf("unknown database alias %q", alias)
	}
	only := config.DatabaseConfig{Enabled: true, Connections: map[string]config.DatabaseConnectionConfig{alias: connection}}
	started := time.Now()
	manager, err := databasefactory.New(only)
	if err != nil {
		return fmt.Errorf("initialize database alias %q: %w", alias, err)
	}
	defer manager.Close()
	results := manager.List(ctx, true)
	if len(results) != 1 {
		return fmt.Errorf("database alias %q did not produce a health result", alias)
	}
	result := results[0]
	value := map[string]any{
		"alias": result.Alias, "driver": result.Driver, "environment": result.Environment,
		"access": result.Access, "available": result.Available,
		"latency_ms": time.Since(started).Milliseconds(),
	}
	if result.Error != "" {
		value["error"] = result.Error
	}
	if asJSON {
		if err := printJSON(a.Stdout, value); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(a.Stdout, "%s  driver=%s environment=%s available=%t latency_ms=%d\n",
			result.Alias, result.Driver, result.Environment, result.Available, value["latency_ms"])
		if result.Error != "" {
			fmt.Fprintf(a.Stdout, "error: %s\n", result.Error)
		}
	}
	if !result.Available {
		return fmt.Errorf("database alias %q is unavailable", alias)
	}
	return nil
}

func (a App) databaseDoctor(ctx context.Context, cfg config.Config, asJSON bool) error {
	if !cfg.Database.Enabled || len(cfg.Database.Connections) == 0 {
		return a.databaseList(cfg, asJSON)
	}
	manager, err := databasefactory.New(cfg.Database)
	if err != nil {
		return err
	}
	defer manager.Close()
	results := manager.List(ctx, true)
	if asJSON {
		return printJSON(a.Stdout, map[string]any{"connections": results})
	}
	for _, result := range results {
		fmt.Fprintf(a.Stdout, "%s %-24s driver=%s environment=%s",
			ternary(result.Available, "OK  ", "WARN"), result.Alias, result.Driver, result.Environment)
		if result.Error != "" {
			fmt.Fprintf(a.Stdout, " error=%s", result.Error)
		}
		fmt.Fprintln(a.Stdout)
	}
	return nil
}

func parseDatabaseAddOptions(args []string) (databaseAddOptions, error) {
	var result databaseAddOptions
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		result.Alias = strings.TrimSpace(args[0])
		args = args[1:]
	}
	next := func(index *int, name string) (string, error) {
		if *index >= len(args) {
			return "", fmt.Errorf("missing value for %s", name)
		}
		value := args[*index]
		*index++
		return value, nil
	}
	for index := 0; index < len(args); {
		name := args[index]
		index++
		switch name {
		case "--driver":
			value, err := next(&index, name)
			if err != nil {
				return result, err
			}
			result.Driver = value
		case "--environment":
			value, err := next(&index, name)
			if err != nil {
				return result, err
			}
			result.Environment = value
		case "--credential-provider":
			value, err := next(&index, name)
			if err != nil {
				return result, err
			}
			result.CredentialProvider = value
		case "--credential-name":
			value, err := next(&index, name)
			if err != nil {
				return result, err
			}
			result.CredentialName = value
		case "--credential-env":
			value, err := next(&index, name)
			if err != nil {
				return result, err
			}
			result.CredentialProvider = "env"
			result.CredentialEnv = value
			result.CredentialName = value
		case "--credential-file":
			value, err := next(&index, name)
			if err != nil {
				return result, err
			}
			result.CredentialProvider = "file"
			result.CredentialName = value
		case "--file-root":
			value, err := next(&index, name)
			if err != nil {
				return result, err
			}
			result.FileRoot = value
		case "--allowed-schemas":
			value, err := next(&index, name)
			if err != nil {
				return result, err
			}
			result.AllowedSchemas = splitCSV(value)
		case "--denied-tables":
			value, err := next(&index, name)
			if err != nil {
				return result, err
			}
			result.DeniedTables = splitCSV(value)
		case "--mask-columns":
			value, err := next(&index, name)
			if err != nil {
				return result, err
			}
			result.MaskColumns = splitCSV(value)
		case "--required":
			result.Required = true
		case "--no-prompt":
			result.NoPrompt = true
		default:
			return result, fmt.Errorf("unsupported database add option %q", name)
		}
	}
	return result, nil
}

func safeDatabaseConfig(alias string, connection config.DatabaseConnectionConfig) map[string]any {
	return map[string]any{
		"alias": alias, "driver": connection.Driver, "environment": connection.Environment,
		"access": connection.Access.Mode, "required": connection.Required,
		"credential_provider": connection.CredentialRef.Provider,
		"credential_name":     connection.CredentialRef.Name,
		"allowed_schemas":     connection.Access.AllowedSchemas,
	}
}

func credentialEnvForAlias(alias string) string {
	value := strings.ToUpper(alias)
	var out strings.Builder
	out.WriteString("CODEBRIDGE_DB_")
	lastUnderscore := false
	for _, char := range value {
		if char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			out.WriteRune(char)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			out.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.TrimSuffix(out.String(), "_") + "_DSN"
}

func splitCSV(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func saveDatabaseCredential(name, secret string) error {
	if strings.TrimSpace(name) == "" || secret == "" {
		return nil
	}
	path := config.DotEnvPath()
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(config.AppConfigDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(config.MergeDotEnv(string(raw), map[string]string{name: secret})), 0o600)
}

func printJSON(output interface{ Write([]byte) (int, error) }, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, string(raw))
	return err
}
