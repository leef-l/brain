#!/usr/bin/env bash
set -euo pipefail

command_name="${1:-up}"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root_dir="$(cd "${script_dir}/../.." && pwd)"
migrations_dir="${MIGRATIONS_DIR:-${root_dir}/persistence/migrations}"
schema="${BRAIN_DB_SCHEMA:-}"

if [[ ! -d "${migrations_dir}" ]]; then
  echo "migrations directory not found: ${migrations_dir}" >&2
  exit 1
fi

mapfile -t sql_files < <(find "${migrations_dir}" -maxdepth 1 -type f -name '*.sql' | LC_ALL=C sort)
if [[ ${#sql_files[@]} -eq 0 ]]; then
  echo "no migration files found in ${migrations_dir}" >&2
  exit 1
fi

usage() {
  cat <<EOF
Usage: $(basename "$0") [up|print|status]

Environment:
  DATABASE_URL        PostgreSQL DSN. If unset, psql falls back to PGHOST/PGPORT/PGUSER/PGDATABASE.
  BRAIN_DB_SCHEMA     Optional schema prepended before each migration.
  MIGRATIONS_DIR      Override migration directory.
EOF
}

schema_prefix() {
  if [[ -z "${schema}" ]]; then
    return 0
  fi
  printf 'CREATE SCHEMA IF NOT EXISTS "%s";\nSET search_path TO "%s", public;\n' "${schema}" "${schema}"
}

psql_base=()
if [[ -n "${DATABASE_URL:-}" ]]; then
  psql_base+=("${DATABASE_URL}")
fi

case "${command_name}" in
  up)
    if ! command -v psql >/dev/null 2>&1; then
      echo "psql is required for migration apply" >&2
      exit 127
    fi
    for file in "${sql_files[@]}"; do
      echo "applying $(basename "${file}")"
      {
        schema_prefix
        cat "${file}"
      } | psql -v ON_ERROR_STOP=1 "${psql_base[@]}"
    done
    ;;
  print)
    for file in "${sql_files[@]}"; do
      echo "===== $(basename "${file}") ====="
      cat "${file}"
      echo
    done
    ;;
  status)
    echo "migrations_dir=${migrations_dir}"
    echo "schema=${schema:-<default>}"
    if command -v psql >/dev/null 2>&1; then
      echo "psql=found"
    else
      echo "psql=missing"
    fi
    printf 'files=%d\n' "${#sql_files[@]}"
    for file in "${sql_files[@]}"; do
      printf -- '- %s\n' "$(basename "${file}")"
    done
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    usage >&2
    exit 64
    ;;
esac
