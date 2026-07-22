# Codebridge

Codebridge is a local coding agent written in Go and distributed as a single binary. It manages workspaces, runs a local MCP server, connects ChatGPT Web through a Secure MCP Tunnel, integrates with CodeGraph, and exposes **75 built-in MCP tools plus tools discovered from configured community MCP servers**.

## Highlights

- Native Go CLI for `setup`, `start`, `stop`, `restart`, `status`, `doctor`, `workspace`, `logs`, `config`, `key`, and `tunnel`.
- One stateless Streamable HTTP daemon with `/mcp/session` for per-chat workspace selection, `/mcp` for the primary workspace, and `/mcp/workspaces/<id>` for fixed compatibility endpoints.
- 75 built-in tools plus namespaced tools dynamically discovered from configured upstream MCP servers.
- Root confinement that blocks path traversal and symlink escapes.
- `strict`, `balanced`, and `full` policies, with one-time exact-action approvals for risky operations.
- Embedded MCP Apps widget with no separate web bundle.
- Optional CodeGraph navigation and a generic upstream MCP gateway for database, design, cloud, search, and other community integrations.
- Provider-neutral project memory with an agentmemory adapter, asynchronous capture, retry/backoff, canonical export/import, and daemon-wide provider/recorder pooling.
- Compatible workspace runtimes reuse upstream MCP clients and immutable tool contracts without sharing workspace state or policy.
- Bounded `runtime_metrics`, workspace/session diagnostics, correlation IDs, latency buckets, audit-writer health, and loopback supervisor telemetry without retaining arguments, results, sessions, or error text.
- Builds for Linux, macOS, and Windows.

## Quick install

### Linux and macOS

Install the current stable release with one command:

```bash
curl -fsSL https://raw.githubusercontent.com/qunv/codebridge/main/install.sh | sh
```

The installer detects Linux/macOS and `amd64`/`arm64`, verifies the release checksum, and installs into `~/.local/bin`.

Install a specific release or directory:

```bash
curl -fsSL https://raw.githubusercontent.com/qunv/codebridge/main/install.sh -o install.sh
sh install.sh --version v1.0.0 --install-dir "$HOME/.local/bin"
rm install.sh
```

### Windows PowerShell

```powershell
$Version = "v1.0.0"
$Architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
$Arch = switch ($Architecture) {
    "X64" { "amd64" }
    "Arm64" { "arm64" }
    default { throw "Unsupported architecture: $Architecture" }
}

$Archive = "codebridge_$($Version.Substring(1))_windows_$Arch.zip"
$BaseUrl = "https://github.com/qunv/codebridge/releases/download/$Version"
$InstallDir = Join-Path $env:LOCALAPPDATA "Codebridge\bin"
$TempArchive = Join-Path $env:TEMP $Archive
$TempChecksums = Join-Path $env:TEMP "codebridge-checksums.txt"

Invoke-WebRequest -Uri "$BaseUrl/$Archive" -OutFile $TempArchive
Invoke-WebRequest -Uri "$BaseUrl/checksums.txt" -OutFile $TempChecksums

$Expected = (Get-Content $TempChecksums | Where-Object { $_ -match "\s+$([regex]::Escape($Archive))$" } | ForEach-Object { ($_ -split '\s+')[0] })
if (-not $Expected) {
    throw "Checksum entry not found for $Archive"
}

$Actual = (Get-FileHash -Algorithm SHA256 $TempArchive).Hash.ToLowerInvariant()
if ($Actual -ne $Expected.ToLowerInvariant()) {
    throw "Checksum verification failed for $Archive"
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Expand-Archive -Path $TempArchive -DestinationPath $InstallDir -Force
Remove-Item $TempArchive, $TempChecksums

$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (($UserPath -split ";") -notcontains $InstallDir) {
    [Environment]::SetEnvironmentVariable("Path", "$UserPath;$InstallDir", "User")
}

& "$InstallDir\codebridge.exe" --version
```

Open a new terminal after installation so the updated user `PATH` is loaded.

Release files and checksums are available on the [v1.0.0 release page](https://github.com/qunv/codebridge/releases/tag/v1.0.0).

## Requirements

- Go 1.25 or later when building from source.
- Git for repository-root detection and project identity.
- `rg` is optional; Codebridge falls back to a Go scanner when it is unavailable.
- `codegraph` is optional; `codegraph_explore` runs only when the project contains `.codegraph/`.
- A Tunnel ID and Runtime API key when using the ChatGPT Web tunnel.
- Any package manager or executable required by configured `stdio` MCP servers, such as `npx`, `uvx`, or Docker.
- agentmemory when project memory is enabled.

## Tool module architecture

Tool groups implement a common extension boundary:

```go
type ToolModule interface {
    Name() string
    Specs() []ToolSpec
    Handle(context.Context, CallIdentity, string, map[string]any) (any, error)
    Health(context.Context) any
    Close() error
}
```

The built-in modules are `basic`, `filesystem`, `repo`, `workflow`, `memory`, and `execution`. `Runtime` validates module and tool uniqueness, builds an O(1) tool-to-module index, and keeps policy, audit, and memory capture as shared cross-cutting behavior. Strict policy derives write access from `ToolSpec.ReadOnly`; every external tool with `ReadOnly=false` requires a hashed exact-argument approval under balanced policy unless its module supplies a custom `ToolPolicyProvider`.

Additional modules such as Redis, MongoDB, Elasticsearch, Kafka, Kubernetes, cloud providers, or upstream MCP bridges can be registered before server construction:

```go
if err := runtime.RegisterModule(customModule); err != nil {
    return err
}
server := mcpserver.New(runtime)
```

## Build from source

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

The default command detects the Git root, auto-registers it as a named workspace when it differs from the configured default, and starts or reconciles Codebridge in the background.

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

Endpoints:

- Session workspace MCP: `http://127.0.0.1:8789/mcp/session`
- Primary workspace MCP: `http://127.0.0.1:8789/mcp`
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
```

Show all commands and options:

```bash
codebridge help
```

## Multiple workspace endpoints

Codebridge runs one local daemon with a session router plus fixed compatibility endpoints:

```text
http://127.0.0.1:8789/mcp/session                 per-chat workspace selection
http://127.0.0.1:8789/mcp                         fixed primary workspace
http://127.0.0.1:8789/mcp/workspaces/loyalty-api  fixed loyalty-api workspace
http://127.0.0.1:8789/mcp/workspaces/admin-web    fixed admin-web workspace
```

### One ChatGPT app, one workspace per chat

Point one ChatGPT custom MCP app or tunnel channel at `/mcp/session`. In every new chat, select the repository with a natural command:

```text
Chat A: workspace loyalty-api
Chat B: workspace admin-web
```

The session endpoint publishes four routing tools:

| Tool | Purpose |
|---|---|
| `workspace_select` | Bind the current chat to an enabled workspace |
| `workspace_current` | Verify the binding and selected root |
| `workspace_list` | List workspace IDs available to the router |
| `workspace_clear` | Detach the chat from its workspace |

When the user writes `workspace <id>`, the MCP instructions tell ChatGPT to call `workspace_select`. Codebridge returns a cryptographically random opaque `workspace_binding`; ChatGPT keeps that value in the conversation context and supplies it automatically on every later Codebridge tool call. The user does not copy, paste, or manage the token.

All routed coding-tool schemas require `workspace_binding`. Codebridge removes the field before runtime dispatch, so it is never passed to a tool handler or written into audit arguments. The runtime memory/audit session uses a short hash of the binding instead of the raw token. This keeps two chats isolated even if ChatGPT reconnects or reuses an MCP transport.

Bindings are process-memory only, expire after 24 hours of inactivity, and disappear when Codebridge restarts. Selecting a new workspace on the same MCP session invalidates its previous binding. The router caps active bindings at 4,096, evicts the least recently used binding only at capacity, and performs full expiry cleanup on a bounded interval instead of scanning all bindings on every tool call. After an expiry, eviction, or restart, say `workspace <id>` again. Fixed `/mcp` and `/mcp/workspaces/<id>` endpoints remain available for clients that want endpoint-level binding.

Running `codebridge` derives the primary workspace ID from the Git repository root folder, or from the current folder outside Git. A different repository is automatically registered as a named workspace; `codebridge restart` performs the same check before restarting the daemon. Generated IDs are lowercase ASCII slugs, replace unsupported character runs with `-`, collapse repeated separators, and are limited to 32 characters. Named-workspace collisions receive a stable path hash. An existing registration for the same root is reused and automatically enabled.

Examples:

```text
Loyalty API.Service  → loyalty-api-service
repo_name            → repo-name
service-api          → service-api
service-api collision → service-api-4f29c8a1
```

The primary workspace remains available at the compatibility endpoint `/mcp`, but its router ID is the repository or folder slug rather than `default`. A bare invocation outside Git uses the current directory; an explicit `--workspace` uses the selected repository or directory.

Register and manage workspaces manually with:

```bash
codebridge workspace add loyalty-api /path/to/loyalty-api
codebridge workspace add admin-web /path/to/admin-web \
  --extra-root /path/to/shared-contracts

codebridge workspace list
codebridge workspace status loyalty-api
codebridge workspace stop loyalty-api
codebridge workspace start loyalty-api
codebridge workspace remove admin-web
```

`workspace add` creates an enabled registry entry and a non-secret workspace config. Restart Codebridge, or run `workspace start <id>`, to reconcile the shared daemon. `workspace stop <id>` disables the endpoint and restarts the daemon when it is online. `workspace remove <id> --force` also removes the named config file; local state is retained unless deleted manually.

Every endpoint owns a separate runtime with its own:

- workspace manager and root confinement;
- notes, tasks, checkpoints, audit, approvals, backups, and patch history;
- managed process registry with fixed-capacity circular stdout/stderr tails, plus profile;
- memory project and workspace-prefixed MCP session identity;
- policy, tool exposure, request limits, and workspace-local handlers.

`SharedServices` owns daemon-lifetime resources. Compatible runtimes reuse one memory provider and asynchronous recorder. Audit records for the same state root share one bounded batching writer with synchronous fallback on queue saturation and size-based log rotation. Upstream HTTP clients reuse one MCP session when the server name, transport configuration, resolved secrets, and timeouts match. Stdio clients are also keyed by their resolved `cwd`, so two repositories cannot accidentally share a workspace-relative child process. Tool filtering or approval-policy differences create separate immutable contracts while still reusing the compatible upstream client.

Built-in tool schemas are constructed once per process, and each fixed or session-routing MCP server is assembled once when the HTTP daemon starts rather than once per JSON-RPC request. `workspace_info`, `memory_status`, and `/internal/healthz` expose `shared_resources` counters for provider, recorder, audit-writer, upstream-client, contract, acquire, and reuse counts. `runtime_metrics` reports per-workspace call counts, bounded latency summaries, in-flight peaks, policy/cancellation outcomes, audit queue/batch/rotation/write-failure counters, memory-observation enqueue/drop counts, repository-cache generation/cardinality, and a bounded argument-free recent-call ring. Deep module health adds sanitized upstream queue, concurrency, health-cache, reconnect, and circuit counters.

Repository overview tools share one generation-keyed inventory per root for five minutes. Tree depth/limit views, project profile, important files, and symbol results derive from that inventory instead of repeating `WalkDir`; successful Codebridge file, patch, shell, process-start, and mutating-Git operations invalidate the generation. Git status uses one porcelain-v2 process and a 300 ms snapshot cache. `security_scan` and `todo_scan` stream tracked files through `git grep`, stop as soon as their result limit is reached, and use a bounded context-aware fallback when Git is unavailable.

The daemon listener, bearer authentication, Origin policy, tunnel process, Runtime API key, and pooled resources remain global. A generated tunnel profile contains channel `session` for `/mcp/session`, channel `main` for `/mcp`, and channel `workspace-<id>` for every enabled fixed endpoint.

The unified default layout on every operating system is:

```text
~/.codebridge/
  config.json
  .env
  workspaces.json
  workspaces/<id>/config.json
  state/
    processes.json
    launcher.log
    profiles/
    instances/<id>/audit.log
    instances/<id>/workspaces/<path-hash>/...
```

Set `CODEBRIDGE_HOME` to relocate the complete layout. The existing granular overrides (`CODEBRIDGE_CONFIG_PATH`, `CODEBRIDGE_DATA_DIR`, and `CODEBRIDGE_WORKSPACE_REGISTRY_PATH`) remain supported for advanced deployments.

The shared `.env` remains the source for the Runtime API key and referenced memory or upstream MCP secrets, so credentials are not copied into workspace configs. Workspace IDs must match `[a-z0-9][a-z0-9_-]{0,31}`; `default` is reserved. Registry validation also rejects shared config paths or data directories between two IDs.

On the first run with the default layout, Codebridge copies missing files from the former OS-specific config and state directories into `~/.codebridge` without overwriting new files or deleting the legacy copies. Registry versions 1 and 2 are upgraded in memory, including default absolute workspace paths. Stop any legacy per-workspace server or tunnel processes before starting the upgraded daemon.

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
2. Add one custom MCP connector.
3. Select the configured tunnel channel `session`, which points to `/mcp/session`.
4. Choose `No auth`; the Runtime API key stays on the local machine and is not entered in the connector.
5. Start a new chat and write `workspace <id>`, for example `workspace loyalty-api`.
6. In another chat, write a different workspace ID. Each chat carries its own opaque binding automatically.

### ChatGPT Skills and Codebridge

ChatGPT Skills are reusable workflows owned and managed by the ChatGPT client. Create, install, upload, and share them through ChatGPT rather than through the Codebridge MCP server. See [Skills in ChatGPT](https://help.openai.com/en/articles/20001066-skills-in-chatgpt).

Codebridge intentionally provides capabilities rather than a second skill registry:

- ChatGPT Skills define reusable instructions, examples, resources, and task workflows.
- Codebridge exposes workspace, filesystem, repository, execution, policy, memory, and upstream MCP tools.
- A skill may instruct ChatGPT to call Codebridge tools, but every call still passes through Codebridge policy, approval, path-confinement, audit, and memory rules.
- Codebridge does not read, create, update, delete, or persist ChatGPT Skills.

The legacy Codebridge skill interfaces have been removed:

```text
MCP: list_skills, read_skill, create_skill, delete_skill
CLI: codebridge skills [list|read]
Files: embedded *.skill assets
```

After upgrading from a build that exposed those interfaces, rebuild or install the new binary, run `codebridge restart`, and reconnect or refresh the ChatGPT connector so it requests the current `tools/list` contract.

## Configuration and secrets

Codebridge loads configuration in this order:

```text
defaults
  → config.json
  → environment overrides
  → CLI options for the current invocation
```

All persistent files now live below one root:

```text
~/.codebridge/                 default root on Linux, macOS, and Windows
~/.codebridge/config.json      non-secret configuration
~/.codebridge/.env             Runtime API key and optional provider secrets
~/.codebridge/workspaces.json  named-workspace registry
~/.codebridge/state/           process, log, audit, backup, and workspace state
```

Set `CODEBRIDGE_HOME=/custom/path` before launching Codebridge to relocate the whole tree. Project-specific command conventions and ignored directories use `<workspace>/.codebridge/profile.json`; the former `<workspace>/.agent/profile.json` remains a read-only fallback during migration.

On Unix, `config.json` and `.env` are written with `0600` permissions. Codebridge does not serialize the MCP bearer token or approval token into `config.json`.

Runtime-only secrets can be supplied through environment variables:

```bash
export CONTROL_PLANE_API_KEY="..."
export MCP_AUTH_TOKEN="..."
export AGENT_APPROVAL_TOKEN="..."
```

## Community and upstream MCP servers

Codebridge can start or connect to community MCP servers and expose their discovered tools through the same policy, approval, audit, health, and lifecycle pipeline as built-in modules. Each `mcpServers` entry becomes a module named `mcp_<server>`, and the entry name is also the public tool prefix: `<server>__<normalized_tool_name>`. This gives every configured server one stable identity and prevents prefix collisions.

A `stdio` server may use any executable available in `PATH`, including `npx`, `uvx`, `pnpm`, `bunx`, `docker`, or a custom binary. Native executables are started directly with structured argv. On Windows only, resolved `.cmd`/`.bat` launchers such as `npx.cmd` are invoked through a restricted `cmd.exe /d /s /v:off /c` adapter; quote, control, `%`, and `!` expansion characters are rejected in batch arguments:

```json
{
  "mcpServers": {
    "postgres_prod": {
      "transport": "stdio",
      "command": "uvx",
      "args": [
        "postgres-mcp",
        "--access-mode=restricted"
      ],
      "envRefs": {
        "DATABASE_URI": "POSTGRES_PROD_MCP_DATABASE_URI"
      },
      "required": false,
      "maxConcurrency": 8,
      "healthCacheMs": 5000,
      "failureCooldownMs": 1000,
      "policy": {
        "default": "approval",
        "readOnlyTools": [
          "list_schemas",
          "list_tables",
          "describe_table"
        ],
        "alwaysApproveTools": [
          "execute_sql"
        ]
      }
    }
  }
}
```

The credential value remains outside `config.json`:

```bash
POSTGRES_PROD_MCP_DATABASE_URI="postgresql://username:password@localhost:5432/dbname"
```

A Streamable HTTP server uses `url`; remote hosts require explicit `allowRemote: true`, and sensitive headers must reference environment variables:

```json
{
  "mcpServers": {
    "remote_search": {
      "transport": "streamable-http",
      "url": "https://mcp.example.com/mcp",
      "allowRemote": true,
      "headerRefs": {
        "Authorization": "REMOTE_MCP_AUTHORIZATION"
      },
      "policy": {
        "default": "approval",
        "trustAnnotations": false,
        "readOnlyTools": ["search"]
      }
    }
  }
}
```

Figma Desktop can be connected through the same generic HTTP bridge:

```json
{
  "mcpServers": {
    "figma": {
      "transport": "streamable-http",
      "url": "http://127.0.0.1:3845/mcp",
      "required": false,
      "policy": {
        "trustAnnotations": false,
        "default": "approval",
        "readOnlyTools": [
          "get_design_context",
          "get_screenshot",
          "get_metadata",
          "get_variable_defs",
          "get_code_connect_map",
          "get_figjam"
        ]
      }
    }
  }
}
```

Important behavior:

- The `mcpServers` entry name is always the module identity and public tool namespace. For example, `postgres_prod` creates module `mcp_postgres_prod` and tools such as `postgres_prod__query`.
- Tool discovery runs once during Codebridge startup. Compatible workspace runtimes reuse the same upstream MCP client/session; policy or tool-filter differences keep separate contracts while reusing that connection. Stdio pooling additionally requires the same confined resolved `cwd`. Restart Codebridge after an upstream server changes its tool list.
- `maxConcurrency` defaults to 8 and is limited to 128 concurrent calls per shared upstream client. Calls wait within their caller context and expose queued/in-flight/rejected counters. Session replacement is reference-counted, so a reconnect cannot close a session still borrowed by another call.
- Deep health checks are single-flight and cached for `healthCacheMs` (default 5,000 ms), preventing multiple workspaces sharing one client from pinging it repeatedly. Repeated transport/reconnect failures open a three-failure circuit for `failureCooldownMs` (default 1,000 ms); calls fail fast during cooldown instead of creating a reconnect storm. Both timing values are limited to 60 seconds.
- `required: true` makes Codebridge startup fail when the server cannot connect or publish a valid tool contract. Optional servers are skipped and reported through `workspace_info.upstream_mcp.startup_warnings`.
- `codebridge start` streams `[startup]` phase logs while workspace, memory, and upstream MCP dependencies initialize. The launcher readiness timeout includes every enabled server's `startupTimeoutMs` and the bounded subprocess cleanup time for stdio servers, so slow or unavailable dependencies are not killed by the supervisor while they are shutting down.
- `codebridge doctor` requests a deep local health check and reports each configured upstream as `mcp:<name>`, including transport, discovered tool count, reconnect count, and the latest sanitized connection error.
- Community tool annotations are untrusted by default. Unknown tools require approval under `balanced`; `alwaysApproveTools` still requires exact approval under `policy=full`.
- `strict` blocks every upstream tool not explicitly classified as read-only.
- Parent process secrets are not inherited. Only a small platform environment plus explicit `inheritEnv`, `env`, and `envRefs` values are passed to `stdio` processes.
- Raw upstream arguments and results are not persisted in audit or automatic memory capture. Audit stores only argument names and bounded call metadata.
- A community MCP command is arbitrary local code and is not an operating-system sandbox. Pin package or image versions and review the server before enabling it.

Tool exposure works through module ownership:

```json
{
  "tools": {
    "allowedGroups": ["basic", "filesystem", "mcp_postgres_prod"],
    "deniedTools": ["postgres_prod__execute_sql"]
  }
}
```

## Database and Figma integrations

Database and Figma integrations are provided through upstream MCP servers configured under `mcpServers`. Restart Codebridge after changing the configuration so their tools are discovered and registered.

The exact upstream tool names depend on the selected community server. Use restrictive upstream access modes and explicit `readOnlyTools`/`alwaysApproveTools` policy lists rather than assuming a server's annotations are trustworthy.

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
    "deliveryWorkers": 4,
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

### `deliveryWorkers`

Defaults to 4 and is limited to 32. It is used only when the configured provider explicitly opts into concurrent calls; otherwise Codebridge forces one worker. The total `queueSize` is divided across session shards, so increase both values together when one session can produce large bursts.

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

Fixed endpoints derive memory identity from the MCP connection. Codebridge prefers the protocol session ID; when the transport does not provide one, it creates a stable process-local hash from the session object. Named endpoints prefix that identity with `workspace:<id>:`. The `/mcp/session` router instead uses `workspace:<id>:chat:<binding-hash>`, so two chats remain separate even if ChatGPT reconnects or reuses one transport. The raw workspace binding is never used as a provider session ID.

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

Compatible workspace runtimes share one daemon-scoped recorder; every queued observation still carries its own project, `cwd`, and workspace-prefixed session ID. Providers that explicitly implement `ConcurrencySafeProvider` use `deliveryWorkers` deterministic session shards: observations from one session remain FIFO on one worker, while different sessions can deliver concurrently. Providers without that opt-in remain single-worker and serialized. Closing one runtime leaves the queue active. During daemon shutdown, `SharedServices` stops acquisitions, drains each recorder, and closes the pooled provider afterward. `memory_status` exposes `scope=daemon`, worker/shard capacity, `enqueued`, `delivered`, `retried`, `failed`, `dropped`, and the current queue depth.

## Provider options

`memory.options` contains adapter-specific configuration, such as custom REST paths or a response-size limit. Codebridge rejects any option key containing `secret`, `password`, `token`, `apiKey`, `authorization`, or `credential`; secrets must be referenced through `memory.secretEnv`.

A new provider can register through `memory/factory.Register` without changing the MCP tools or runtime dispatch. Pooled providers are wrapped by a serializer, so plugins do not need to implement their own concurrent-call safety; optional export/import capabilities are preserved.

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
- The `balanced` policy permits edits but requires an exact one-time approval for deletion, installation, network access, mutating Git, `memory_forget`, and upstream tools not explicitly classified as read-only.
- The `full` policy enables the complete project workflow while catastrophic system commands remain blocked; upstream `alwaysApproveTools` still require approval.
- Audit arguments are recursively redacted before they are written to local state. Community MCP modules reduce audit data to argument names and bounded metadata and are excluded from automatic memory capture.
- Session-routed coding calls require an explicit opaque binding. Codebridge removes it before runtime dispatch and never persists the raw value in audit, memory, health, or workspace state.
- A non-loopback MCP host requires a bearer token.

# Repository structure

```text
cmd/codebridge/       executable entrypoint
internal/app/         version and tier metadata
internal/cli/         command parsing, setup, lifecycle, tunnel, and installation
internal/server/      HTTP routes, authentication, CORS/origin, and limits
internal/mcpserver/   MCP SDK adapter, session identity, and widget resource
internal/agent/       tool-module registry, runtime pipeline, policy, and functional modules
internal/workspace/   root confinement, owning-root resolution, search, and tree
internal/security/    command guards, redaction, and approvals
internal/patch/       backup, diff, preview, validation, and undo
internal/processx/    bounded process execution and process-tree management
internal/upstreammcp/  generic stdio and Streamable HTTP MCP client/session management
internal/memory/      canonical contracts, recorder, scoping, and adapters
internal/state/       per-workspace local state
internal/workspaceregistry/ persistent named-workspace registry and migration
internal/assets/      embedded MCP Apps widget
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the detailed design.

# Verification

```bash
go test ./...
go test -race ./internal/memory/... ./internal/agent ./internal/cli ./internal/config ./internal/mcpserver ./internal/server ./internal/workspaceregistry
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

Codebridge is released under AGPL-3.0-or-later; see `LICENSE` at the repository root.
