#!/usr/bin/env bash
# Ensure a local PostgreSQL 15 cluster is running and has the app database.
# Idempotent: safe to run repeatedly. Uses the local server instead of Docker.
set -euo pipefail

CLUSTER="15 main"
PORT=5432
DB_USER="${DB_USER:-postgres}"
DB_PASSWORD="${DB_PASSWORD:-devpassword}"
DB_NAME="${DB_NAME:-appdb}"

# Never inherit docker/devcontainer PG* vars that point at a remote host.
unset PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE 2>/dev/null || true

start_cluster() {
  if sudo -n true 2>/dev/null; then
    sudo -n pg_ctlcluster "$CLUSTER" start >/dev/null 2>&1 || true
  else
    pg_ctlcluster "$CLUSTER" start >/dev/null 2>&1 || true
  fi
}

ensure_db() {
  run_sql() { sudo -n runuser -u postgres -- psql "$@"; }

  run_sql -tAc "SELECT 1 FROM pg_roles WHERE rolname='${DB_USER}'" | grep -q 1 \
    && run_sql -c "ALTER ROLE ${DB_USER} WITH PASSWORD '${DB_PASSWORD}'"

  if ! run_sql -tAc "SELECT 1 FROM pg_database WHERE datname='${DB_NAME}'" | grep -q 1; then
    run_sql -c "CREATE DATABASE ${DB_NAME} OWNER ${DB_USER}"
  fi
}

start_cluster
ensure_db

echo "PostgreSQL ready at host=localhost port=${PORT} dbname=${DB_NAME} (user=${DB_USER})"
