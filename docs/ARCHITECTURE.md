# Codebridge architecture

## 1. Design goals

Codebridge uses one binary for both the CLI supervisor and the MCP server. The CLI is responsible only for loading configuration and managing processes and tunnels; all tool business logic lives in `agent.Runtime`, so foreground servers, background servers, and tests share the same policy and state implementation.

Primary goals:

1. Provide a single MCP gateway for workspaces, CodeGraph, Figma, and memory.
2. Maintain a stable tool contract that is independent of any specific backend.
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
   └── 91 registered tools
          │
          ▼
agent.Runtime.HandleSession
   ├── normalize arguments
   ├── enforce policy / consume exact approval
   ├── dispatch to the tool-group handler
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
| `internal/agent` | 91-tool registry, shared runtime, policy, and tool handlers |
| `internal/workspace` | Canonical paths, configured roots, owning-root resolution, list/search/tree |
| `internal/security` | Shell and Git guards, risk classification, approvals, and redaction |
| `internal/patch` | Backup batches, structured operations, unified diffs, and undo |
| `internal/processx` | Timeouts, output caps, and managed process trees |
| `internal/figma` | MCP client bridge to Figma Desktop |
| `internal/memory` | Canonical contracts, project identity, asynchronous recorder, and adapters |
| `internal/database` | Alias routing, shared `database/sql` execution core, driver dialects, limits, and masking |
| `internal/state` | Per-workspace notes, tasks, decisions, audit, index, and backups |
| `internal/assets` | Embedded MCP Apps widget and built-in skills |

Important dependency direction:

```text
mcpserver → agent → memory.Provider
                    ↑
             provider adapters
```

`agent` does not import `agentmemory`; the concrete adapter is selected only through `memory/factory`.

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
  Figma configuration, memory configuration, limits

.env
  CONTROL_PLANE_API_KEY
  optional memory-provider secret
```

`Config.Save` clears `AuthToken` and `ApprovalToken` before serialization. The memory secret is not part of `MemoryConfig`; the configuration stores only the environment variable name in `secretEnv`.

Memory-provider options are validated recursively. Keys containing `secret`, `password`, `token`, `apikey`, `authorization`, or `credential` are rejected to prevent credentials from being stored in JSON.

### Configuration identity and process reuse

The supervisor creates `ConfigID` from:

- workspace and extra roots;
- mode, policy, port, and whether authentication is enabled;
- binary hash and widget hash;
- Figma endpoint;
- all non-secret memory configuration;
- a shortened fingerprint of the memory secret.

When the health endpoint reports the same `ConfigID`, the supervisor reuses the existing server. Changing the memory agent ID, retry configuration, provider options, or secret changes `ConfigID` and causes a new server to be created.

## 5. HTTP and MCP layers

Codebridge exposes:

```text
/mcp                 public MCP endpoint
/healthz             public health endpoint
/internal/healthz    supervisor health with PID and config ID
```

A non-loopback `host` requires an MCP bearer token. A browser Origin is accepted only when it is loopback or included in the explicit allowlist.

`agent.Tools()` is the single source of truth for:

- name;
- title and description;
- JSON input schema;
- read-only and destructive annotations;
- MCP Apps metadata.

`internal/mcpserver` converts a tool result into both JSON text and `structuredContent` when the output is an object. Errors are returned as `CallToolResult{IsError:true}` rather than becoming protocol failures.

## 6. Runtime pipeline and policy

Every tool request follows this pipeline:

```text
argument normalization
  → enforcePolicy
  → dispatch
  → audit
  → captureMemoryObservation
```

When policy rejects a request, the runtime still records an audit failure but does not capture a post-tool observation because the tool did not run.

### Policies

- `strict`: blocks mutation tools.
- `balanced`: allows edits but requires exact one-time approval for risky actions.
- `full`: does not require ordinary approvals, although command guards still block catastrophic operations.

An approval action includes the exact target or exact arguments. `memory_forget` serializes its arguments into the approval action, so an approval cannot be reused for a different memory.

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

Memory tools, `ping`, and `proc_output` are excluded from automatic capture to prevent recursion and output leakage.

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
4. Command `cwd` is root-confined, but command execution is not an operating-system sandbox.
5. Raw Git blocks flags that can write arbitrary output, change the worktree, or execute an external program.
6. The balanced policy uses exact, expiring, one-time approvals.
7. Audit arguments are recursively redacted.
8. Automatic memory capture does not send raw source, patches, commands, stdout, or raw errors.
9. Provider options cannot contain secret-like keys.
10. Browser Origins are blocked by default unless they are loopback or explicitly allowed.
11. A non-loopback MCP listener requires a bearer token.
12. HTTP responses from memory providers are size-limited before decoding.

## 11. Testing strategy

Contract tests lock down:

- tool count and unique dispatch groups;
- MCP in-memory round trips;
- session-ID propagation;
- path traversal and root-deletion rejection;
- secret separation and `.env` permissions;
- ConfigID sensitivity;
- provider REST mapping and response normalization;
- behavior without an authentication secret;
- context fallback;
- project normalization and owning-root behavior;
- recorder delivery, retry, drain, and dropped counters;
- required and optional provider startup behavior;
- export filtering and canonical import.

Recommended verification:

```bash
go test ./...
go test -race ./internal/memory/... ./internal/agent ./internal/cli ./internal/config ./internal/mcpserver
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
