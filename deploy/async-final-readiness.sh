#!/usr/bin/env bash
set -euo pipefail

# Starts final readiness in a detached job so SSH disconnects do not
# kill Docker build or read-only readiness reporting.

ASYNC_ID="${ASYNC_ID:-go-async-readiness-$(date +%Y%m%d%H%M%S)}"
WORK_DIR="${WORK_DIR:-/tmp/$ASYNC_ID-src}"
REPORT_DIR="${REPORT_DIR:-/tmp/$ASYNC_ID-report}"
SOURCE_ARCHIVE="${SOURCE_ARCHIVE:-}"
LOG_FILE="${LOG_FILE:-$REPORT_DIR/async-final-readiness.log}"
STATUS_FILE="${STATUS_FILE:-$REPORT_DIR/async-final-readiness.status}"
PID_FILE="${PID_FILE:-$REPORT_DIR/async-final-readiness.pid}"
JOB_SCRIPT="$REPORT_DIR/async-final-readiness.job.sh"
ALLOW_INLINE_SECRETS="${ALLOW_INLINE_SECRETS:-0}"
CLEANUP_SECRET_DIRS="${CLEANUP_SECRET_DIRS:-}"

refuse_inline_secret() {
  local name="$1"
  if [ "${!name:-}" != "" ] && [ "$ALLOW_INLINE_SECRETS" != "1" ]; then
    echo "Refusing inline secret env $name for async job. Use ${name}_FILE instead." >&2
    exit 2
  fi
}

shell_quote() {
  printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

write_export() {
  local name="$1"
  local value="${!name:-}"
  if [ "$value" = "" ]; then
    return 0
  fi
  printf 'export %s=%s\n' "$name" "$(shell_quote "$value")" >> "$JOB_SCRIPT"
}

for secret_name in \
  API_KEY ADMIN_PASSWORD ADMIN_UPDATE_PASSWORD WORKER_PASSWORD WORKER_SERVERKEY \
  FINAL_PLAN_API_KEY FINAL_PLAN_ADMIN_PASSWORD FINAL_PLAN_ADMIN_UPDATE_PASSWORD FINAL_PLAN_WORKER_PASSWORD \
  DEPLOY_CHECK_API_KEY DEPLOY_CHECK_ADMIN_PASSWORD DEPLOY_CHECK_ADMIN_UPDATE_PASSWORD DEPLOY_CHECK_WORKER_PASSWORD; do
  refuse_inline_secret "$secret_name"
done

mkdir -p "$REPORT_DIR"
chmod 0777 "$REPORT_DIR"

cat > "$JOB_SCRIPT" <<'JOB'
#!/usr/bin/env bash
set -euo pipefail
JOB

for name in \
  ASYNC_ID WORK_DIR REPORT_DIR SOURCE_ARCHIVE LOG_FILE STATUS_FILE PID_FILE \
  CLEANUP_SECRET_DIRS \
  BUILD_IMAGE SHKEEPER_DOCKER_NETWORK MARIADB_CONTAINER MARIADB_HOST MARIADB_DATABASE MARIADB_DATABASE_URL GO_SHKEEPER_IMAGE \
  UPDATE_WORKER_SERVERKEY CONFIRM_DB_WRITE RUN_DEPLOY_CHECK CONFIRM_REAL_CHAIN_REHEARSAL \
  FINAL_PLAN_USE_WALLET_CRYPTOS FINAL_PLAN_USE_WALLET_API_KEY FINAL_PLAN_USE_WALLET_SERVERKEY FINAL_PLAN_REDACT_SECRETS \
  FINAL_PLAN_ALL_CRYPTOS FINAL_PLAN_CRYPTOS FINAL_PLAN_PAYOUT_AMOUNT FINAL_PLAN_PAYMENT_AMOUNT \
  FINAL_PLAN_ADMIN_USERNAME FINAL_PLAN_ADMIN_UPDATE_USERNAME WORKER_USERNAME WORKER_SERVERKEY_CRYPTOS UTILITY_DOCKER_USER \
  API_KEY_FILE ADMIN_PASSWORD_FILE ADMIN_UPDATE_PASSWORD_FILE WORKER_PASSWORD_FILE WORKER_SERVERKEY_FILE \
  DEPLOY_CHECK_PAYOUT_AMOUNT DEPLOY_CHECK_PAYMENT_AMOUNT; do
  write_export "$name"
done

while IFS='=' read -r name _; do
  case "$name" in
    FINAL_PLAN_PAYOUT_AMOUNT_*|FINAL_PLAN_PAYMENT_AMOUNT_*|DEPLOY_CHECK_PAYOUT_AMOUNT_*|DEPLOY_CHECK_PAYMENT_AMOUNT_*|WORKER_USERNAME_*|WORKER_PASSWORD_*_FILE)
      write_export "$name"
      ;;
  esac
done < <(env)

cat >> "$JOB_SCRIPT" <<'JOB'

cleanup_secret_dirs() {
  local raw dir
  raw="${CLEANUP_SECRET_DIRS:-}"
  if [ -z "$raw" ]; then
    return 0
  fi
  IFS=',' read -r -a dirs <<< "$raw"
  for dir in "${dirs[@]}"; do
    dir="$(printf '%s' "$dir" | xargs)"
    if [ -z "$dir" ]; then
      continue
    fi
    case "$dir" in
      /tmp/codex-*secret*|/tmp/codex-*secrets*)
        rm -rf -- "$dir"
        ;;
      *)
        echo "Refusing cleanup of non-codex secret path: $dir" >&2
        ;;
    esac
  done
}

finish() {
  local status=$?
  cleanup_secret_dirs
  printf 'exit_code=%s finished_at=%s\n' "$status" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$STATUS_FILE"
  exit "$status"
}
trap finish EXIT

printf 'status=running started_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$STATUS_FILE"

mkdir -p "$WORK_DIR"
if [ -n "${SOURCE_ARCHIVE:-}" ]; then
  tar -xzf "$SOURCE_ARCHIVE" -C "$WORK_DIR"
fi

if [ -x "$WORK_DIR/go-shkeeper/deploy/final-readiness.sh" ] || [ -f "$WORK_DIR/go-shkeeper/deploy/final-readiness.sh" ]; then
  READINESS_SCRIPT="$WORK_DIR/go-shkeeper/deploy/final-readiness.sh"
elif [ -f "$WORK_DIR/deploy/final-readiness.sh" ]; then
  READINESS_SCRIPT="$WORK_DIR/deploy/final-readiness.sh"
else
  echo "Cannot find final-readiness.sh under $WORK_DIR" >&2
  exit 2
fi

bash "$READINESS_SCRIPT"
JOB

chmod 0700 "$JOB_SCRIPT"
nohup bash "$JOB_SCRIPT" > "$LOG_FILE" 2>&1 &
pid=$!
printf '%s\n' "$pid" > "$PID_FILE"
printf 'async_final_readiness_started pid=%s report_dir=%s log=%s status=%s\n' "$pid" "$REPORT_DIR" "$LOG_FILE" "$STATUS_FILE"
