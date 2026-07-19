#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose=(docker compose -f "$root/testdata/database/docker-compose.yml")

cleanup() {
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

"${compose[@]}" up --detach --wait

export CODEBRIDGE_TEST_POSTGRES_DSN='postgres://codebridge:codebridge@127.0.0.1:55432/app?sslmode=disable'
export CODEBRIDGE_TEST_POSTGRES_WRITE_DSN='postgres://codebridge_writer:codebridge_writer@127.0.0.1:55432/app?sslmode=disable'
export CODEBRIDGE_TEST_MYSQL_DSN='codebridge:codebridge@tcp(127.0.0.1:53306)/app?tls=false'
export CODEBRIDGE_TEST_MYSQL_WRITE_DSN='codebridge_writer:codebridge_writer@tcp(127.0.0.1:53306)/app?tls=false'

go test "$root/internal/database/postgres" -run 'TestPostgres(Integration|MutationIntegration)' -count=1 -v
go test "$root/internal/database/mysql" -run 'TestMySQL(Integration|MutationIntegration)' -count=1 -v
