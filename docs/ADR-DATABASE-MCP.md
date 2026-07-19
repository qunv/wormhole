# ADR: Secure multi-database MCP support

## Status

Accepted for the first implementation slice.

## Context

CodeBridge needs to expose database capabilities through its existing MCP server while supporting multiple independently configured connections such as `db.codebridge_dev` and `db.codebridge_prod`.

Database aliases are routing identifiers, not authorization boundaries. Security must be enforced by CodeBridge policy, per-connection policy, dedicated database credentials, database grants, query limits, audit redaction, and deployment isolation.

## Decision

1. MCP tool names remain stable. Every database tool requires an exact `alias` argument.
2. Unknown aliases fail closed. There is no default connection, fuzzy matching, prefix matching, or environment substitution.
3. Credentials are referenced from non-secret configuration and resolved from environment variables at runtime. DSNs are never serialized to `config.json`, MCP results, audit logs, or memory.
4. The execution layer is `internal/database/sqlcore`, built on `database/sql`. It owns pooling, read-only transactions, row scanning, masking, and result limits. Each driver supplies a small dialect adapter for placeholders, identifier quoting, session setup, explain syntax, schema introspection, driver-specific validation, and error normalization.
5. PostgreSQL is implemented through `database/sql` and `pgx/v5/stdlib`; MySQL is implemented through `database/sql` and `go-sql-driver/mysql`.
6. The first public capability set is read-only:
   - `db_list_connections`
   - `db_describe`
   - `db_query`
   - `db_explain`
7. Queries are restricted to one read-only statement, executed in a read-only transaction, subject to timeout, concurrency, row, cell, and result-size limits.
8. Production connections must be configured as read-only. Production mutation tools are outside this implementation slice.
9. SQL, parameters, rows, DSNs, and raw database results are redacted from CodeBridge audit and excluded from automatic memory capture.
10. Tool exposure can be restricted by group and exact tool name so a production-read instance can expose only `basic` and `database` tools.
11. Database content is untrusted data and must never be treated as instructions.

## Consequences

- Adding connections does not change the MCP tool contract.
- Adding drivers is isolated behind the database constructor registry.
- Read-only safety does not depend only on SQL text classification; dedicated read-only database users remain required for production.
- Long-lived transactions, arbitrary write SQL, DDL, procedures, and runtime-created connections are intentionally unsupported.

## Driver extension contract

Adding another SQL engine requires:

1. Importing its `database/sql` driver package.
2. Implementing `sqlcore.Dialect` for placeholders, identifier quoting, read-only session setup, explain syntax, introspection, validation, and errors.
3. Constructing the shared client with `sqlcore.Open` or `sqlcore.NewWithDB`.
4. Registering the constructor through `database/factory.Register`.

It must not duplicate pool configuration, transaction lifecycle, row scanning, masking, result-size enforcement, alias routing, or MCP handlers.

## Planned follow-up

After the read-only PostgreSQL and MySQL slices are stable, controlled structured mutations may be added for non-production connections with exact approvals and affected-row guards. SQLite and external secret providers can then be introduced without changing the MCP contract.
