# Changelog

All notable changes to Wormhole are documented in this file.

## [Unreleased]

### Added

- Added an extensible remote MCP ingress model with dedicated loopback-only fixed workspace/profile listeners, mandatory per-ingress bearer secrets, external and managed Cloudflare publishers, status/doctor/log/diagnostic integration, and a structured Admin editor with workspace/profile selection, provider-specific fields, write-only secret state, advanced JSON escape hatch, and bounded live MCP readiness for hosted clients such as Notion Custom Agents.
- Added safe Admin Save & Restart, upstream MCP catalog refresh and contract diffs, workspace override provenance/compaction previews, and downloadable sanitized diagnostic bundles.
- Added persisted custom tool profiles with stable fixed/session endpoints, contract hashes, output modes, compact defaults, tunnel assignment, and an Admin profile editor.
- Added an Admin Operations center with live runtime/module metrics, local approval decisions, and a bounded redacted audit explorer.
- Added per-workspace runtime tool-call concurrency limits, selected-workspace schema validation for routed tools, panic recovery, and bounded redacted audit errors.
- Added native multi-tunnel management: one Wormhole daemon can generate Fast/Full profiles, supervise one tunnel-client process per named Tunnel ID, isolate logs and runtime-key environment variables, and migrate legacy single-tunnel state without breaking existing installations.
- Added local Admin UI username/password authentication with owner-only hashed credentials, create-only loopback first-account setup, bounded HttpOnly browser sessions, login throttling, logout, and CLI-only password change/reset/recovery.
- Added an Admin UI guided setup wizard for runtime, tunnel ID, write-only OpenAI Runtime API key, and optional memory configuration, including direct links to the OpenAI organization tunnel and API-key settings.
- Added Admin UI directory browsing plus revision-safe named-workspace registration and removal, with preserved runtime state and optional override deletion.
- Added schema-versioned workspace overrides and `wormhole workspace compact <id> [--dry-run]` for safely converging legacy full snapshots toward inherited deltas.
- Added duplicate-key and unknown-field rejection for global and workspace JSON configuration.
- Added owner-identified lifecycle locking and platform process fingerprints for safe daemon/tunnel reconciliation.
- Added Linux, macOS, Windows, race-detector, and installer-syntax CI jobs.
- Added recursively merged partial configuration overrides for named workspaces, including explicit array replacement, `false` values, and `null` deletion of inherited keys.
- Added per-upstream `workspaceIds` scoping and `startupMode` values `eager`, `background`, and `lazy`.
- Added bounded, owner-only persistent upstream tool catalogs for deferred typed-tool registration.

### Changed

- Added a hosted-client connection kit to the remote MCP Admin editor, including browser-generated 256-bit one-time bearer handoff through the existing write-only Secrets API, copyable Notion/header configuration, local readiness badges, and restart/profile-rescan guidance without adding any secret-read endpoint.
- Added explicit `wormhole remote list` and `wormhole remote verify <name>` commands; verification authenticates to both local and configured public MCP endpoints, blocks redirects, bounds tool discovery, and requires protocol plus full discovered tool-contract hashes to match.
- Added staged remote MCP bearer rotation without a credential cutover gap, with optional `authTokenFallbackEnv`, dual-token listener acceptance, fallback-only startup safety, credential-level readiness probes, fallback-first public verification, and an Admin rotation flow that promotes the staged credential only after the running listener accepts it.
- Upgraded the official MCP Go SDK to v1.7.0, enabling MCP `2026-07-28` negotiation on stateless remote ingresses while preserving older Streamable HTTP compatibility; upstream health now uses a protocol-compatible read probe after `ping` removal, and caller-cancelled 2026 sessions are transparently repaired before the next upstream call without consuming mutation retries.
- Hardened remote MCP ingresses with exact public-Origin validation, authenticated GET transport handling, ownership-aware status reporting, and an active `doctor` probe that negotiates MCP and lists the fixed tool contract instead of treating an open TCP port as readiness.

- Reworked the Operations runtime view into a compact workspace master-detail layout with a bounded overview list, one selected workspace detail panel, top-tool expansion, separate daemon metrics, clearer parent/child tab hierarchy, and larger Operations-specific typography.
- Daemon identity now fingerprints the complete effective config and all referenced secrets while hashing binary/widget inputs only once per reconciliation.
- Explicit test/build/lint overrides require full mode and exact approval; all quality commands invalidate repository caches after execution.
- Missing workspace roots now fail validation instead of being silently recreated.
- New named workspace registrations now persist only workspace-specific deltas and inherit later global memory, MCP, policy, and limit changes.
- Initialized workspace runtimes concurrently and based the supervisor timeout on the slowest workspace dependency chain.
- Single-flighted shared upstream MCP creation and cached recent startup failures for `failureCooldownMs`.
- Refreshed deferred tool catalogs on first connection while keeping the active downstream tool contract immutable until restart.

### Fixed

- Prevented the Admin Operations page from rendering blank when a workspace has no startup warnings, and added a page-level render error fallback.
- Prevented stale or recycled PIDs from being signaled and made stop failures visible instead of always printing success.
- Rolled back workspace config changes when registry persistence fails, and restored force-removed configs when unregistering fails.
- Propagated recursive tree traversal errors and cancellation instead of returning silently incomplete trees.
- Preserved empty JSON arrays in strict config parsing so array-replacement overrides remain distinct from `null`.
- Prevented upstream error auditing from dereferencing a typed-nil MCP result.

## [1.0.1] - 2026-07-26

### Added

- Added `wormhole state gc --dry-run` and bounded startup garbage collection for workspace state.
- Added backup retention limits by age, batch count, per-workspace storage, and single-batch size.

### Changed

- Simplified the README around installation, Secure MCP Tunnel setup, Runtime API keys, ChatGPT MCP apps, channels, and workspace usage.
- Reduced audit I/O and metrics lock contention.
- Bounded server and tunnel log retention.

### Fixed

- Prevented read-only runtimes and tests from creating empty persistent workspace-state directories.
- Removed stale repository caches, orphaned and over-quota backups, expired terminal approvals, and empty workspace-state directories without deleting durable notes, tasks, checkpoints, or decisions.
- Isolated runtime-heavy test packages from the user's real `~/.wormhole` tree.
- Made legacy state migration one-time so removed canonical state is not recreated on later starts.
- Prevented ChatGPT from falling back to an external container filesystem for Wormhole host workspace paths.

## [1.0.0] - 2026-07-22

Wormhole 1.0.0 is the first stable release. It packages the multi-workspace MCP gateway, local coding tools, policy enforcement, observability, memory integration, and cross-platform installer into one production-ready binary.

### Highlights

- Added per-chat workspace routing through `/mcp/session` with opaque, expiring workspace bindings.
- Added named workspace registration, isolated runtime state, fixed compatibility endpoints, and shared daemon resources.
- Added a generic upstream MCP gateway for stdio and Streamable HTTP servers, including connection pooling, bounded concurrency, health caching, reconnect handling, and policy controls.
- Added provider-neutral project memory with agentmemory support, asynchronous capture, retry/backoff, project and session scoping, and canonical export/import.
- Added runtime metrics, correlation IDs, audit-writer health, repository cache diagnostics, and bounded supervisor telemetry.
- Consolidated persistent configuration and state under `~/.wormhole`, with safe migration from the previous OS-specific directories.
- Added repository inventory caching, CodeGraph navigation, changed-test selection, quality gates, diff review, security scans, and managed process tools.
- Hardened root confinement, symlink handling, approvals, secret separation, command execution, upstream environment isolation, and HTTP authentication/origin checks.
- Added Linux, macOS, and Windows release artifacts for amd64 and arm64, with checksums and an installer script.

### Compatibility notes

- Project profile configuration now uses `<workspace>/.wormhole/profile.json`. The former `<workspace>/.agent/profile.json` remains a compatibility fallback.
- The built-in skill registry and skill CRUD tools were removed. ChatGPT Skills remain client-owned and can orchestrate Wormhole MCP tools.
- Existing configuration, workspace registry, secrets, and runtime state are copied into the new `~/.wormhole` layout without overwriting canonical files or deleting legacy data.

### Verification

- `go test ./...`
- `go vet ./...`
- `go build ./...`
- Cross-compilation for Linux, macOS, and Windows on amd64 and arm64 through GoReleaser.

[Unreleased]: https://github.com/qunv/wormhole/compare/v1.0.1...HEAD
[1.0.1]: https://github.com/qunv/wormhole/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/qunv/wormhole/releases/tag/v1.0.0
