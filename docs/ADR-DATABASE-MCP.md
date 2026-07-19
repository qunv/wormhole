# ADR: Secure multi-database MCP support

## Status

Accepted and implemented.

## Context

CodeBridge exposes database capabilities through its MCP server while supporting independently configured aliases such as `db.app_dev` and `db.app_prod`.

Aliases are routing identifiers, not authorization boundaries. Security is enforced by CodeBridge policy, per-connection access rules, dedicated database credentials, database grants, query limits, exact approvals, audit redaction, and deployment isolation.

## Decision

1. Every database tool requires an exact `alias`. Unknown aliases fail closed; there is no default, fuzzy matching, prefix matching, or environment substitution.
2. Credentials remain outside `config.json`. `credentialRef` selects a registered resolver. Built-ins are `env` and permission-checked `file`; external providers register through `database/credential.Register`.
3. `internal/database/sqlcore`, built on `database/sql`, owns pool configuration, transactions, scanning, masking, result limits, metrics, and structured mutation execution.
4. Dialects own placeholders, identifier quoting, session hardening, explain syntax, metadata/primary-key introspection, engine-specific validation, and error normalization.
5. Supported engines are:
   - PostgreSQL through `pgx/v5/stdlib`.
   - MySQL through `go-sql-driver/mysql`.
   - SQLite through `modernc.org/sqlite`.
6. Public read-only tools are:
   - `db_list_connections`
   - `db_describe`
   - `db_query`
   - `db_explain`
7. Read-only SQL is restricted to one `SELECT` or read-only `WITH` statement. The conservative scanner rejects ambiguous comments/escapes, executable comments, mutation tokens, file output, attachment, PRAGMA, and dialect-specific side effects.
8. PostgreSQL and MySQL queries execute in read-only transactions. SQLite opens an existing canonical root-confined file using `mode=ro` and enforces `query_only`; memory databases, URI credentials, root escapes, and symlink escapes are rejected.
9. Raw write SQL is unsupported. Non-production structured mutations use:
   - `db_preview_mutation`
   - `db_mutate`
10. Structured mutations support only `update` and `delete`; require `access.mode=read-write`, a non-production environment, simple identifiers, scalar values, equality predicates containing the complete primary key, and `max_affected_rows` between 1 and the hard limit.
11. `db_mutate` requires an exact one-time approval even under `policy=full`. `policy=strict` always blocks it. A transaction is rolled back when the affected-row result exceeds the requested guard.
12. Production aliases must remain read-only. Dedicated read-only database users are still required because SQL classification is a defense-in-depth layer, not the authorization boundary.
13. SQL, parameters, rows, mutation values, predicates, DSNs, endpoints, and credentials are excluded from audit and automatic memory capture. Audit retains only safe metadata such as alias, environment, query hash, duration, row/affected-row counts, target identifiers, and truncation status.
14. Per-alias summaries expose safe operation counters and `database/sql` pool statistics.
15. Database cells are untrusted data and must never be interpreted as instructions.

## Consequences

- Adding aliases does not change the MCP contract.
- Adding drivers is isolated behind the constructor and dialect registries.
- Adding credential backends is isolated behind the credential resolver registry.
- PostgreSQL/MySQL behavior is verified by a Docker CI matrix; SQLite is tested against real temporary database files.
- Long-lived user transactions, raw mutation SQL, DDL, procedures, runtime-created connections, bulk mutations without primary keys, and production writes remain intentionally unsupported.

## Driver extension contract

Adding another SQL engine requires:

1. Importing its `database/sql` driver.
2. Implementing `sqlcore.Dialect`.
3. Optionally implementing `sqlcore.MutationDialect` only when structured non-production mutations can be safely supported.
4. Constructing the shared client with `sqlcore.Open` or `sqlcore.NewWithDB`.
5. Registering the constructor through `database/factory.Register`.

A driver must not duplicate pool management, transaction lifecycle, row scanning, masking, result-size enforcement, mutation construction, alias routing, audit handling, or MCP handlers.

## Verification

- Adversarial scanner and dialect tests.
- PostgreSQL 15, 16, and 17 integration matrix.
- MySQL 8.0 and 8.4 integration matrix.
- Root-confined real-file SQLite tests.
- Race tests for database, runtime, config, MCP, CLI, and security packages.
- `go vet`, native build, Windows cross-build, and macOS cross-build.
