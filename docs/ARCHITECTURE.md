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
   ├── /mcp/session → SessionRouter → Runtime[binding.workspace]
   ├── /mcp → Runtime[default]
   ├── /mcp/workspaces/<id> → Runtime[id]
   └── health endpoints
          │
          ▼
Official MCP Go SDK
   ├── Streamable HTTP
   ├── logical ServerSession
   ├── embedded Apps resource
   └── 75 built-in tools plus discovered upstream tools
          │
          ▼
selected agent.Runtime.HandleSession
   ├── allocate bounded correlation ID and increment in-flight telemetry
   ├── normalize arguments and build CallIdentity
   ├── enforce policy / consume exact approval
   ├── O(1) lookup in the tool-to-module registry
   ├── ToolModule.Handle(ctx, identity, tool, args)
   ├── append a correlated redacted audit record
   ├── enqueue a correlated redacted memory observation
   └── finalize latency, outcome, audit, and enqueue/drop metrics
```

`Runtime.Handle` remains available for internal callers and tests and uses a fallback session identity. MCP requests go through `HandleSession` to preserve logical connection identity.

## 3. Package boundaries

| Package | Responsibility |
|---|---|
| `cmd/codebridge` | Process entrypoint and exit codes |
| `internal/app` | Version/tier metadata and composition root |
| `internal/cli` | CLI grammar, setup, process lifecycle, tunnel installation, and profile generation |
| `internal/workspaceregistry` | Named workspace registry, schema migration, atomic persistence, and daemon fingerprint input |
| `internal/server` | HTTP routing, health, authentication, Origin/CORS, and body limits |
| `internal/mcpserver` | MCP server construction, per-chat workspace bindings, session identity, widget resource, and result adapter |
| `internal/agent` | Tool-module registry, shared runtime pipeline, policy, identity, audit, and functional module handlers |
| `internal/workspace` | Canonical paths, configured roots, owning-root resolution, list/search/tree |
| `internal/security` | Shell and Git guards, risk classification, approvals, and redaction |
| `internal/patch` | Backup batches, structured operations, unified diffs, and undo |
| `internal/processx` | Timeouts, output caps, and managed process trees |
| `internal/upstreammcp` | Generic long-lived MCP client sessions for command/stdio and Streamable HTTP transports |
| `internal/memory` | Canonical contracts, project identity, asynchronous recorder, and adapters |
| `internal/state` | Per-workspace notes, tasks, decisions, audit, index, and backups |
| `internal/assets` | Embedded MCP Apps widget |

Important dependency and ownership direction:

```text
mcpserver → agent.Runtime[id] → workspace-local ToolModule handlers
                  │                ├── workspace / process / state services
                  │                ├── approvals / patches / profile
                  │                └── memory project + session scope
                  │
                  └── SharedServices (daemon owner)
                         ├── memory.Provider pool ← provider adapters
                         ├── memory.Recorder pool
                         ├── upstreammcp.Client/session pool
                         └── immutable upstream contract pool
```

`agent` does not import `agentmemory`; the concrete adapter is selected only through `memory/factory`. Standalone runtimes create and own a private `SharedServices`; the multi-workspace daemon injects one shared instance and closes it only after every runtime has shut down.

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

Each runtime keeps workspace-local module handlers, health views, policy, audit, and memory-scope behavior. Cross-cutting concerns remain in `Runtime.HandleSession`: exact approvals, audit redaction, and memory observation capture. `CallIdentity` carries both the logical MCP session ID and fixed workspace ID without coupling modules to the MCP SDK. Strict policy derives mutation status from `ToolSpec.ReadOnly`, so newly registered write tools cannot bypass it. Modules can optionally implement `ToolPolicyProvider`; otherwise every external tool with `ReadOnly=false` receives a hashed exact-argument approval action under balanced policy.

Built-in module contracts are immutable and constructed once per process through `sharedModuleSpecs`; each runtime still has its own tool-to-handler index. Fixed-workspace and session-routing `mcp.Server` instances are also assembled once when the HTTP daemon is created and are reused for each stateless request. `Runtime.Shutdown()` closes workspace-local modules in reverse registration order. Memory and upstream module `Close()` methods intentionally do not close pooled resources. The daemon closes `SharedServices` after all runtimes, which flushes audit writers, drains recorders, and then closes providers and upstream clients exactly once.

`internal/mcpserver` enumerates `runtime.Tools()` rather than a static global catalog. External modules must be registered with `runtime.RegisterModule` before constructing the MCP server. The package-level `agent.Tools()` function remains only as a compatibility catalog for existing callers and tests.

Managed process stdout and stderr use fixed-capacity circular byte buffers. Writes remain O(chunk size) after the buffer fills instead of shifting the retained 200 KiB tail on every log chunk, and `proc_output(tail_chars)` copies only the requested logical tail across at most two ring segments.

Repository diagnostics share a runtime-local inventory cache keyed by owning root and mutation generation. One bounded, sorted, context-aware traversal supplies tree views, project profile, important files, and symbol-cache keys. Depth/limit/symbol combinations are retained as a small LRU-like view set; the latest compatible view is also persisted to `index.json` for restart diagnostics. Successful filesystem, patch, arbitrary shell, managed-process start, and mutating-Git operations increment the generation and discard repository and Git snapshots. External edits are bounded by the five-minute inventory TTL. Git status uses one `--porcelain=v2 --branch` process and a 300 ms cache. Pattern scans use streaming tracked-file `git grep`, cancel the subprocess at the result limit, and fall back to a bounded tracked-file stream or filesystem scan.

Configured `mcpServers` entries are materialized after the six built-in modules. `SharedServices` first resolves a connection key from server name, transport settings, timeouts, referenced-secret fingerprints, and—only for stdio—the confined resolved `cwd`. Compatible runtimes reuse one long-lived `upstreammcp.Client` and MCP session. Tool filtering and approval policy form a separate contract key; policy-only differences therefore create different `upstreamMCPModule` contracts while sharing the same client. Startup performs paginated `tools/list`, validates and bounds the returned contract, namespaces names as `<server>__<normalized_name>`, and caches the immutable assembled `ToolSpec` values. Tool-list changes are applied on Codebridge restart rather than mutating the downstream contract in place.

The generic client supports:

```text
stdio              mcp.CommandTransport around executable/argv; Windows batch launchers use a restricted cmd.exe adapter
streamable-http    mcp.StreamableClientTransport with explicit headers
```

A stdio child receives only a small platform environment plus explicitly configured `inheritEnv`, non-secret `env`, and secret `envRefs` values. stdout remains reserved for MCP framing; stderr is counted and bounded as health metadata. Streamable HTTP endpoints are loopback-only unless `allowRemote=true`. A pooled client allows concurrent calls on the SDK session while serializing only connect/reconnect lifecycle transitions. A per-client semaphore bounds calls at `maxConcurrency` and records queued, in-flight, peak, completed, context-rejected, and circuit-rejected counts. One request's cancellation or deadline does not invalidate a healthy shared session. Transport failures detach only the session generation that failed; reference-counted retired sessions close only after every active borrower releases them, so reconnect cannot interrupt another call. A failed read-only call may reconnect and retry once; mutation calls reconnect for subsequent work but are never replayed automatically.

Health checks are single-flight and cached for `healthCacheMs` across every workspace sharing the client. Three consecutive transport or reconnect failures open a cooldown circuit for `failureCooldownMs`; reconnect attempts fail fast during that window and resume afterward. A successful reconnect or call resets the consecutive-failure state. Client health exposes only bounded counters, timestamps, transport identity, sanitized errors, and a hostname or executable basename—not headers, arguments, results, or secret values.

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

All persistent Codebridge files use one canonical root on every operating system:

```text
~/.codebridge/
  config.json
  .env
  workspaces.json
  workspaces/<id>/config.json
  state/
    processes.json
    server.log
    tunnel.log
    profiles/
    instances/<id>/...
    workspaces/<workspace-path-hash>/...
```

`CODEBRIDGE_HOME` relocates the entire tree. The older granular overrides remain available: `CODEBRIDGE_CONFIG_PATH` changes the primary config file, `CODEBRIDGE_DATA_DIR` changes the state root, and `CODEBRIDGE_WORKSPACE_REGISTRY_PATH` changes the registry file.

With the default layout, startup performs a one-time copy migration from the former XDG, Application Support, or AppData locations. Existing canonical files are never overwritten, legacy files are retained, and a completion marker prevents those retained backups from resurrecting canonical files removed after migration. Registry schema version 3 also rewrites default absolute paths from the previous layout while preserving explicitly customized paths.

Workspace-specific command conventions, ignored directories, and profile metadata live in `<workspace>/.codebridge/profile.json`. `<workspace>/.agent/profile.json` remains a compatibility fallback when the canonical profile does not exist.

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

### Named workspace endpoints

The multi-workspace daemon keeps one supervisor process, listener, and optional tunnel while creating a fully isolated `agent.Runtime` for every enabled workspace registration:

```text
workspaces.json
  <id> → workspace root, config path, data directory, enabled

workspaces/<id>/config.json
  non-secret workspace runtime configuration

shared daemon
  /mcp/session
    → SessionRouter[binding] → Runtime[selected]
  /mcp
    → Runtime[default]
  /mcp/workspaces/<id>
    → Runtime[id]
```

`internal/workspaceregistry` owns schema validation, stable ordering, atomic persistence, schema migrations through version 3, and the registry/config fingerprint used by the supervisor. The ID `default` remains reserved for named registrations, and two registrations cannot share a config path or data directory. Registry identity is authoritative for the workspace root. Named configs are loaded without ambient environment overrides, then the daemon copies global listener security fields such as host, port, bearer token, approval token, and allowed Origins from the primary config. Process-level `AGENT_WORKSPACE` or `PORT` values therefore cannot repoint a named endpoint.

Each runtime owns a separate workspace manager, state store, approval manager, patch engine, managed-process registry, profile, memory project, workspace-prefixed session identity, policy, request limits, and workspace-local handlers. The primary runtime uses the application state directory; named runtimes use `instances/<id>` as their state root. This keeps notes, tasks, audit, approvals, backups, patch history, and managed processes isolated even when two registrations point at repositories with similar contents.

The daemon owns one `SharedServices` instance. Compatible memory configurations reuse a provider and recorder; incompatible backend, secret, or delivery settings produce separate pooled entries. Runtimes that share an audit path also share one bounded batching writer. Compatible upstream connection configurations reuse a client/session, while policy and tool-filter differences retain separate contracts. `workspace_info`, `memory_status`, and `/internal/healthz` publish bounded `shared_resources` counts and acquire/reuse counters without exposing resource keys or secrets.

The MCP adapter prefixes workspace session IDs with `workspace:<id>:` and places the same workspace ID in `CallIdentity`. Audit records include both `workspace_id` and `session_id`. Exact approval actions are prefixed with the workspace ID before they are stored or consumed, preventing an approval created for one endpoint from authorizing another.

`SessionRouter` adds conversation-level routing without mutating any runtime. `workspace_select` creates a cryptographically random in-memory binding for one enabled runtime. Every routed coding-tool schema requires the returned `workspace_binding`; the router removes it before policy, audit, and handler dispatch, then supplies a runtime session identity derived from a SHA-256 prefix of the token. The raw token is never written to audit, memory, health, or local state. A new selection on the same MCP session invalidates its old binding. Bindings expire after 24 hours of inactivity, are invalidated by `workspace_clear`, and disappear on daemon restart. Resolution checks only the selected token on the hot path; full expiry cleanup runs at most once per minute. Active bindings are capped at 4,096 with least-recently-used eviction at capacity, and a reverse session index makes clear/expiry removal proportional only to sessions attached to that token. Because the binding is explicit on every coding call, two ChatGPT tabs remain isolated even when the client reconnects or reuses one MCP transport.

`workspace start` and `workspace stop` toggle a named registry entry and reconcile the shared daemon. The primary runtime ID is a lowercase ASCII slug derived from the Git root folder, or from the current folder outside Git. A bare `codebridge`, `run`, or `here` invocation registers the Git root only when it differs from the configured primary workspace; `restart` performs the same check before stopping the daemon. Named-workspace IDs receive a stable canonical-path hash suffix on collision. Existing registrations are matched by canonical path and re-enabled instead of duplicated.

The supervisor ConfigID includes the registry, every enabled named config, and the secret fingerprints referenced by each runtime, so endpoint, tool, provider, or credential changes cannot silently reuse a stale process. Startup readiness time includes required memory and upstream MCP timeouts from all enabled runtimes.

One tunnel profile publishes channel `main` for `/mcp/session` and channel `workspace-<id>` for each enabled fixed endpoint. Runtime and upstream secrets remain in the shared `.env` and are resolved through environment references rather than copied into workspace configuration files.

## 5. HTTP and MCP layers

Codebridge exposes:

```text
/mcp/session                 per-chat workspace-routing MCP endpoint
/mcp                         primary workspace compatibility endpoint
/mcp/workspaces/<id>         fixed named workspace MCP endpoint
/healthz                     public health endpoint
/internal/healthz            loopback supervisor health with PID, ConfigID, workspace, pool, and router summaries
```

A non-loopback `host` requires an MCP bearer token. A browser Origin is accepted only when it is loopback or included in the explicit allowlist. The listener security policy is global. Fixed endpoints use their runtime body limit. Because the session endpoint must parse the JSON-RPC envelope before resolving its binding, it conservatively uses the smallest configured runtime body limit, then applies the selected runtime's tool policy and handler behavior.

Each module's `Specs()` output is the source of truth for its tools, and `runtime.Tools()` is the assembled server contract:

- name;
- title and description;
- JSON input schema;
- read-only and destructive annotations;
- MCP Apps metadata;
- functional ownership through the module name.

`internal/mcpserver` converts a tool result into both JSON text and `structuredContent` when the output is an object. Errors are returned as `CallToolResult{IsError:true}` rather than becoming protocol failures.

### 5.1 Client-owned skills boundary

Skills and MCP tools solve different problems:

```text
ChatGPT Skill
  reusable workflow, instructions, examples, and supporting resources
        │
        ▼
ChatGPT orchestration
        │ calls MCP tools
        ▼
Codebridge
  capability execution, policy, approvals, confinement, audit, and memory
```

Codebridge deliberately does not implement a skill registry. Skills are owned by the client using the MCP server, while Codebridge publishes executable capabilities through `tools/list`. The server therefore does not store skill documents under the workspace, embed skill assets, or expose skill CRUD operations.

The removed compatibility surface is:

```text
list_skills
read_skill
create_skill
delete_skill
codebridge skills [list|read]
```

This boundary avoids two competing sources of reusable instructions and keeps the MCP contract focused on capabilities. A ChatGPT Skill may select and sequence Codebridge tools, but it cannot bypass `ToolSpec` policy classification, exact approvals, root confinement, command guards, audit redaction, or memory capture rules.

The built-in contract contains 75 tools. Because MCP clients commonly cache tool discovery for the lifetime of a connection, a client must reconnect or refresh after a Codebridge upgrade that changes `tools/list`.

## 6. Runtime pipeline and policy

Every tool request follows this pipeline:

```text
beginToolCall (correlation ID + in-flight counters)
  → argument normalization + CallIdentity
  → enforcePolicy
  → toolModules[tool].Handle
  → correlated audit append
  → correlated captureMemoryObservation
  → finishToolCall (outcome + latency + enqueue/drop counters)
```

The tracker stores only registered tool/module names, timestamps, counts, durations, outcome classes, and generated call IDs. It never stores session IDs, arguments, results, or error text. Unknown names collapse into one `_unknown` metric key, and the recent-call ring is capped at 64 entries. `runtime_metrics` can omit per-tool and recent data, while `workspace_info`, `session_report`, `workspace_doctor`, and loopback `/internal/healthz` expose progressively richer bounded views.

Detached server and tunnel processes run behind an internal output wrapper. The wrapper owns their stdout/stderr pipes, writes separate `server.log` and `tunnel.log` files, rotates each at 32 MiB, and retains four previous generations. Because children write to pipes rather than directly opened log files, rotation can close, rename, and reopen files without leaving a long-running child attached to a stale inode.

Audit records include the same `call_id`, tool module, outcome status, and execution duration. Runtime dispatch redacts and encodes the bounded record, then enqueues it to the shared audit writer instead of opening the file on the tool-call critical path. The writer drains bounded batches, rotates `audit.log` at 64 MiB with five retained generations, and falls back to a synchronous append when its queue is saturated so records are not silently dropped. `FlushAudit` provides an explicit persistence barrier for tests and controlled shutdown. Audit write failures do not change the tool result, but synchronous/enqueue failures and background writer failures are reported separately in runtime health and make the audit check in `workspace_doctor` warn. State operations use a bounded striped path-lock table, so unrelated notes, task, index, approval, and audit files do not share one workspace-wide mutex while writes to the same path remain serialized.

When policy rejects a request, the runtime still records an audit failure and policy-rejected metric but does not capture a post-tool observation because the tool did not run.

### Policies

- `strict`: blocks every tool whose assembled `ToolSpec.ReadOnly` is false, including upstream tools not explicitly classified as read-only.
- `balanced`: allows edits but requires exact one-time approval for risky actions and upstream tools classified as `approval` or `always-approval`.
- `full`: does not require ordinary approvals, although command guards still block catastrophic operations and upstream `always-approval` tools remain gated.

An approval action includes the exact target or a hash of the complete tool-plus-arguments payload. `memory_forget` serializes its arguments into the approval action, while generic and upstream write tools use a bounded SHA-256 action so approval cannot be reused for different arguments and raw payloads are not exposed in the action string. The approval manager loads persisted records into an action-to-record index once, re-reads only candidate records before authorization to remain fail-closed against external changes, and chooses the most recently approved exact record. Denied, consumed, and expired records are retained for at most 30 days and the local cache/file set is trimmed toward 1,024 records without deleting active approvals.

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

`SharedServices` pools providers by backend name, endpoint, timeout, adapter options, secret reference, and secret fingerprint. Runtime-only fields such as agent ID, token budget, project strategy, required/fail-open behavior, capture mode, and health-cache duration do not split the provider pool. Recorder delivery settings use a separate key. Because third-party factory registrations are not required to be thread-safe, pooled providers are wrapped by a serializer that preserves optional `Exporter` and `Importer` interfaces.

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

For a named endpoint, the MCP adapter scopes the identity again:

```text
workspace:<id>:mcp:<protocol-session-id>
```

For `/mcp/session`, the router derives identity from the selected workspace and a hash of the opaque chat binding rather than from the transport:

```text
workspace:<id>:chat:<binding-hash>
```

The process-level fallback session is used only by internal callers that do not go through MCP. Fixed endpoint connections remain separate, while routed chat identities remain separate even if the client reuses one MCP transport. Raw binding tokens are not used as provider session IDs.

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

Memory tools, community upstream MCP modules, `ping`, `proc_output`, and `runtime_metrics` are excluded from automatic capture to prevent recursion, telemetry feedback loops, and untrusted payload leakage.

Before enqueueing:

1. Input passes through recursive redaction.
2. Git retains only the operation and argument count, not remote or authentication arguments.
3. Results are reduced to whitelisted fields.
4. Failures store a generic message; the raw error remains in the local audit log.

### 8.9 Recorder delivery guarantees

Compatible workspace runtimes share one daemon-scoped recorder. Each observation already contains its project, `cwd`, and workspace-prefixed session identity, so sharing the delivery queue does not merge memory scope. A different provider or delivery configuration receives a separate pooled recorder.

The recorder uses a bounded queue and non-blocking enqueue:

```text
queue available → enqueued
queue full      → dropped++, tool response is not blocked
```

Providers without an explicit `ConcurrencySafeProvider` opt-in retain one serialized worker. Opted-in providers use up to `deliveryWorkers` queues selected by a deterministic FNV hash of `SessionID`. One session therefore always reaches one FIFO worker, while unrelated sessions can deliver concurrently. The configured total queue capacity is divided across shards and health reports minimum/maximum shard capacity so hot-session burst limits are visible.

Worker delivery:

```text
per-attempt timeout
  → capped exponential retry with deterministic jitter
  → maximum attempts
  → delivered or failed
```

Backoff is capped at two seconds. Closing an individual workspace runtime leaves the shared recorder active. During daemon `SharedServices.Close()`:

1. new resource acquisitions are rejected;
2. shared audit writers flush and close;
3. each recorder rejects new records and drains its current shard queues within a bounded shutdown deadline;
4. shutdown cancellation interrupts in-flight provider calls and retry backoff, counting unfinished observations as abandoned;
5. each pooled memory provider closes after its recorders;
6. reference-counted upstream clients retire their current sessions and close immediately when no active borrower remains, or after the final borrower releases.

Recorder statistics:

```text
workers
sharded
queue_depth
queue_capacity
shard_capacity_min
shard_capacity_max
enqueued
delivered
retried
failed
dropped
abandoned
shutdown_timeouts
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
~/.codebridge/state/
  audit.log
  processes.json
  server.log
  server.log.1 ... server.log.4
  tunnel.log
  tunnel.log.1 ... tunnel.log.4
  launcher.log  # legacy combined log; no longer written by new processes
  profiles/
  workspaces/<workspace-path-hash>/
    notes.json
    checkpoint.json
    current-task.json
    decisions.md
    index.json
    patch-history.json
    backups/
    approvals/

  instances/<workspace-id>/
    audit.log
    workspaces/<workspace-path-hash>/
      ...same isolated state files...
```

The inner workspace path hash is derived from the canonical primary workspace path. The outer named instance directory prevents state overlap across endpoint identities and supports replacing a registration without mixing the old repository's state with the new root. Memory-provider data is not stored in the local state directory, except for audit records and counters associated with tool calls.

`state.NewAt` computes these paths without creating them. The first owning write materializes only the required parent directory, preventing read-only runtimes and tests from accumulating empty workspace hashes. Agent, MCP-server, and HTTP-server package tests set an isolated `CODEBRIDGE_HOME` through package `TestMain` entry points.

State garbage collection distinguishes durable state from regenerable or bounded state. Durable notes, checkpoints, current tasks, decisions, and unknown files are never expired automatically. Repository `index.json` is removed after seven days or immediately when its recorded root no longer exists. Terminal approvals retain their existing 30-day window. Patch backups retain the newest eligible data subject to all three limits: 50 batches, 30 days, and 256 MiB per workspace; one batch may not exceed 128 MiB. Directories not referenced by `patch-history.json` are orphaned and removed.

The daemon performs a startup sweep capped at 100 workspace directories and skips it when another daemon is active. `codebridge state gc --dry-run` scans the primary state root, registered named-workspace data directories, and existing instance roots left by removed registrations without changing files; destructive manual GC requires an offline daemon unless `--force` is explicit. Filesystem-entry scans and reported action lists are bounded, and malformed or unknown state is preserved rather than guessed about.

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
17. Pooled memory-provider calls remain serialized unless the provider explicitly implements `ConcurrencySafeProvider`; sharded recorder delivery preserves FIFO order within each session.
18. Session-routed coding calls require an explicit opaque binding; the raw binding is removed before runtime dispatch and is never persisted in audit, memory, health, or workspace state.
19. Repository inventories, derived views, symbol views, and Git snapshots have independent cardinality limits, TTLs, mutation generations, and caller-context cancellation.
20. Upstream clients bound concurrency, cache health per session generation, defer retired-session close until active borrowers release, and cool down repeated reconnect failures.
21. Patch backup, copy, hunk application, and undo operations observe caller cancellation between files/chunks; rollback uses an independent context so cancellation cannot interrupt restoration.

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
- recorder delivery, retry jitter, drain, dropped counters, provider opt-in concurrency, cross-session overlap, and same-session FIFO order;
- required and optional provider startup behavior;
- export filtering and canonical import;
- generic stdio and Streamable HTTP upstream MCP discovery and calls;
- environment isolation, header forwarding, remote opt-in, and idempotent shutdown;
- upstream namespace collisions, schema validation, policy mapping, audit minimization, memory exclusion, and downstream gateway round trips.
- registry schema migration through version 3, legacy layout copying, atomic persistence, endpoint enable/disable state, and ConfigID sensitivity to named secrets;
- real Streamable HTTP routing for default and named endpoints;
- cross-workspace file, state, audit, approval, and memory-session isolation;
- per-runtime request limits and shared-daemon health summaries;
- provider/recorder reuse, runtime-only memory-setting compatibility, provider-call serialization, and optional export/import preservation;
- upstream client reuse across concurrent workspaces, contract separation for policy differences, stdio `cwd` key isolation, bounded call concurrency, health-cache deduplication, circuit cooldown, active-session retirement, and daemon close-once ownership;
- repository inventory/view/symbol cardinality, mutation invalidation, Git snapshot reuse, tracked-only pattern scans, and cancellation;
- patch cancellation before mutation plus context-aware backup/copy/hunk/undo paths;
- session-router tool contracts, two-chat workspace isolation, same-transport binding separation, reconnect recovery, expiry, clear, token redaction, and real Streamable HTTP routing.

Recommended verification:

```bash
go test ./...
go test -race ./internal/memory/... ./internal/upstreammcp ./internal/agent ./internal/cli ./internal/config ./internal/mcpserver ./internal/server ./internal/workspaceregistry
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
