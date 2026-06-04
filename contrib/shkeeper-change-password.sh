#!/usr/bin/env bash
set -euo pipefail

# Go-native admin password reset helper.
# It shells into /app/admin-account from the Go runtime image.

GO_SHKEEPER_IMAGE="${GO_SHKEEPER_IMAGE:-go-shkeeper:latest}"
GO_SHKEEPER_DOCKER_USER="${GO_SHKEEPER_DOCKER_USER:-0:0}"
NETWORK="${SHKEEPER_DOCKER_NETWORK:-shkeeper_default}"
MARIADB_CONTAINER="${MARIADB_CONTAINER:-mariadb}"
MARIADB_HOST="${MARIADB_HOST:-$MARIADB_CONTAINER}"
MARIADB_DATABASE="${MARIADB_DATABASE:-shkeeper}"
ADMIN_ACCOUNT_REPORT_DIR="${ADMIN_ACCOUNT_REPORT_DIR:-/tmp/go-shkeeper-admin-account}"
ADMIN_ACCOUNT_REPORT_FILE="${ADMIN_ACCOUNT_REPORT_FILE:-$ADMIN_ACCOUNT_REPORT_DIR/admin-account.json}"

secret_mounts=()
secret_envs=()
tmp_password_file=""

cleanup() {
  if [ -n "$tmp_password_file" ]; then
    rm -f "$tmp_password_file"
  fi
}
trap cleanup EXIT

mysql_root_password() {
  docker exec "$MARIADB_CONTAINER" printenv MYSQL_ROOT_PASSWORD
}

require_mariadb_url() {
  local value lowered
  value="$1"
  lowered="${value,,}"
  case "$lowered" in
    mariadb://*|mysql://*|*@tcp\(*\)/*)
      return 0
      ;;
  esac
  echo "Only MariaDB/MySQL is supported. Set MARIADB_DATABASE_URL to mariadb://user:password@mariadb:3306/shkeeper." >&2
  exit 2
}

reject_sqlite_url() {
  local lowered
  lowered="${1,,}"
  if [[ "$lowered" == sqlite:* || "$lowered" == *.sqlite* ]]; then
    echo "SQLite is not supported. Set MARIADB_DATABASE_URL to a MariaDB DSN." >&2
    exit 2
  fi
}

reject_postgres_url() {
  local lowered
  lowered="${1,,}"
  if [[ "$lowered" == postgres:* || "$lowered" == postgresql:* || "$lowered" == pgdb:* ]]; then
    echo "PostgreSQL/PGDB is not supported. Set MARIADB_DATABASE_URL to a MariaDB DSN." >&2
    exit 2
  fi
}

add_secret_file() {
  local source_path="$1"
  local target_name="$2"
  if [ ! -s "$source_path" ]; then
    echo "Missing secret file: $source_path" >&2
    exit 2
  fi
  secret_mounts+=("-v" "$source_path:/run/secrets/$target_name:ro")
}

prompt_password_file() {
  local pass1 pass2
  read -rsp "Password: " pass1
  printf '\n'
  read -rsp "Confirm password: " pass2
  printf '\n'
  if [ -z "$pass1" ] || [ "$pass1" != "$pass2" ]; then
    echo "Passwords do not match." >&2
    exit 2
  fi
  tmp_password_file="$(mktemp)"
  chmod 0600 "$tmp_password_file"
  printf '%s' "$pass1" > "$tmp_password_file"
  add_secret_file "$tmp_password_file" admin_password
  secret_envs+=("-e" "ADMIN_PASSWORD_FILE=/run/secrets/admin_password")
}

MARIADB_DATABASE_URL="${MARIADB_DATABASE_URL:-}"
if [ -z "$MARIADB_DATABASE_URL" ]; then
  MYSQL_ROOT_PASSWORD="${MYSQL_ROOT_PASSWORD:-$(mysql_root_password)}"
  if [ -z "$MYSQL_ROOT_PASSWORD" ]; then
    echo "MYSQL_ROOT_PASSWORD is empty in container $MARIADB_CONTAINER" >&2
    exit 2
  fi
  MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/$MARIADB_DATABASE"
fi
reject_sqlite_url "$MARIADB_DATABASE_URL"
reject_postgres_url "$MARIADB_DATABASE_URL"
require_mariadb_url "$MARIADB_DATABASE_URL"

if [ -n "${ADMIN_PASSWORD_FILE:-}" ]; then
  add_secret_file "$ADMIN_PASSWORD_FILE" admin_password
  secret_envs+=("-e" "ADMIN_PASSWORD_FILE=/run/secrets/admin_password")
elif [ -n "${ADMIN_PASSWORD:-}" ]; then
  secret_envs+=("-e" "ADMIN_PASSWORD")
else
  prompt_password_file
fi

mkdir -p "$ADMIN_ACCOUNT_REPORT_DIR"
chmod 0777 "$ADMIN_ACCOUNT_REPORT_DIR"

docker run --rm --network "$NETWORK" \
  --user "$GO_SHKEEPER_DOCKER_USER" \
  "${secret_mounts[@]}" \
  -v "$ADMIN_ACCOUNT_REPORT_DIR:/deploy-reports" \
  -e MARIADB_DATABASE_URL="$MARIADB_DATABASE_URL" \
  -e ADMIN_USERNAME="${ADMIN_USERNAME:-admin}" \
  -e ADMIN_ACCOUNT_REPORT_FILE=/deploy-reports/admin-account.json \
  "${secret_envs[@]}" \
  "$GO_SHKEEPER_IMAGE" /app/admin-account

if ! grep -Eq '"status"[[:space:]]*:[[:space:]]*"ok"' "$ADMIN_ACCOUNT_REPORT_FILE"; then
  echo "Admin account update report did not pass: $ADMIN_ACCOUNT_REPORT_FILE" >&2
  exit 1
fi

printf 'admin_account_updated report=%s\n' "$ADMIN_ACCOUNT_REPORT_FILE"
