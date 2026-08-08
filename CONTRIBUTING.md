# Contributing to Wormhole

Thank you for helping improve Wormhole. This project is a local-first coding agent and MCP gateway written in Go. Contributions should preserve its security boundaries, stable tool contract, workspace isolation, bounded resource usage, and cross-platform behavior.

Before making a substantial change, read [README.md](README.md) and [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). The architecture document is the source of truth for package ownership, runtime lifecycle, security invariants, memory behavior, and release compatibility.

## Ways to contribute

Useful contributions include:

- reproducible bug reports;
- focused fixes with regression tests;
- documentation and examples;
- performance or observability improvements with measurable evidence;
- new memory-provider adapters;
- improvements to upstream MCP integration;
- new tools or modules that fit the existing policy and lifecycle model;
- cross-platform fixes for Linux, macOS, or Windows.

For large features, breaking MCP contract changes, new persistent state, or changes to the security model, open an issue or discussion before investing in a full implementation. Describe the problem, expected behavior, compatibility impact, and proposed architecture.

## Development requirements

Required:

- Go 1.25 or later;
- Git;
- a Unix-like shell for the documented Makefile commands, or equivalent commands on Windows.

Optional:

- Node.js 24 and npm when changing the embedded Admin UI under `web/admin`;
- `rg` for faster repository search;
- `codegraph` for `codegraph_explore` development and testing;
- GoReleaser for release and archive changes;
- an upstream MCP server when testing gateway integrations;
- agentmemory when testing the built-in memory adapter.

Clone your fork and verify the repository before changing code:

```bash
git clone https://github.com/<your-account>/wormhole.git
cd wormhole
go test ./...
go vet ./...
go build ./...
```

The equivalent Makefile targets are:

```bash
make test
make vet
make build
```

Run Wormhole locally without a tunnel:

```bash
go run ./cmd/wormhole serve --no-tunnel
```

When changing the Admin UI, verify and regenerate the committed embedded assets:

```bash
make admin-ui-check
make admin-ui
git diff --exit-code -- internal/adminui/dist
```

Use temporary directories or `WORMHOLE_HOME` when a test or manual workflow should not touch your normal configuration:

```bash
export WORMHOLE_HOME="$(mktemp -d)"
```

Never commit `.env`, runtime tokens, database credentials, tunnel credentials, local state, generated binaries, logs, profiles, or private workspace configuration.

## Repository architecture

Keep changes inside the package that owns the behavior:

| Path | Responsibility |
|---|---|
| `cmd/wormhole` | Executable entrypoint and process exit behavior |
| `internal/app` | Version metadata and application composition |
| `internal/cli` | CLI grammar, setup, lifecycle, tunnel, and installation |
| `internal/server` | HTTP composition, authentication, Origin policy, health, and limits |
| `internal/adminauth` | Local Admin credential hashing, bounded sessions, throttling, and session invalidation |
| `internal/admin` | Local Admin API, authentication, CSRF/Host enforcement, revision-safe configuration and write-only secrets |
| `internal/adminui` | Embedded production Admin UI assets |
| `web/admin` | React, TypeScript, Vite, and Tailwind Admin UI source |
| `internal/mcpserver` | MCP server construction, session routing, bindings, and result adaptation |
| `internal/agent` | Tool modules, runtime dispatch, policy, audit, metrics, and shared services |
| `internal/workspace` | Canonical paths, owning roots, search, and confinement |
| `internal/security` | Risk classification, command guards, approvals, and redaction |
| `internal/patch` | Patch validation, backup, application, and undo |
| `internal/processx` | Bounded commands and managed process trees |
| `internal/upstreammcp` | Stdio and Streamable HTTP MCP client lifecycle |
| `internal/memory` | Provider contracts, scoping, recorder, and adapters |
| `internal/state` | Notes, tasks, audit, approvals, backups, and workspace state |
| `internal/workspaceregistry` | Named workspace persistence and migration |
| `internal/assets` | Embedded MCP Apps assets |

Avoid package cycles and avoid moving transport-specific concerns into tool handlers. `agent.Runtime` owns cross-cutting policy, audit, metrics, memory capture, and module lifecycle. Workspace-local state must not be placed in daemon-global objects.

## Go style

Follow standard Go conventions and the existing source style.

- Run `gofmt` on every modified Go file.
- Prefer clear, direct code over unnecessary abstraction.
- Return contextual errors with `%w` when callers need the underlying error.
- Accept and propagate the caller's `context.Context` for operations that may block, traverse files, call providers, start processes, or access the network.
- Check cancellation during bounded loops and multi-file operations.
- Do not start an unbounded goroutine, queue, cache, map, log, response, or output buffer.
- Make ownership and shutdown explicit. Shared services close after all workspace runtimes; workspace-local modules close in reverse registration order.
- Keep health and metrics outputs bounded and free of raw arguments, results, session tokens, and secret values.
- Add comments for exported APIs and for non-obvious concurrency, security, migration, or compatibility decisions.

New Go source files should retain the project license header:

```go
// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later
```

## Tool and module changes

The MCP tool contract is a public compatibility surface. Tool names, schemas, annotations, result fields, and workspace-routing behavior may be cached by clients and relied on by integrations.

When adding or changing a built-in tool:

1. Place it in the module that owns its behavior.
2. Define a complete `ToolSpec` with a stable name, useful title, accurate description, bounded JSON schema, and correct read-only/destructive classification.
3. Ensure the module has a unique valid name and that every tool name is unique.
4. Add dispatch tests, schema/contract tests, policy tests, and MCP round-trip coverage when applicable.
5. Update README or architecture documentation when the public contract or workflow changes.
6. Treat renames, removals, changed required arguments, and incompatible result changes as breaking changes requiring explicit versioning and migration notes.

`ToolSpec.ReadOnly` is security-sensitive. A mutating tool must never be marked read-only to avoid approval or strict-policy enforcement.

External and community MCP annotations are untrusted by default. New upstream behavior must preserve namespacing, schema and response limits, environment isolation, bounded concurrency, audit minimization, and the rule that mutation calls are not automatically replayed after a transport failure.

## Security requirements

Security behavior is part of the project contract, not an optional hardening layer.

Every contribution must preserve these principles:

- resolve file paths through the workspace manager and keep them inside configured roots;
- canonicalize symlinks and the longest existing ancestor before containment checks;
- reject deleting or renaming a configured root;
- keep commands and upstream stdio working directories root-confined;
- default remote listeners and upstream HTTP connections to safe, explicit opt-in behavior;
- keep secret values outside persistent JSON configuration;
- use `secretEnv`, `envRefs`, and `headerRefs` instead of storing credentials;
- redact sensitive values before audit or diagnostic persistence;
- never persist raw workspace-binding tokens;
- keep upstream arguments and results out of automatic long-term memory capture;
- preserve exact, expiring, one-time approval semantics;
- fail closed when validation, confinement, policy, or approval state is ambiguous.

Do not weaken a guard merely to make a test pass. Add a focused test that demonstrates both the permitted case and the blocked case.

For a sensitive vulnerability, do not publish credentials, private workspace data, or a weaponized proof of concept in a public issue. Use GitHub private vulnerability reporting when it is enabled, or contact the maintainer privately through the repository owner profile.

## Configuration and state changes

Persistent configuration and state live under the canonical `~/.wormhole` layout. Changes to paths or schemas must be backward compatible unless explicitly released as a breaking change.

When changing configuration or persisted state:

- provide defaults that keep existing installations working;
- validate all new fields;
- preserve secret separation;
- make migrations idempotent;
- do not overwrite an existing canonical file during migration;
- retain rollback-safe behavior where practical;
- test Linux, macOS, and Windows path behavior when platform rules differ;
- update ConfigID inputs when a setting changes runtime identity or process reuse;
- update [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

Named workspaces must remain isolated in configuration, runtime state, approvals, audit, managed processes, memory sessions, and MCP bindings.

## Memory-provider changes

The canonical `memory.Provider` contract must remain provider-neutral. Adapters normalize provider-specific payloads into Wormhole result types; runtime and MCP modules must not depend on a backend's private schema.

Memory contributions should preserve:

- fail-open behavior when `required=false`;
- startup health enforcement when `required=true`;
- project and session scoping;
- same-session FIFO delivery;
- bounded queues, retries, timeouts, and shutdown;
- serialization for providers that do not explicitly opt into concurrent calls;
- redacted, reduced automatic observations;
- canonical export/import compatibility.

Tests should cover unavailable providers, timeouts, retries, shutdown, response limits, capability normalization, and secret-free operation where supported.

## Testing

Add regression tests for every bug fix and focused tests for new behavior. Prefer deterministic tests that use temporary directories, loopback listeners, fake providers, fake upstream servers, and explicit timeouts. Any package test that constructs a runtime through the default data directory must isolate `WORMHOLE_HOME` in `TestMain`; tests must never create entries in the developer's real `~/.wormhole/state/workspaces` tree.

Minimum verification for ordinary changes:

```bash
gofmt -w <changed-go-files>
go test ./...
go vet ./...
go build ./...
```

Run the race-enabled package set for concurrency, runtime, memory, upstream MCP, server, registry, or lifecycle changes:

```bash
go test -race \
  ./internal/memory/... \
  ./internal/upstreammcp \
  ./internal/agent \
  ./internal/cli \
  ./internal/config \
  ./internal/mcpserver \
  ./internal/server \
  ./internal/workspaceregistry
```

Run cross-platform builds when changing path handling, process management, command execution, networking, archives, installation, or platform-specific code:

```bash
GOOS=windows GOARCH=amd64 go build ./...
GOOS=darwin GOARCH=arm64 go build ./...
```

For release configuration or archive changes, verify a local snapshot:

```bash
goreleaser release --snapshot --clean
```

For the external Streamable HTTP path, use the documented smoke test when a local server is available:

```bash
WORMHOLE_TEST_ENDPOINT=http://127.0.0.1:8132/mcp \
  go test ./internal/server -run TestExternalStreamableHTTP -v
```

Tests must not depend on a developer's normal `~/.wormhole` state, credentials, internet access, wall-clock timing without tolerance, or execution order.

## Documentation

Update documentation in the same pull request when changing:

- user-visible CLI behavior;
- configuration fields or paths;
- MCP endpoints, tools, schemas, or result fields;
- policy or approval behavior;
- security invariants;
- memory or upstream MCP integration;
- platform support;
- migration or release behavior.

Use `README.md` for user workflows and `docs/ARCHITECTURE.md` for ownership, lifecycle, invariants, and detailed design. Add notable user-visible changes to `CHANGELOG.md` when preparing a release.

## Commit guidelines

Keep commits focused and reviewable. Conventional Commit-style subjects are preferred:

```text
feat: add ...
fix: prevent ...
refactor: simplify ...
perf: reduce ...
test: cover ...
docs: explain ...
chore: update ...
```

Use the imperative mood, keep the subject concise, and explain important compatibility, security, migration, or performance decisions in the commit body. Avoid mixing unrelated refactors, formatting, generated artifacts, and behavior changes in one commit.

## Pull requests

A pull request should include:

- the problem and why it matters;
- the chosen approach and important trade-offs;
- affected packages and public interfaces;
- security, compatibility, migration, and multi-workspace impact;
- tests executed and their results;
- screenshots or logs for UI, installer, or operational changes;
- documentation changes;
- follow-up work that is intentionally out of scope.

Keep the diff as small as the solution allows. Review your own diff before requesting review, remove debugging code, and confirm that no secret or local state file is included.

Suggested checklist:

```text
[ ] The change is focused and follows package ownership.
[ ] Go files are formatted and retain the SPDX header.
[ ] New behavior has tests; bug fixes have regression tests.
[ ] go test ./..., go vet ./..., and go build ./... pass.
[ ] Race or cross-platform checks were run when relevant.
[ ] Tool schemas and read-only/destructive annotations are correct.
[ ] Security, confinement, redaction, and approval behavior are preserved.
[ ] Multi-workspace state and sessions remain isolated.
[ ] User and architecture documentation are updated.
[ ] No credentials, local state, logs, or generated binaries are committed.
```

## Licensing

Wormhole is licensed under the GNU Affero General Public License, version 3 or later. By submitting a contribution, you agree that it may be distributed under the same license. Preserve existing copyright, attribution, and SPDX notices.

See [LICENSE](LICENSE) for the complete terms.
