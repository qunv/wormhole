# Codebridge architecture

## 1. Design goals

Codebridge uses one binary for both the CLI supervisor and the MCP server. The CLI is responsible only for loading configuration and managing processes and tunnels. Tool business logic is organized into `agent.ToolModule` implementations coordinated by `agent.Runtime`, so foreground servers, background servers, and tests share the same policy, identity, audit, and state pipeline.

Primary goals:

1. Provide a single MCP gateway for workspaces, CodeGraph, memory, and configured community MCP servers, including database and design integrations.
2. Maintain a stable built-in tool contract while safely namespacing tool contracts discovered from upstream MCP servers.
3. Enforce path confinement and policy checks before every mutation.
4. Keep secrets separate from persistent non-secret configuration.
5. Make memory fail open by default so it does not slow down the coding workflow.
6. Allow providers to be added and memory to be migrated without changing the model's workflow.

## 2. Overall control flow

```text
CLI command
   │
   ├── setup/config/key/tunnel
   │      ├── config.json (non-secret)
   │      └── .env (runtime secrets)
   │
   ├── start/stop/restart/status/doctor
   │      ├── process state
   │      ├── config ID
   │      └── health checks
   │
   └── serve
          │
          ▼
HTTP server
   ├── body limit
   ├── bearer authentication
   ├── Origin/CORS guard
   └── health endpoints
          │
          ▼
Official MCP Go SDK
   ├── Streamable HTTP
   ├── logical ServerSession
   ├── embedded Apps resource
   └── 78 built-in tools plus discovered upstream tools
          │
          ▼
agent.Runtime.HandleSession
   ├── normalize arguments and build CallIdentity
   ├── enforce policy / consume exact approval
   ├── O(1) lookup in the tool-to-module registry
   ├── ToolModule.Handle(ctx, identity, tool, args)
   ├── append a redacted audit record
   └── enqueue a redacted memory observation
```

`Runtime.Handle` remains available for internal callers and tests and uses a fallback session identity. MCP requests go through `HandleSession` to preserve logical connection identity.

## 3. Package boundaries

| Package | Responsibility |
|---|---|
| `cmd/codebridge` | Process entrypoint and exit codes |
| `internal/app` | Version/tier metadata and composition root |
| `internal/cli` | CLI grammar, setup, process lifecycle, tunnel installation, and profile generation |
| `internal/server` | HTTP routing, health, authentication, Origin/CORS, and body limits |
| `internal/mcpserver` | MCP server construction, session identity, widget resource, and result adapter |
| `internal/agent` | Tool-module registry, shared runtime pipeline, policy, identity, audit, and functional module handlers |
| `internal/workspace` | Canonical paths, configured roots, owning-root resolution, list/search/tree |
| `internal/security` | Shell and Git guards, risk classification, approvals, and redaction |
| `internal/patch` | Backup batches, structured operations, unified diffs, and undo |
| `internal/processx` | Timeouts, output caps, and managed process trees |
| `internal/upstreammcp` | Generic long-lived MCP client sessions for command/stdio and Streamable HTTP transports |
| `internal/memory` | Canonical contracts, project identity, asynchronous recorder, and adapters |
| `internal/state` | Per-workspace notes, tasks, decisions, audit, index, and backups |
| `internal/assets` | Embedded MCP Apps widget and built-in skills |

Important dependency direction:

```text
mcpserver → agent.Runtime → ToolModule
                              ├── memory.Provider ← provider adapters
                              ├── upstreammcp.Client ← stdio / Streamable HTTP MCP servers
                              └── workspace / process / state services
```

`agent` does not import `agentmemory`; the concrete adapter is selected only through `memory/factory`.

### 3.1 Tool module registry

The MCP runtime is extended through one interface:

```go
type ToolModule interface {
    Name() string
    Specs() []ToolSpec
    Handle(context.Context, CallIdentity, string, map[string]any) (any, error)
    Health(context.Context) any
    Close() error
}
```

`Runtime` keeps both a module registry and an O(1) tool-to-module index. Registration rejects empty modules, invalid specifications, duplicate module names, and duplicate tool names before mutating the registry. Registration is protected by an RW lock; handler, health, and close calls run without holding the registry lock.

The built-in functional modules are:

```text
basic
filesystem
repo
workflow
memory
execution
```

Database and Figma are intentionally not built-in modules. They use the same configured `mcpServers` path as every other community integration, so Codebridge does not embed SQL drivers, database credentials, or a Figma-specific MCP client.

Each module owns its tool specifications, group-local routing, health result, and lifecycle behavior. Cross-cutting concerns remain in `Runtime.HandleSession`: exact approvals, audit redaction, and memory observation capture. `CallIdentity` currently carries the logical MCP session ID and can be extended without coupling modules to the MCP SDK. Strict policy derives mutation status from `ToolSpec.ReadOnly`, so newly registered write tools cannot bypass it. Modules can optionally implement `ToolPolicyProvider`; otherwise every external tool with `ReadOnly=false` receives a hashed exact-argument approval action under balanced policy. `Runtime.Shutdown()` closes modules in reverse registration order and returns aggregated close errors; `Runtime.Close()` remains as the compatibility wrapper.

`internal/mcpserver` enumerates `runtime.Tools()` rather than a static global catalog. External modules must be registered with `runtime.RegisterModule` before constructing the MCP server. The package-level `agent.Tools()` function remains only as a compatibility catalog for existing callers and tests.

Configured `mcpServers` entries are materialized during `Runtime.NewContext` after the six built-in modules and before downstream MCP server construction. Each entry creates one `upstreamMCPModule` named `mcp_<server>`. The entry name is the integration's only public identity: startup opens a long-lived MCP client session, calls paginated `tools/list`, validates and bounds the returned contract, filters tools, namespaces names as `<server>__<normalized_name>`, and registers the assembled `ToolSpec` values. Tool-list changes are therefore applied on Codebridge restart rather than mutating the downstream contract in place.

The generic client supports:

```text
stdio              mcp.CommandTransport around executable/argv; Windows batch launchers use a restricted cmd.exe adapter
streamable-http    mcp.StreamableClientTransport with explicit headers
```

A stdio child receives only a small platform environment plus explicitly configured `inheritEnv`, non-secret `env`, and secret `envRefs` values. stdout remains reserved for MCP framing; stderr is counted and bounded as health metadata. Streamable HTTP endpoints are loopback-only unless `allowRemote=true`. Sessions are reused across calls and closed by module shutdown. A failed read-only call may reconnect and retry once; mutation calls reconnect for subsequent work but are never replayed automatically.

Optional module interfaces keep untrusted upstream data outside shared persistence boundaries:

```go
type ToolAuditProvider interface {
    AuditArguments(string, map[string]any) any
    AuditMetadata(string, map[string]any, any) map[string]any
    AuditError(error) string
}

type ToolObservationPolicyProvider interface {
    CaptureObservation(string) bool
}
```

Community MCP modules store only argument names and bounded result metadata in local audit and opt out of automatic long-term memory capture.

## 4. Configuration lifecycle

Configuration is assembled in this order:

```text
Default()
  → JSON unmarshal from config.json
  → environment overrides
  → normalize + validate
  → CLI options for the current invocation
```

### Configuration locations

| Operating system | Config directory |
|---|---|
| Linux | `$XDG_CONFIG_HOME/codebridge`, falling back to `~/.config/codebridge` |
| macOS | `~/Library/Application Support/Codebridge` |
| Windows | `%APPDATA%\Codebridge` |

Runtime state uses the application data/state directory:

| Operating system | State directory |
|---|---|
| Linux | `$XDG_STATE_HOME/codebridge`, falling back to `~/.local/state/codebridge` |
| macOS | `~/Library/Application Support/Codebridge` |
| Windows | `%LOCALAPPDATA%\Codebridge` |

### Secret ownership

```text
config.json
  workspace, mode, policy, tunnel metadata,
  memory and upstream MCP configuration, limits,
  and environment-variable references rather than secret values

.env
  CONTROL_PLANE_API_KEY
  optional memory-provider and upstream MCP secrets
```

`Config.Save` clears `AuthToken` and `ApprovalToken` before serialization. The memory secret is not part of `MemoryConfig`; the configuration stores only the environment variable name in `secretEnv`.

Memory-provider options are validated recursively. Keys containing `secret`, `password`, `token`, `apikey`, `authorization`, or `credential` are rejected to prevent credentials from being stored in JSON.

Upstream MCP configuration follows the same separation. Sensitive environment variables use `envRefs`, and sensitive HTTP headers use `headerRefs`; both map a child-facing name to a source environment-variable name. Direct secret-like keys, sensitive `inheritEnv` entries, credential-bearing URLs, control bytes, transport-incompatible fields, conflicting policy lists, and case-insensitive header collisions are rejected during configuration validation.

### Configuration identity and process reuse

The supervisor creates `ConfigID` from:

- workspace and extra roots;
- mode, policy, port, and whether authentication is enabled;
- binary hash and widget hash;
- all non-secret memory configuration and a shortened fingerprint of the memory secret;
- all non-secret upstream MCP configuration and shortened fingerprints of referenced environment/header secrets.

When the health endpoint reports the same `ConfigID`, the supervisor reuses the existing server. Changing memory or upstream server configuration, package arguments, tool policy, environment references, or referenced secret values changes `ConfigID` and causes a new server to be created.

## 5. HTTP and MCP layers

Codebridge exposes:

```text
/mcp                 public MCP endpoint
/healthz             public health endpoint
/internal/healthz    supervisor health with PID and config ID
```

A non-loopback `host` requires an MCP bearer token. A browser Origin is accepted only when it is loopback or included in the explicit allowlist.

Each module's `Specs()` output is the source of truth for its tools, and `runtime.Tools()` is the assembled server contract:

- name;
- title and description;
- JSON input schema;
- read-only and destructive annotations;
- MCP Apps metadata;
- functional ownership through the module name.

`internal/mcpserver` converts a tool result into both JSON text and `structuredContent` when the output is an object. Errors are returned as `CallToolResult{IsError:true}` rather than becoming protocol failures.

## 6. Runtime pipeline and policy

Every tool request follows this pipeline:

```text
argument normalization + CallIdentity
  → enforcePolicy
  → toolModules[tool].Handle
  → audit
  → captureMemoryObservation
```

When policy rejects a request, the runtime still records an audit failure but does not capture a post-tool observation because the tool did not run.

### Policies

- `strict`: blocks every tool whose assembled `ToolSpec.ReadOnly` is false, including upstream tools not explicitly classified as read-only.
- `balanced`: allows edits but requires exact one-time approval for risky actions and upstream tools classified as `approval` or `always-approval`.
- `full`: does not require ordinary approvals, although command guards still block catastrophic operations and upstream `always-approval` tools remain gated.

An approval action includes the exact target or a hash of the complete tool-plus-arguments payload. `memory_forget` serializes its arguments into the approval action, while generic and upstream write tools use a bounded SHA-256 action so approval cannot be reused for different arguments and raw payloads are not exposed in the action string.

## 7. Workspace identity and confinement

`workspace.Manager` stores:

```text
Primary
Roots
realRoots
Skips
RGBin
```

`Resolve` performs these steps:

1. Resolve a relative path from the primary root.
2. Find the longest existing ancestor.
3. Canonicalize that ancestor through symlinks.
4. Reattach the non-existent tail of the path.
5. Verify that the canonical path remains inside a configured real root.

This prevents even non-existent targets from escaping through symlinks.

### Owning root

A resolved path can belong to the primary root or an extra root. `OwningRoot` selects the most specific configured root containing that path. Project identity, especially under `path-hash`, is always computed from the owning root rather than from a subdirectory or file.

Example:

```text
configured root: /repo
request path:    /repo/internal/memory/provider.go
project root:    /repo
```

This prevents memory from being fragmented by `cwd` or subdirectory.

## 8. Memory architecture

### 8.1 Canonical contract

`memory.Provider` defines the core operations:

```text
Health
Search
Context
Remember
Observe
Forget
Close
```

Optional interfaces:

```text
memory.Exporter
memory.Importer
```

Outputs do not expose raw backend payloads. Adapters normalize results into:

- `Item`;
- `SearchResult`;
- `ContextResult`;
- `RememberResult`;
- `ForgetResult`;
- `ExportResult`;
- `ImportResult`.

`ProviderID` preserves the backend identifier, while MCP clients depend only on canonical fields.

### 8.2 Provider registry

`memory/factory` uses a registry:

```go
factory.Register("provider-name", constructor)
```

Built-in registrations:

```text
none
agentmemory
```

When memory is disabled or the provider is `none`, the factory returns the no-op provider. Adding another backend does not require changes to the runtime or MCP tool registry.

### 8.3 Agentmemory adapter

The adapter uses these default REST endpoints:

```text
GET  /agentmemory/health
GET  /agentmemory/config/flags
POST /agentmemory/search
POST /agentmemory/context
POST /agentmemory/remember
POST /agentmemory/observe
POST /agentmemory/forget
GET  /agentmemory/export
```

Paths and the response-size limit can be overridden through `memory.options`.

The bearer header is sent only when the variable named by `secretEnv` contains a value.

Health capabilities begin with the features supported by the adapter and are then adjusted using the health and flags responses when the backend publishes feature state. Context retrieval combines session context with query search; when the context endpoint is unsupported, the adapter falls back to narrative search.

### 8.4 Project identity

Two strategies are available:

```text
git-origin
path-hash
```

`git-origin` reads `remote.origin.url`, supports HTTPS, SSH, and SCP-like URLs, strips credentials, removes `.git`, and lowercases the result:

```text
git@github.com:Owner/Repo.git
https://github.com/Owner/Repo.git
       ↓
git:github.com/owner/repo
```

When the Git origin does not exist or is invalid, the resolver falls back to:

```text
workspace:<sha256-prefix-of-owning-root>
```

`path-hash` always uses this fallback form.

### 8.5 Session identity

The MCP layer derives identity from `ServerSession`:

```text
mcp:<protocol-session-id>
```

Some transports do not assign a protocol ID. In that case, Codebridge hashes the process-local identity of the stable session object:

```text
mcp-local:<hash>
```

The process-level fallback session is used only by internal callers that do not go through MCP. Concurrent MCP connections therefore remain separate.

### 8.6 Retrieval semantics

`memory_context` uses:

- task query;
- project ID;
- owning-root `cwd`;
- agent ID;
- MCP session ID;
- token budget.

An adapter can use a session-context endpoint, a search endpoint, or both. The result still follows the canonical `ContextResult` schema.

Memory is historical evidence. MCP instructions require the model to verify implementation details with CodeGraph or current files before editing.

### 8.7 Explicit writes

`memory_remember` stores durable memory with a kind, concepts, files, and optional TTL.

`memory_commit` can automatically assemble:

- a caller-provided summary;
- the current task plan;
- the checkpoint;
- Git change summary;
- review result;
- touched files;
- next steps.

The resulting memory is associated with the project, agent, and MCP session identity.

### 8.8 Automatic capture

Capture modes:

```text
off
selected
metadata
```

`selected` captures only tool groups with durable historical value: notes and checkpoints, plans and decisions, file mutations, Git, tests/build/lint, quality gates, CodeGraph, reviews, and session reports.

`metadata` captures a broader set of calls but keeps only fields such as path, `cwd`, staged, recursive, and kind.

Memory tools, community upstream MCP modules, `ping`, and `proc_output` are excluded from automatic capture to prevent recursion and untrusted payload leakage.

Before enqueueing:

1. Input passes through recursive redaction.
2. Git retains only the operation and argument count, not remote or authentication arguments.
3. Results are reduced to whitelisted fields.
4. Failures store a generic message; the raw error remains in the local audit log.

### 8.9 Recorder delivery guarantees

The recorder uses a bounded channel and non-blocking enqueue:

```text
queue available → enqueued
queue full      → dropped++, tool response is not blocked
```

Worker delivery:

```text
per-attempt timeout
  → exponential retry
  → maximum attempts
  → delivered or failed
```

Backoff is capped at two seconds. During shutdown:

1. `closed=true` rejects new records.
2. The worker receives the stop signal.
3. The current queue is drained.
4. The provider closes after the recorder.

Recorder statistics:

```text
queue_depth
queue_capacity
enqueued
delivered
retried
failed
dropped
```

This provides at-most-once enqueue with bounded retry delivery; there is no durable local spool. A process crash can still lose observations that have not been delivered.

### 8.10 Health and fail-open behavior

With `memory.required=false`:

- the runtime starts even when the provider is offline;
- memory tool calls may return errors;
- coding tools continue to work;
- asynchronous failures only increment recorder counters.

With `memory.required=true`:

- startup calls provider health;
- an unavailable provider causes runtime creation to fail.

Health is cached according to `healthCacheMs` so `workspace_snapshot` and `workspace_doctor` do not continuously call the backend.

### 8.11 Migration boundary

`memory_export` returns canonical schema version 1 as an object or JSONL.

An agentmemory export can contain multiple projects; the adapter normalizes it and filters it to the requested project. `memory_import` replays each canonical item through the provider's `Remember` operation instead of restoring a raw database dump.

Trade-offs:

- portable between providers;
- independent of database schema;
- provider-specific fields are retained only in bounded metadata;
- imports may not preserve internal embeddings or graph identifiers.

## 9. Local state ownership

Per-workspace state is stored under:

```text
workspaces/<workspace-id>/
  notes.json
  checkpoint.json
  current-task.json
  decisions.md
  audit.jsonl
  index.json
  patch-history.json
  backups/
  approvals/
```

The workspace ID is derived from the canonical primary workspace path. Memory-provider data is not stored in the local state directory, except for audit records and counters associated with tool calls.

## 10. Security invariants

1. Every file path is resolved inside configured roots.
2. The longest existing ancestor is canonicalized before the containment check.
3. A configured root cannot be deleted or renamed through dedicated tools or patch operations.
4. Command `cwd`, including upstream stdio `cwd`, is root-confined, but command execution and community MCP servers are not operating-system sandboxes.
5. Native upstream commands are executed directly as executable plus argv. On Windows only, `.cmd`/`.bat` launchers use a restricted `cmd.exe` adapter that rejects quote, control, `%`, and `!` expansion characters.
6. Stdio children do not inherit the complete Codebridge environment. Sensitive values require explicit `envRefs`; sensitive HTTP headers require `headerRefs`.
7. Remote upstream HTTP endpoints are blocked unless `allowRemote=true`.
8. Upstream tool names, counts, metadata, and schemas are bounded; normalization collisions and duplicate tools fail startup.
9. Raw Git blocks flags that can write arbitrary output, change the worktree, or execute an external program.
10. The balanced policy uses exact, expiring, one-time approvals, and upstream annotations are untrusted unless explicitly enabled.
11. Audit arguments are recursively redacted; upstream modules persist only argument names and bounded metadata.
12. Automatic memory capture does not send raw source, patches, commands, stdout, raw errors, or upstream MCP payloads.
13. Provider options and upstream persistent configuration cannot contain secret-like keys.
14. Browser Origins are blocked by default unless they are loopback or explicitly allowed.
15. A non-loopback MCP listener requires a bearer token.
16. HTTP responses from memory providers and upstream tool contracts are size-limited before processing.

## 11. Testing strategy

Contract tests lock down:

- tool count and unique module ownership;
- duplicate module/tool rejection and close-once lifecycle;
- MCP in-memory round trips, runtime module enumeration, and session-ID propagation;
- path traversal and root-deletion rejection;
- secret separation and `.env` permissions;
- ConfigID sensitivity;
- provider REST mapping and response normalization;
- behavior without an authentication secret;
- context fallback;
- project normalization and owning-root behavior;
- recorder delivery, retry, drain, and dropped counters;
- required and optional provider startup behavior;
- export filtering and canonical import;
- generic stdio and Streamable HTTP upstream MCP discovery and calls;
- environment isolation, header forwarding, remote opt-in, and idempotent shutdown;
- upstream namespace collisions, schema validation, policy mapping, audit minimization, memory exclusion, and downstream gateway round trips.

Recommended verification:

```bash
go test ./...
go test -race ./internal/memory/... ./internal/upstreammcp ./internal/agent ./internal/cli ./internal/config ./internal/mcpserver
go vet ./...
go build ./...
GOOS=windows GOARCH=amd64 go build ./...
GOOS=darwin GOARCH=arm64 go build ./...
```

## 12. Release

`.goreleaser.yml` builds binaries for:

- Linux amd64/arm64;
- macOS amd64/arm64;
- Windows amd64/arm64.

The version is injected through:

```text
-X codebridge/internal/app.Version=<version>
```

The MCP tool contract is a public compatibility surface. Replacing a provider adapter should not change tool names or canonical result fields; breaking contract changes require explicit versioning and migration.
