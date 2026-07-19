# Codebridge

Codebridge is a local coding agent written in Go and distributed as a single binary. It manages workspaces, runs a local MCP server, connects ChatGPT Web through a Secure MCP Tunnel, bridges the Figma Desktop MCP server, integrates with CodeGraph, and exposes **91 MCP tools** for reading and editing code, running commands, Git operations, planning, review, approvals, and project memory.

## Highlights

- Native Go CLI for `setup`, `start`, `stop`, `restart`, `status`, `doctor`, `workspace`, `logs`, `config`, `key`, `skills`, `figma`, and `tunnel`.
- Stateless Streamable HTTP MCP at `/mcp`, public health at `/healthz`, and supervisor health at `/internal/healthz`.
- 91 tools registered from a single contract in `agent.Tools()`.
- Root confinement that blocks path traversal and symlink escapes.
- `strict`, `balanced`, and `full` policies, with one-time exact-action approvals for risky operations.
- Embedded MCP Apps widget with no separate web bundle.
- Optional CodeGraph navigation and Figma Desktop MCP bridge.
- Provider-neutral project memory with an agentmemory adapter, asynchronous capture, retry/backoff, and canonical export/import.
- Builds for Linux, macOS, and Windows.

## Requirements

- Go 1.25 or later.
- Git for repository-root detection and project identity.
- `rg` is optional; Codebridge falls back to a Go scanner when it is unavailable.
- `codegraph` is optional; `codegraph_explore` runs only when the project contains `.codegraph/`.
- A Tunnel ID and Runtime API key when using the ChatGPT Web tunnel.
- Figma Desktop MCP when using the Figma tool group.
- agentmemory when project memory is enabled.

## Build and install

```bash
cd codebridge
go test ./...
go build -o dist/codebridge ./cmd/codebridge
```

Or:

```bash
make build
```

Install into the user binary directory:

```bash
install -Dm755 dist/codebridge "$HOME/.local/bin/codebridge"
```

The binary also supports:

```bash
./dist/codebridge install-cli
```

## Quick start

Run the setup wizard:

```bash
codebridge setup
```

Start Codebridge in the current repository:

```bash
cd /path/to/repo
codebridge
```

The default command detects the Git root, saves it as the workspace, and starts Codebridge in the background.

Run only the local MCP server without a tunnel:

```bash
codebridge start \
  --workspace /path/to/repo \
  --no-tunnel \
  --background \
  --save
```

Run in the foreground for debugging:

```bash
codebridge serve --workspace /path/to/repo --no-tunnel
```

Default endpoints:

- MCP: `http://127.0.0.1:8789/mcp`
- Health: `http://127.0.0.1:8789/healthz`
- Supervisor health: `http://127.0.0.1:8789/internal/healthz`

## Common CLI commands

```bash
codebridge status
codebridge status --json
codebridge doctor
codebridge doctor --json
codebridge workspace
codebridge workspace /path/to/repo
codebridge restart
codebridge stop
codebridge logs
codebridge config get
codebridge config path
codebridge skills list
codebridge figma status
```

Show all commands and options:

```bash
codebridge help
```

## ChatGPT Web tunnel

```bash
codebridge tunnel install
codebridge setup
codebridge key set
codebridge profile
codebridge restart
```

`codebridge key set` stores the Runtime API key in the local `.env` file with `0600` permissions. In ChatGPT Web:

1. Open **Settings → Connectors → Developer mode**.
2. Add a custom MCP connector.
3. Select the configured tunnel.
4. Choose `No auth`; the Runtime API key stays on the local machine and is not entered in the connector.
5. Call `workspace_info` or `workspace_snapshot` to verify the connection.

## Configuration and secrets

Codebridge loads configuration in this order:

```text
defaults
  → config.json
  → environment overrides
  → CLI options for the current invocation
```

Configuration locations:

| Operating system | Config directory |
|---|---|
| Linux | `$XDG_CONFIG_HOME/codebridge` or `~/.config/codebridge` |
| macOS | `~/Library/Application Support/Codebridge` |
| Windows | `%APPDATA%\Codebridge` |

Main files:

```text
config.json   non-secret configuration
.env          Runtime API key and optional memory secret
```

On Unix, `config.json` and `.env` are written with `0600` permissions. Codebridge does not serialize the MCP bearer token or approval token into `config.json`.

Runtime-only secrets can be supplied through environment variables:

```bash
export CONTROL_PLANE_API_KEY="..."
export MCP_AUTH_TOKEN="..."
export AGENT_APPROVAL_TOKEN="..."
```

## Database MCP

CodeBridge exposes stable, alias-routed, read-only database tools:

```text
db_list_connections
db_describe
db_query
db_explain
```

The shared execution path lives in `internal/database/sqlcore` and uses `database/sql` for pooling, read-only transactions, row scanning, masking, and result limits. Driver packages provide only a dialect adapter and their `database/sql` driver. PostgreSQL uses `pgx/v5/stdlib`; MySQL uses `go-sql-driver/mysql`.

A database connection stores only a credential reference:

```json
{
  "database": {
    "enabled": true,
    "connections": {
      "db.codebridge_dev": {
        "driver": "postgres",
        "environment": "dev",
        "credentialRef": {
          "provider": "env",
          "name": "CODEBRIDGE_DB_CODEBRIDGE_DEV_DSN"
        },
        "access": {
          "mode": "read-only",
          "allowedSchemas": ["public"]
        }
      }
    }
  }
}
```

The DSN belongs in `.env` or another runtime secret source, never in `config.json`:

```bash
CODEBRIDGE_DB_CODEBRIDGE_DEV_DSN="postgres://..."
CODEBRIDGE_DB_MYSQL_DEV_DSN="user:password@tcp(127.0.0.1:3306)/app?tls=true"
```

For a MySQL alias, set `"driver": "mysql"`. When `allowedSchemas` is configured, the DSN must select one of those databases. Production MySQL TCP connections require verified TLS; Unix sockets are permitted without TLS. CodeBridge rejects MySQL DSNs enabling multi-statements, client-side parameter interpolation, unrestricted local files, cleartext/old passwords, plaintext fallback, or TLS verification bypass.

Adding another SQL driver requires implementing `sqlcore.Dialect`, constructing the common client with `sqlcore.Open` or `sqlcore.NewWithDB`, and registering it through `database/factory.Register`. See `docs/ADR-DATABASE-MCP.md`.

## Figma Desktop

By default, Codebridge connects to:

```text
http://127.0.0.1:3845/mcp
```

Check the bridge:

```bash
codebridge figma status
codebridge figma tools
```

Remote Figma endpoints are blocked by default. Enable `FIGMA_DESKTOP_ALLOW_REMOTE=1` only when you understand the risk.

# Project memory

Codebridge exposes a provider-neutral contract so ChatGPT does not depend directly on agentmemory:

| Tool | Purpose |
|---|---|
| `memory_status` | Return the provider, project scope, capabilities, health, and recorder statistics |
| `memory_context` | Retrieve compact historical context for the current task |
| `memory_search` | Search decisions, failures, solutions, preferences, and procedures |
| `memory_remember` | Store an explicit fact, decision, or solution |
| `memory_commit` | Create a session handoff from a summary and local project state |
| `memory_forget` | Delete a memory or session; destructive under the `balanced` policy |
| `memory_export` | Export the canonical schema as an object or JSONL |
| `memory_import` | Import the canonical schema to migrate between providers |

Memory is **historical evidence**, not the current source of truth. The agent must still verify the current implementation with CodeGraph or file tools before editing code.

## Enable memory

Recommended setup:

```bash
codebridge setup
codebridge restart
codebridge doctor
```

The setup wizard stores non-secret settings in `config.json`. Only the secret, when required by the backend, is stored in `.env`.

At the secret prompt:

```text
Enter  keep the existing secret
-      clear the secret
value  replace the secret
```

When agentmemory is bound only to localhost and `AGENTMEMORY_SECRET` is not enabled, the secret can remain empty. When a secret is configured, Codebridge sends:

```text
Authorization: Bearer <secret>
```

## Example memory configuration

```json
{
  "memory": {
    "enabled": true,
    "provider": "agentmemory",
    "endpoint": "http://127.0.0.1:3111",
    "secretEnv": "CODEBRIDGE_MEMORY_SECRET",
    "timeoutMs": 3000,
    "captureMode": "selected",
    "tokenBudget": 1600,
    "agentId": "chatgpt-codebridge",
    "required": false,
    "projectStrategy": "git-origin",
    "queueSize": 128,
    "deliveryTimeoutMs": 2000,
    "retryMaxAttempts": 3,
    "retryBackoffMs": 100,
    "healthCacheMs": 5000,
    "options": {}
  }
}
```

### `required`

- `false`: fail open. An offline provider does not prevent Codebridge from starting or running coding tools.
- `true`: startup fails when the provider health check does not succeed.

### `captureMode`

- `off`: disable automatic capture; explicitly invoked memory tools still work.
- `selected`: capture only tools with durable historical value, such as edits, tests, builds, Git operations, plans, decisions, and reviews.
- `metadata`: capture more tool calls while retaining only minimal metadata.

Automatic capture does not send raw source code, patches, command output, or secrets. Inputs are redacted, and results are reduced to whitelisted fields.

### `projectStrategy`

- `git-origin`: normalize the remote into a value such as `git:github.com/owner/repo`; clones in different directories share the same project memory.
- `path-hash`: hash the configured owning root; separate checkouts receive separate memory scopes.

A file or subdirectory is always mapped to its configured owning root before the project ID is created, preventing memory fragmentation by subdirectory.

### Session identity

Each logical MCP connection receives its own memory session. Codebridge prefers the protocol session ID; when the transport does not provide one, it creates a stable process-local hash from the session object. Concurrent chats or connections are therefore not merged into one process-level session.

### Asynchronous recorder

Automatic capture uses a bounded queue and does not block tool responses:

```text
tool completed
  → redact + whitelist
  → non-blocking enqueue
  → deliver with timeout
  → bounded exponential retry
  → delivered / failed / dropped counters
```

During shutdown, the recorder stops accepting new entries and drains the remaining queue. `memory_status` exposes `enqueued`, `delivered`, `retried`, `failed`, `dropped`, and the current queue depth.

## Provider options

`memory.options` contains adapter-specific configuration, such as custom REST paths or a response-size limit. Codebridge rejects any option key containing `secret`, `password`, `token`, `apiKey`, `authorization`, or `credential`; secrets must be referenced through `memory.secretEnv`.

A new provider can register through `memory/factory.Register` without changing the MCP tools or runtime dispatch.

## Export and import

Export memory through MCP:

```json
{
  "path": ".",
  "format": "jsonl"
}
```

Import it again:

```json
{
  "path": ".",
  "jsonl": "{\"id\":\"...\",\"content\":\"...\"}\n"
}
```

Exports are normalized into the Codebridge schema. Import replays each item through the provider's `Remember` operation instead of restoring a raw database dump, making the format suitable for migration between different backends.

# Security model

- File tools and command `cwd` values are restricted to configured roots.
- Canonicalization blocks traversal, symlink, and junction escapes.
- A configured root cannot be deleted or renamed through dedicated tools or patch operations.
- Codebridge is not an operating-system sandbox; accepted commands still run with the current user's privileges.
- `safe` mode blocks destructive shell patterns and Git mutations.
- The `strict` policy permits only read and analysis operations.
- The `balanced` policy permits edits but requires an exact one-time approval for deletion, installation, network access, mutating Git, mutating Figma, and `memory_forget`.
- The `full` policy enables the complete project workflow while catastrophic system commands remain blocked.
- Audit arguments are recursively redacted before they are written to local state.
- A non-loopback MCP host requires a bearer token.

# Repository structure

```text
cmd/codebridge/       executable entrypoint
internal/app/         version and tier metadata
internal/cli/         command parsing, setup, lifecycle, tunnel, and installation
internal/server/      HTTP routes, authentication, CORS/origin, and limits
internal/mcpserver/   MCP SDK adapter, session identity, and widget resource
internal/agent/       91-tool registry, runtime, policy, and handlers
internal/workspace/   root confinement, owning-root resolution, search, and tree
internal/security/    command guards, redaction, and approvals
internal/patch/       backup, diff, preview, validation, and undo
internal/processx/    bounded process execution and process-tree management
internal/figma/       Figma Desktop MCP bridge
internal/memory/      canonical contracts, recorder, scoping, and adapters
internal/database/    alias routing, shared database/sql core, and dialect adapters
internal/state/       per-workspace local state
internal/assets/      embedded widget and built-in skills
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the detailed design.

# Verification

```bash
go test ./...
go test -race ./internal/memory/... ./internal/agent ./internal/cli ./internal/config ./internal/mcpserver
go vet ./...
go build ./...
GOOS=windows GOARCH=amd64 go build ./...
GOOS=darwin GOARCH=arm64 go build ./...
```

External Streamable HTTP smoke test:

```bash
CODEBRIDGE_TEST_ENDPOINT=http://127.0.0.1:8789/mcp \
  go test ./internal/server -run TestExternalStreamableHTTP -v
```

Optional MySQL integration test:

```bash
CODEBRIDGE_TEST_MYSQL_DSN='user:password@tcp(127.0.0.1:3306)/app?tls=true' \
  go test ./internal/database/mysql -run TestMySQLIntegration -v
```

Codebridge is released under AGPL-3.0-or-later; see `LICENSE` at the repository root.
