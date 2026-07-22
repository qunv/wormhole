# Changelog

All notable changes to Codebridge are documented in this file.

## [1.0.0] - 2026-07-22

Codebridge 1.0.0 is the first stable release. It packages the multi-workspace MCP gateway, local coding tools, policy enforcement, observability, memory integration, and cross-platform installer into one production-ready binary.

### Highlights

- Added per-chat workspace routing through `/mcp/session` with opaque, expiring workspace bindings.
- Added named workspace registration, isolated runtime state, fixed compatibility endpoints, and shared daemon resources.
- Added a generic upstream MCP gateway for stdio and Streamable HTTP servers, including connection pooling, bounded concurrency, health caching, reconnect handling, and policy controls.
- Added provider-neutral project memory with agentmemory support, asynchronous capture, retry/backoff, project and session scoping, and canonical export/import.
- Added runtime metrics, correlation IDs, audit-writer health, repository cache diagnostics, and bounded supervisor telemetry.
- Consolidated persistent configuration and state under `~/.codebridge`, with safe migration from the previous OS-specific directories.
- Added repository inventory caching, CodeGraph navigation, changed-test selection, quality gates, diff review, security scans, and managed process tools.
- Hardened root confinement, symlink handling, approvals, secret separation, command execution, upstream environment isolation, and HTTP authentication/origin checks.
- Added Linux, macOS, and Windows release artifacts for amd64 and arm64, with checksums and an installer script.

### Compatibility notes

- Project profile configuration now uses `<workspace>/.codebridge/profile.json`. The former `<workspace>/.agent/profile.json` remains a compatibility fallback.
- The built-in skill registry and skill CRUD tools were removed. ChatGPT Skills remain client-owned and can orchestrate Codebridge MCP tools.
- Existing configuration, workspace registry, secrets, and runtime state are copied into the new `~/.codebridge` layout without overwriting canonical files or deleting legacy data.

### Verification

- `go test ./...`
- `go vet ./...`
- `go build ./...`
- Cross-compilation for Linux, macOS, and Windows on amd64 and arm64 through GoReleaser.

[1.0.0]: https://github.com/qunv/codebridge/releases/tag/v1.0.0
