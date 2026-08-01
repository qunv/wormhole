<p align="center">
  <img src="./assets/codebridge-terminal-banner.svg" alt="Codebridge terminal banner" width="100%" />
</p>

# Codebridge

Codebridge is a local-first MCP coding agent written in Go. It runs beside your repositories, gives ChatGPT controlled access to local files and development tools, and can connect private workspaces through OpenAI Secure MCP Tunnel without exposing a public server.

## What it provides

- One local daemon for multiple repositories.
- Per-chat workspace selection with `workspace <id>`.
- A compact `fast` tool profile for everyday coding and a complete `main` profile for advanced workflows.
- Root-confined file, Git, command, patch, review, process, memory, and upstream MCP tools.
- `strict`, `balanced`, and `full` policies with exact, expiring approvals for risky actions.
- Bounded logs, backups, audit records, process output, caches, and workspace state.
- A single native binary for Linux, macOS, and Windows.

For package ownership, runtime internals, and security invariants, see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Requirements

For local use:

- Git.
- Go 1.25 or later only when building from source.
- `rg` and `codegraph` are optional.

For ChatGPT through Secure MCP Tunnel:

- A ChatGPT workspace with custom MCP app access and Developer mode enabled. OpenAI currently documents full MCP apps for ChatGPT Business, Enterprise, and Edu on the web.
- An OpenAI Platform organization permitted to use Secure MCP Tunnel.
- A tunnel scoped to the target ChatGPT workspace.
- A restricted Runtime API key with **Tunnels Read + Use**.

ChatGPT now calls this integration a **custom app** or **MCP app**. Older interfaces may call it a custom connector. It is not a legacy ChatGPT plugin.

Official references:

- [Developer mode and MCP apps in ChatGPT](https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt)
- [OpenAI tunnel-client end-user guide](https://github.com/openai/tunnel-client/blob/master/docs/end-user-guide.md)
- [OpenAI tunnel-client releases](https://github.com/openai/tunnel-client/releases/latest)

## Install

### Linux and macOS

```bash
curl -fsSL https://raw.githubusercontent.com/qunv/codebridge/main/install.sh | sh
codebridge --version
```

The installer downloads the matching release, verifies its checksum, and installs to `~/.local/bin` by default.

### Windows

Download the matching archive and `checksums.txt` from the [latest Codebridge release](https://github.com/qunv/codebridge/releases/latest), verify the SHA-256 checksum, extract `codebridge.exe`, and add its directory to your user `PATH`.

### Build from source

```bash
git clone https://github.com/qunv/codebridge.git
cd codebridge
go test ./...
make install
```

## Connect Codebridge to ChatGPT

The complete flow is:

```text
ChatGPT MCP app
  → OpenAI Secure MCP Tunnel
  → tunnel-client running on your machine
  → Codebridge at http://127.0.0.1:8789
  → selected local workspace
```

### 1. Configure Platform permissions

In OpenAI Platform, create or assign roles before creating keys:

| Operator | Required tunnel permissions |
|---|---|
| Runs Codebridge/tunnel-client | Tunnels Read + Use |
| Creates or edits tunnels | Tunnels Read + Manage |
| Does both | Tunnels Read + Manage + Use |

Print the Tunnels and Runtime API-key URLs with:

```bash
codebridge keys
```

Role and group configuration is available on these pages:

- [Organization roles](https://platform.openai.com/settings/organization/people/roles)
- [Organization groups](https://platform.openai.com/settings/organization/people/groups)
- [Tunnels](https://platform.openai.com/settings/organization/tunnels)
- [Runtime API keys](https://platform.openai.com/settings/organization/api-keys)

Prefer assigning roles through groups when multiple operators need access.

### 2. Create the Secure MCP Tunnels

Open [Platform → Organization → Tunnels](https://platform.openai.com/settings/organization/tunnels). For the recommended setup, create two tunnels:

| Tunnel | Local mode | Purpose |
|---|---|---|
| `codebridge-fast` | `fast` | Default coding app with the compact tool contract |
| `codebridge-full` | `full` | Optional app for memory, processes, approvals, diagnostics, and upstream MCP tools |

For each tunnel:

1. Scope it to the correct Platform organization.
2. Include the target ChatGPT workspace ID. A tunnel without the correct workspace scope may exist in Platform but not appear in ChatGPT's tunnel picker.
3. Copy the returned ID, which looks like `tunnel_0123456789abcdef0123456789abcdef`.

Codebridge also retains the legacy one-tunnel configuration for existing installations, but two named tunnels are recommended because the current ChatGPT tunnel picker selects a Tunnel ID and does not expose the tunnel-client's logical channel field.

### 3. Create the Runtime API key

Open [Platform → Organization → API keys](https://platform.openai.com/settings/organization/api-keys) and create a **Restricted** Runtime API key with:

```text
Tunnels Read
Tunnels Use
```

Do not use an Admin API key for the long-running tunnel process. `OPENAI_ADMIN_KEY` is only needed when using `tunnel-client admin tunnels create|update|delete`.

Keep these values separate:

| Value | Used for |
|---|---|
| Tunnel ID | Identifies the tunnel shared by ChatGPT and tunnel-client |
| Runtime API key | Authenticates the long-running tunnel-client process |
| Admin API key | Optional tunnel-management CLI operations only |

### 4. Configure and start Codebridge

Install OpenAI's tunnel client:

```bash
codebridge tunnel install
```

Open the Admin UI and add the named tunnel definitions under **Configuration → Tunnel definitions**:

```json
{
  "noTunnel": false,
  "tunnels": {
    "fast": {
      "enabled": true,
      "tunnelId": "tunnel_FAST_ID",
      "mode": "fast",
      "profile": "codebridge-fast",
      "runtimeKeyEnv": "CONTROL_PLANE_API_KEY_FAST"
    },
    "full": {
      "enabled": true,
      "tunnelId": "tunnel_FULL_ID",
      "mode": "full",
      "profile": "codebridge-full",
      "runtimeKeyEnv": "CONTROL_PLANE_API_KEY_FULL"
    }
  }
}
```

Store each Runtime API key separately from `config.json`:

```bash
codebridge key set --runtime-key-env CONTROL_PLANE_API_KEY_FAST
codebridge key set --runtime-key-env CONTROL_PLANE_API_KEY_FULL
```

The values are written to the owner-only `~/.codebridge/.env` file. The Admin **Secrets** page provides the same write-only workflow.

Generate both profiles, start Codebridge, and verify every managed process:

```bash
codebridge profile
codebridge restart
codebridge doctor
codebridge status
codebridge tunnel list
```

Codebridge runs one local MCP daemon and one tunnel-client process per enabled tunnel. The generated Fast profile maps its `main` channel to `/mcp/session/fast`; the Full profile maps `main` to `/mcp/session`.

Existing single-tunnel configurations using `tunnelId`, `profile`, and `runtimeKeyEnv` continue to work and keep the historical logical channels for backward compatibility.

### 5. Create the MCP app in ChatGPT

First enable Developer mode for the ChatGPT workspace. OpenAI documents the admin control under:

```text
Workspace Settings
  → Permissions & Roles
  → Connected Data
  → Developer mode / Create custom MCP connectors
```

Create two apps:

1. Open **Settings → Apps → Create**, or **Workspace Settings → Apps → Create** as an admin/owner.
2. Create `Codebridge Fast`, select **Tunnel** or **Secure MCP Tunnel**, and choose `tunnel_FAST_ID`.
3. Choose **No auth** for the MCP app. Tunnel control-plane authentication is handled by the Runtime API key stored on the machine.
4. Click **Scan Tools**, verify the compact tool set, and create the app.
5. Repeat for `Codebridge Full` with `tunnel_FULL_ID` and verify the expanded tool set.

The ChatGPT UI does not need a channel selector: each Tunnel ID has its own generated profile whose `main` channel points to the intended local endpoint.

The app should appear under enabled apps with a `Dev` label until it is published by a workspace admin.

OpenAI may change labels while MCP apps remain in beta. The current official flow is documented in the [Developer mode guide](https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt).

### 6. Use it in a chat

Open a new ChatGPT conversation and enable the Codebridge app from the tools menu. Then select a repository:

```text
workspace codebridge
```

Use a different workspace in another chat:

```text
workspace loyalty-api
```

ChatGPT receives an opaque workspace binding automatically. You do not need to copy or manage that token. Bindings expire after inactivity and are reset when Codebridge restarts; say `workspace <id>` again when needed.

## Which managed tunnel mode should I use?

| Mode | Local endpoint published as `main` | Use case |
|---|---|---|
| `fast` | `/mcp/session/fast` | Recommended default. Compact coding profile with high-value read, edit, command, Git, CodeGraph, and quality tools |
| `full` | `/mcp/session` | Memory, workflow, managed processes, approvals, diagnostics, security, and upstream MCP tools |

Enable only the Fast app for normal coding. Enable the Full app when the task genuinely requires its larger tool surface. Both use the same workspace-selection workflow and the same local daemon.

After changing tunnel definitions, IDs, modes, profiles, runtime keys, or enabled state, run `codebridge restart` and rescan the corresponding app's tools in ChatGPT.

## Workspaces

Running `codebridge` inside a Git repository starts the daemon and automatically registers that repository when necessary:

```bash
cd /path/to/repository
codebridge
```

Manage workspaces explicitly:

```bash
codebridge workspace add loyalty-api /path/to/loyalty-api
codebridge workspace add admin-web /path/to/admin-web \
  --extra-root /path/to/shared-contracts

codebridge workspace list
codebridge workspace status loyalty-api
codebridge workspace stop loyalty-api
codebridge workspace start loyalty-api
codebridge workspace compact loyalty-api --dry-run
codebridge workspace compact loyalty-api
codebridge workspace remove admin-web
```

Each workspace has isolated roots, policy, state, backups, approvals, tasks, memory scope, and process registry. All enabled workspaces share one daemon and one port; every enabled named tunnel has its own managed tunnel-client process.

## Local use without ChatGPT

Run only the local MCP server:

```bash
codebridge start \
  --workspace /path/to/repository \
  --no-tunnel \
  --background \
  --save
```

For foreground debugging:

```bash
codebridge serve --workspace /path/to/repository --no-tunnel
```

Local endpoints:

```text
http://127.0.0.1:8789/mcp/session/fast
http://127.0.0.1:8789/mcp/session
http://127.0.0.1:8789/mcp/fast
http://127.0.0.1:8789/mcp
http://127.0.0.1:8789/healthz
http://127.0.0.1:8789/internal/healthz
http://127.0.0.1:8789/admin/
```

## Admin UI

Codebridge includes a local administration console for the complete non-secret configuration, named-workspace registration and removal, directory browsing, workspace overrides, upstream MCP servers, memory, tool exposure, resource limits, and referenced secrets.

Open `http://127.0.0.1:8789/admin/`. When no local admin credential exists, the loopback-only first-run screen asks for a username and password, creates the account once, signs in, and opens the guided setup wizard. The wizard covers workspace, mode, policy, port, OpenAI tunnel ID, write-only Runtime API key, and optional memory settings.

The CLI remains available for account creation and administration:

```bash
codebridge admin set-password admin
codebridge restart
codebridge admin
```

The username and a salted one-way password hash are stored in the owner-only file `~/.codebridge/admin-auth.json`; the plaintext password is never persisted. The browser bootstrap endpoint uses exclusive create semantics and stops accepting setup after that file exists. The default URL is `http://127.0.0.1:8789/admin/`. The UI is embedded in the Codebridge binary and does not require a separate web server in production.

There is no browser password-recovery flow. If the password is forgotten, reset it from the local machine:

```bash
codebridge admin reset-password admin
```

Changing the username or password immediately invalidates existing Admin UI browser sessions. Check the configured account without revealing credential material with `codebridge admin status`.

Security boundaries are enforced by the Go server:

- Admin routes accept only loopback clients and `localhost` or loopback-IP Host headers.
- Every Admin API route except login status, login, and the create-only first-account endpoint requires an authenticated, bounded HttpOnly session cookie.
- Failed logins are throttled. The first account may be created once from the loopback UI; password changes, reset, and recovery remain local-CLI-only.
- Every write, including login and logout, requires an exact same-origin request and a CSRF token.
- Configuration and workspace-registry saves use revision checks to prevent overwriting concurrent changes.
- The workspace browser is directory-only, bounded, and confined to the current user's home directory; an existing absolute path may still be entered manually.
- Runtime tokens are excluded from JSON configuration.
- Referenced `.env` secrets are write-only; the UI can see only whether each value exists.
- Changes are persisted atomically and require a Codebridge restart before active runtimes use them.

The Admin UI can run the guided setup flow, open OpenAI tunnel and API-key settings in new tabs, persist a tunnel ID, store referenced secrets write-only, browse directories, register named workspaces, remove registrations, and optionally delete a workspace override file. Browser same-origin rules prevent Codebridge from reading or auto-filling OpenAI Platform pages, so generated IDs and keys must be pasted into the local inputs. Removal preserves repository files and workspace runtime state. Enable/disable, daemon restart, tunnel-client installation, and tunnel lifecycle remain explicit CLI operations; registry changes require a restart before active MCP endpoints are reconciled.

### Admin UI development

The production assets are committed under `internal/adminui/dist` so normal Go builds remain single-step. Node.js is needed only when modifying the frontend:

```bash
make admin-ui-check
make admin-ui
```

For a development server with API proxying:

```bash
cd web/admin
npm install
npm run dev
```

## Common commands

| Command | Purpose |
|---|---|
| `codebridge` | Start and auto-register the current Git repository |
| `codebridge setup` | Configure workspace, policy, tunnel, and optional memory |
| `codebridge restart` | Reconcile config and restart server/tunnel |
| `codebridge status --json` | Show endpoints, process IDs, and health |
| `codebridge doctor` | Check local server, tunnel, memory, and upstream MCP dependencies |
| `codebridge logs` | Print bounded server and tunnel log tails |
| `codebridge profile` | Regenerate the tunnel-client profile |
| `codebridge tunnel install` | Download and verify OpenAI tunnel-client |
| `codebridge admin` | Print the local Admin UI URL |
| `codebridge admin set-password [username]` | Create or replace the local Admin account |
| `codebridge admin reset-password [username]` | Reset the password and invalidate browser sessions |
| `codebridge admin status` | Show whether the local Admin account is configured |
| `codebridge key set` | Store the Runtime API key in `.env` |
| `codebridge config get` | Print the effective non-secret configuration |
| `codebridge state gc --dry-run` | Preview safe state cleanup |
| `codebridge help` | Show the complete CLI surface |

## Configuration and secrets

Persistent data lives under:

```text
~/.codebridge/
  admin-auth.json
  config.json          non-secret global configuration
  .env                 Runtime API key and referenced secrets
  workspaces.json      named-workspace registry
  workspaces/<id>/     named-workspace configuration overrides
  state/               logs, process state, audit, backups, approvals, caches
```

Set `CODEBRIDGE_HOME=/custom/path` to relocate the complete tree.

Configuration precedence:

```text
primary runtime: defaults → config.json → environment → CLI options
named runtime:   primary effective config → workspaces/<id>/config.json override → registry/listener ownership
```

Named workspace files are versioned partial overrides rather than full snapshots. A newly registered workspace normally stores only `schemaVersion`, `extraRoots`, and explicitly supplied workspace options, so later global memory, MCP, limits, and policy changes are inherited automatically. Objects merge recursively, arrays replace the inherited array, `false` remains an explicit override, and `null` removes an inherited key. Unknown fields and duplicate JSON keys are rejected instead of being silently ignored.

For example, this workspace uses another database URI while inheriting the global command, startup mode, timeouts, and tool policy:

```json
{
  "schemaVersion": 1,
  "mcpServers": {
    "postgres_prod": {
      "envRefs": {
        "DATABASE_URI": "POSTGRES_API_MCP_DATABASE_URI"
      }
    }
  }
}
```

To disable one inherited server for a workspace, use `"enabled": false`; to remove it from the effective map entirely, set that server entry to `null`. Existing full workspace config files remain valid, but every field present in them is treated as an explicit override. Use `codebridge workspace compact <id> --dry-run` to preview removal of values that merely duplicate the current global config, then rerun without `--dry-run` to persist the compact version. An explicit `extraRoots: []` is preserved so future global roots do not leak into that workspace.

Minimal tunnel-related configuration:

```json
{
  "workspace": "/path/to/repository",
  "mode": "safe",
  "policy": "balanced",
  "host": "127.0.0.1",
  "port": 8789,
  "noTunnel": false,
  "tunnelId": "tunnel_0123456789abcdef0123456789abcdef",
  "runtimeKeyEnv": "CONTROL_PLANE_API_KEY"
}
```

Runtime-only secrets can be provided through environment variables:

```bash
export CONTROL_PLANE_API_KEY="..."
export MCP_AUTH_TOKEN="..."
export AGENT_APPROVAL_TOKEN="..."
```

Do not commit `.env`, API keys, tunnel credentials, local state, or private workspace configuration.

## Optional upstream MCP servers

Codebridge can expose tools from trusted stdio or Streamable HTTP MCP servers under the same workspace policy and audit pipeline.

Example:

```json
{
  "mcpServers": {
    "postgres_prod": {
      "transport": "stdio",
      "command": "uvx",
      "args": ["postgres-mcp", "--access-mode=restricted"],
      "envRefs": {
        "DATABASE_URI": "POSTGRES_PROD_MCP_DATABASE_URI"
      },
      "required": false,
      "workspaceIds": ["codebridge"],
      "startupMode": "lazy",
      "policy": {
        "default": "approval",
        "readOnlyTools": ["list_schemas", "list_tables", "describe_table"]
      }
    }
  }
}
```

`workspaceIds` limits the server to named runtimes that actually use it. An empty list or `"*"` preserves the previous behavior of exposing it in every workspace.

`startupMode` controls readiness behavior:

- `eager` is the backward-compatible default and connects before the daemon becomes ready.
- `background` registers a cached tool contract immediately, then connects and refreshes it asynchronously.
- `lazy` registers a cached tool contract immediately and connects on the first tool call.

Required servers always behave as `eager`. The first `background` or `lazy` startup without a cache performs one eager discovery so Codebridge can publish typed tools; later starts use the cache. Deferred clients refresh `tools/list` after connecting, and the refreshed contract is applied on the next Codebridge restart.

Tool catalogs are stored owner-only under `~/.codebridge/state/upstream-mcp/catalogs`. They contain only bounded tool names, descriptions, input schemas, and annotations—not credentials, calls, arguments, results, or arbitrary upstream metadata. Codebridge retains at most 64 catalogs and prunes entries older than 90 days when saving a catalog.

Keep credentials in `.env` or the process environment, not `config.json`. Restart Codebridge after changing upstream servers so their tools are rediscovered.

Community MCP servers run as local code with your user's privileges. Pin versions and review them before enabling them.

## Optional project memory

Codebridge supports provider-neutral project memory with an agentmemory adapter. Enable it through:

```bash
codebridge setup
codebridge restart
codebridge doctor
```

Memory is historical context, not the current source of truth. Codebridge still verifies current files and repository state before editing. Raw source, patches, command output, and secrets are excluded from automatic memory capture.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for provider configuration, capture modes, queueing, retry behavior, and session scoping.

## State retention

Codebridge creates workspace state lazily and removes only regenerable or expired data:

- Repository index cache: 7 days.
- Terminal approvals: 30 days.
- Patch history: at most 50 batches, 30 days, and 256 MiB per workspace.
- One backup batch: at most 128 MiB.
- Server and tunnel logs: size-based rotation.

Durable notes, checkpoints, current tasks, decisions, and unknown files are preserved.

```bash
codebridge state gc --dry-run
codebridge stop
codebridge state gc
codebridge restart
```

## Security model

- Files and command working directories are confined to configured roots.
- Canonicalization blocks traversal and symlink/junction escapes.
- `safe` mode blocks destructive shell patterns and Git mutations.
- `strict` permits read and analysis operations only.
- `balanced` allows normal edits and requests exact approval for risky actions.
- `full` enables the complete workflow while catastrophic system commands remain blocked.
- A non-loopback local MCP listener requires a bearer token.
- Codebridge is not an operating-system sandbox; approved commands run with the current user's privileges.
- Audit arguments are redacted before being written locally.
- Raw workspace bindings are never persisted in audit, memory, health, or state.

## Troubleshooting

### The tunnel does not appear in ChatGPT

Check all four layers:

1. The tunnel includes the correct ChatGPT workspace scope.
2. Your ChatGPT operator and Runtime API key principal have Tunnels Read + Use.
3. `codebridge status` shows the tunnel online.
4. The ChatGPT workspace has Developer mode and custom MCP apps enabled.

Then run:

```bash
codebridge doctor
codebridge logs
codebridge profile
codebridge restart
```

Reopen **Settings → Apps → Create**, select the tunnel, and scan tools again.

### `missing Runtime API key`

```bash
codebridge key set
codebridge restart
```

### The app has an old tool list

ChatGPT may retain the discovered tool contract. Restart Codebridge after configuration changes, then rescan the app's tools or recreate the draft app.

### A workspace binding expired

In the same chat, select it again:

```text
workspace <id>
```

### Inspect local health

```bash
codebridge status --json
codebridge doctor --json
codebridge logs
```

## Development

```bash
go test ./...
go vet ./...
go build ./...
```

Useful project documents:

- [Architecture](docs/ARCHITECTURE.md)
- [Contributing](CONTRIBUTING.md)
- [Changelog](CHANGELOG.md)

## License

Codebridge is licensed under the [GNU Affero General Public License v3.0 or later](LICENSE).
