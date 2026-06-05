#!/usr/bin/env bash
set -euo pipefail

# Final readiness entrypoint for a production SHKeeper host.
# Defaults to read-only checks. It does not stop or replace production containers.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

BUILD_IMAGE="${BUILD_IMAGE:-1}"
NETWORK="${SHKEEPER_DOCKER_NETWORK:-shkeeper_default}"
MARIADB_CONTAINER="${MARIADB_CONTAINER:-mariadb}"
MARIADB_HOST="${MARIADB_HOST:-$MARIADB_CONTAINER}"
MARIADB_DATABASE="${MARIADB_DATABASE:-shkeeper}"
READINESS_ID="${READINESS_ID:-go-final-readiness-$(date +%Y%m%d%H%M%S)}"
REPORT_DIR="${REPORT_DIR:-/tmp/$READINESS_ID}"
GO_SHKEEPER_IMAGE="${GO_SHKEEPER_IMAGE:-go-shkeeper:$READINESS_ID}"
UTILITY_DOCKER_USER="${UTILITY_DOCKER_USER:-0:0}"

PREFLIGHT_REPORT="$REPORT_DIR/go-shkeeper-cutover-preflight.json"
PLAN_FILE="$REPORT_DIR/go-shkeeper-final-cutover.plan.json"
READINESS_FILE="$REPORT_DIR/go-shkeeper-final-readiness.json"
SERVERKEY_REPORT="$REPORT_DIR/go-shkeeper-worker-serverkey.json"
DEPLOY_REPORT="$REPORT_DIR/go-shkeeper-final-deploy-check-report.json"
RUNTIME_AUDIT_REPORT="$REPORT_DIR/runtime-audit.json"
GOAL_AUDIT_REPORT="$REPORT_DIR/goal-audit.json"
INVENTORY_FILE="$REPORT_DIR/container-inventory.jsonl"

UPDATE_WORKER_SERVERKEY="${UPDATE_WORKER_SERVERKEY:-0}"
CONFIRM_DB_WRITE="${CONFIRM_DB_WRITE:-}"
RUN_DEPLOY_CHECK="${RUN_DEPLOY_CHECK:-0}"
CONFIRM_REAL_CHAIN_REHEARSAL="${CONFIRM_REAL_CHAIN_REHEARSAL:-}"
WORKER_SECRET_NAMES="${WORKER_SECRET_NAMES:-BTC LTC DOGE FIRO LIGHTNING ETH TRON BNB POLYGON AVALANCHE ARBITRUM OPTIMISM SOLANA XMR XRP}"

SECRET_MOUNTS=()
SECRET_MOUNT_TARGETS=" "
FINAL_SECRET_ENVS=()
DEPLOY_SECRET_ENVS=()
SERVERKEY_SECRET_ENVS=()
FINAL_ENV=()
DEPLOY_ENV=()

log() {
  printf '%s\n' "$*"
}

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
  echo "Unsupported database URL. Set MARIADB_DATABASE_URL to mariadb://user:password@mariadb:3306/shkeeper." >&2
  exit 2
}

require_file() {
  local path="$1"
  local label="$2"
  if [ ! -s "$path" ]; then
    echo "Missing required $label: $path" >&2
    exit 2
  fi
}

require_report_status() {
  local path="$1"
  local want="$2"
  local label="$3"
  require_file "$path" "$label"
  if ! grep -Eq '"status"[[:space:]]*:[[:space:]]*"'$want'"' "$path"; then
    echo "$label is not $want: $path" >&2
    cat "$path" >&2
    exit 1
  fi
}

run_goal_audit() {
  docker ps --format '{{json .}}' > "$INVENTORY_FILE"
  set +e
  docker run --rm \
    --user "$UTILITY_DOCKER_USER" \
    -v "$REPORT_DIR:/deploy-reports" \
    -e GOAL_AUDIT_RUNTIME_AUDIT_FILE=/deploy-reports/runtime-audit.json \
    -e GOAL_AUDIT_PREFLIGHT_FILE=/deploy-reports/go-shkeeper-cutover-preflight.json \
    -e GOAL_AUDIT_READINESS_FILE=/deploy-reports/go-shkeeper-final-readiness.json \
    -e GOAL_AUDIT_DEPLOY_REPORT_FILES=/deploy-reports/go-shkeeper-final-deploy-check-report.json \
    -e GOAL_AUDIT_RELEASE_AUDIT_FILE=/deploy-reports/go-shkeeper-release-audit.json \
    -e GOAL_AUDIT_POST_CUTOVER_FILE=/deploy-reports/go-shkeeper-post-cutover.json \
    -e GOAL_AUDIT_CONTAINER_INVENTORY_FILE=/deploy-reports/container-inventory.jsonl \
    -e GOAL_AUDIT_CONTAINER_STATS_FILE=/deploy-reports/container-stats.jsonl \
    -e GOAL_AUDIT_MAX_CONTAINER_MEMORY_MB="${GOAL_AUDIT_MAX_CONTAINER_MEMORY_MB:-512}" \
    -e GOAL_AUDIT_OUTPUT_FILE=/deploy-reports/goal-audit.json \
    "$GO_SHKEEPER_IMAGE" /app/goal-audit
  local status=$?
  set -e
  if [ "$status" -eq 0 ]; then
    log "goal_audit_pass report=$GOAL_AUDIT_REPORT"
  else
    log "goal_audit_blocked report=$GOAL_AUDIT_REPORT"
  fi
  return "$status"
}

add_secret_mount() {
  local source_path="$1"
  local container_name="$2"
  require_file "$source_path" "secret file"
  if [[ "$SECRET_MOUNT_TARGETS" == *" $container_name "* ]]; then
    return 0
  fi
  SECRET_MOUNT_TARGETS="$SECRET_MOUNT_TARGETS$container_name "
  SECRET_MOUNTS+=("-v" "$source_path:/run/secrets/$container_name:ro")
}

add_final_secret() {
  local source_var="$1"
  local target_var="$2"
  local container_name="$3"
  local source_path="${!source_var:-}"
  if [ -z "$source_path" ]; then
    return 0
  fi
  add_secret_mount "$source_path" "$container_name"
  FINAL_SECRET_ENVS+=("-e" "$target_var=/run/secrets/$container_name")
}

add_deploy_secret() {
  local source_var="$1"
  local target_var="$2"
  local container_name="$3"
  local source_path="${!source_var:-}"
  if [ -z "$source_path" ]; then
    return 0
  fi
  add_secret_mount "$source_path" "$container_name"
  DEPLOY_SECRET_ENVS+=("-e" "$target_var=/run/secrets/$container_name")
}

add_serverkey_secret() {
  local source_var="$1"
  local target_var="$2"
  local container_name="$3"
  local source_path="${!source_var:-}"
  if [ -z "$source_path" ]; then
    return 0
  fi
  add_secret_mount "$source_path" "$container_name"
  SERVERKEY_SECRET_ENVS+=("-e" "$target_var=/run/secrets/$container_name")
}

add_env_if_set() {
  local target="$1"
  local key="$2"
  local value="$3"
  if [ -z "$value" ]; then
    return 0
  fi
  case "$target" in
    final)
      FINAL_ENV+=("-e" "$key=$value")
      ;;
    deploy)
      DEPLOY_ENV+=("-e" "$key=$value")
      ;;
  esac
}

forward_amount_envs() {
  local name value final_name final_value
  while IFS='=' read -r name value; do
    case "$name" in
      FINAL_PLAN_PAYMENT_AMOUNT|FINAL_PLAN_PAYMENT_AMOUNT_*|FINAL_PLAN_PAYOUT_AMOUNT|FINAL_PLAN_PAYOUT_AMOUNT_*)
        FINAL_ENV+=("-e" "$name=$value")
        ;;
      DEPLOY_CHECK_PAYMENT_AMOUNT|DEPLOY_CHECK_PAYMENT_AMOUNT_*|DEPLOY_CHECK_PAYOUT_AMOUNT|DEPLOY_CHECK_PAYOUT_AMOUNT_*)
        DEPLOY_ENV+=("-e" "$name=$value")
        final_name="${name/DEPLOY_CHECK/FINAL_PLAN}"
        final_value="${!final_name:-}"
        if [ -z "$final_value" ]; then
          FINAL_ENV+=("-e" "$final_name=$value")
        fi
        ;;
    esac
  done < <(env)
}

add_secret_file_envs() {
  local worker suffix lower source_var
  add_final_secret API_KEY_FILE FINAL_PLAN_API_KEY_FILE api_key
  add_deploy_secret API_KEY_FILE DEPLOY_CHECK_API_KEY_FILE api_key
  add_final_secret ADMIN_PASSWORD_FILE FINAL_PLAN_ADMIN_PASSWORD_FILE admin_password
  add_deploy_secret ADMIN_PASSWORD_FILE DEPLOY_CHECK_ADMIN_PASSWORD_FILE admin_password
  add_final_secret ADMIN_UPDATE_PASSWORD_FILE FINAL_PLAN_ADMIN_UPDATE_PASSWORD_FILE admin_update_password
  add_deploy_secret ADMIN_UPDATE_PASSWORD_FILE DEPLOY_CHECK_ADMIN_UPDATE_PASSWORD_FILE admin_update_password
  add_final_secret WORKER_PASSWORD_FILE FINAL_PLAN_WORKER_PASSWORD_FILE worker_password
  add_deploy_secret WORKER_PASSWORD_FILE DEPLOY_CHECK_WORKER_PASSWORD_FILE worker_password
  add_serverkey_secret WORKER_PASSWORD_FILE WORKER_PASSWORD_FILE worker_password
  add_serverkey_secret WORKER_SERVERKEY_FILE WORKER_SERVERKEY_FILE worker_serverkey

  for worker in $WORKER_SECRET_NAMES; do
    suffix="${worker^^}"
    suffix="${suffix//-/_}"
    lower="${suffix,,}"
    source_var="WORKER_PASSWORD_${suffix}_FILE"
    add_final_secret "$source_var" "FINAL_PLAN_WORKER_PASSWORD_${suffix}_FILE" "worker_password_$lower"
    add_deploy_secret "$source_var" "DEPLOY_CHECK_WORKER_PASSWORD_${suffix}_FILE" "worker_password_$lower"
  done
}

add_worker_username_envs() {
  local worker suffix source_var value
  add_env_if_set final FINAL_PLAN_WORKER_USERNAME "${WORKER_USERNAME:-}"
  add_env_if_set deploy DEPLOY_CHECK_WORKER_USERNAME "${WORKER_USERNAME:-}"
  for worker in $WORKER_SECRET_NAMES; do
    suffix="${worker^^}"
    suffix="${suffix//-/_}"
    source_var="WORKER_USERNAME_${suffix}"
    value="${!source_var:-}"
    add_env_if_set final "FINAL_PLAN_WORKER_USERNAME_${suffix}" "$value"
    add_env_if_set deploy "DEPLOY_CHECK_WORKER_USERNAME_${suffix}" "$value"
  done
}

MYSQL_ROOT_PASSWORD="${MYSQL_ROOT_PASSWORD:-$(mysql_root_password)}"
if [ -z "$MYSQL_ROOT_PASSWORD" ]; then
  echo "MYSQL_ROOT_PASSWORD is empty in container $MARIADB_CONTAINER" >&2
  exit 2
fi

MARIADB_DATABASE_URL="${MARIADB_DATABASE_URL:-mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/$MARIADB_DATABASE}"
require_mariadb_url "$MARIADB_DATABASE_URL"

mkdir -p "$REPORT_DIR"
chmod 0777 "$REPORT_DIR"

add_secret_file_envs
add_worker_username_envs
forward_amount_envs

log "readiness_id=$READINESS_ID"
log "report_dir=$REPORT_DIR"
log "image=$GO_SHKEEPER_IMAGE"

if [ "$BUILD_IMAGE" = "1" ]; then
  docker build -t "$GO_SHKEEPER_IMAGE" -f "$REPO_ROOT/Dockerfile" "$REPO_ROOT"
fi

docker run --rm \
  -v "$REPORT_DIR:/deploy-reports" \
  "$GO_SHKEEPER_IMAGE" sh -lc '/app/runtime-audit > /deploy-reports/runtime-audit.json'
require_report_status "$RUNTIME_AUDIT_REPORT" ok "runtime audit report"

docker run --rm --network "$NETWORK" \
  -v "$REPORT_DIR:/deploy-reports" \
  -e MARIADB_DATABASE_URL="$MARIADB_DATABASE_URL" \
  -e CUTOVER_PREFLIGHT_OUTPUT_FILE=/deploy-reports/go-shkeeper-cutover-preflight.json \
  "$GO_SHKEEPER_IMAGE" /app/cutover-preflight
require_report_status "$PREFLIGHT_REPORT" pass "cutover preflight report"

if [ "$UPDATE_WORKER_SERVERKEY" = "1" ]; then
  if [ "$CONFIRM_DB_WRITE" != "GO_SHKEEPER_PRODUCTION" ]; then
    echo "Refusing wallet.serverkey update. Set CONFIRM_DB_WRITE=GO_SHKEEPER_PRODUCTION with UPDATE_WORKER_SERVERKEY=1." >&2
    exit 2
  fi
  docker run --rm --network "$NETWORK" \
    --user "$UTILITY_DOCKER_USER" \
    "${SECRET_MOUNTS[@]}" \
    -v "$REPORT_DIR:/deploy-reports" \
    -e MARIADB_DATABASE_URL="$MARIADB_DATABASE_URL" \
    -e WORKER_USERNAME="${WORKER_USERNAME:-}" \
    -e WORKER_SERVERKEY_CRYPTOS="${WORKER_SERVERKEY_CRYPTOS:-}" \
    -e WORKER_SERVERKEY_REPORT_FILE=/deploy-reports/go-shkeeper-worker-serverkey.json \
    "${SERVERKEY_SECRET_ENVS[@]}" \
    "$GO_SHKEEPER_IMAGE" /app/worker-serverkey
  require_report_status "$SERVERKEY_REPORT" ok "worker serverkey report"
fi

FINAL_ENV+=(
  "-e" "MARIADB_DATABASE_URL=$MARIADB_DATABASE_URL"
  "-e" "FINAL_PLAN_MARIADB_DATABASE_URL=$MARIADB_DATABASE_URL"
  "-e" "FINAL_PLAN_OUTPUT_FILE=/deploy-reports/go-shkeeper-final-cutover.plan.json"
  "-e" "FINAL_PLAN_READINESS_FILE=/deploy-reports/go-shkeeper-final-readiness.json"
  "-e" "FINAL_PLAN_REPORT_FILE=/deploy-reports/go-shkeeper-final-deploy-check-report.json"
  "-e" "FINAL_PLAN_USE_WALLET_CRYPTOS=${FINAL_PLAN_USE_WALLET_CRYPTOS:-true}"
  "-e" "FINAL_PLAN_USE_WALLET_API_KEY=${FINAL_PLAN_USE_WALLET_API_KEY:-false}"
  "-e" "FINAL_PLAN_USE_WALLET_SERVERKEY=${FINAL_PLAN_USE_WALLET_SERVERKEY:-false}"
  "-e" "FINAL_PLAN_REDACT_SECRETS=${FINAL_PLAN_REDACT_SECRETS:-true}"
)
add_env_if_set final FINAL_PLAN_ADMIN_USERNAME "${ADMIN_USERNAME:-}"
add_env_if_set final FINAL_PLAN_ADMIN_UPDATE_USERNAME "${ADMIN_UPDATE_USERNAME:-}"

docker run --rm --network "$NETWORK" \
  --user "$UTILITY_DOCKER_USER" \
  "${SECRET_MOUNTS[@]}" \
  -v "$REPORT_DIR:/deploy-reports" \
  "${FINAL_ENV[@]}" \
  "${FINAL_SECRET_ENVS[@]}" \
  "$GO_SHKEEPER_IMAGE" /app/final-plan

require_file "$PLAN_FILE" "final deploy-check plan"
require_file "$READINESS_FILE" "final readiness report"
if ! grep -Eq '"status"[[:space:]]*:[[:space:]]*"ready"' "$READINESS_FILE"; then
  echo "final_readiness_blocked report_dir=$REPORT_DIR" >&2
  cat "$READINESS_FILE" >&2
  run_goal_audit || true
  exit 1
fi

if [ "$RUN_DEPLOY_CHECK" = "1" ]; then
  if [ "$CONFIRM_REAL_CHAIN_REHEARSAL" != "GO_SHKEEPER_PRODUCTION" ]; then
    echo "Refusing mutating deploy-check. Set CONFIRM_REAL_CHAIN_REHEARSAL=GO_SHKEEPER_PRODUCTION with RUN_DEPLOY_CHECK=1." >&2
    exit 2
  fi
  DEPLOY_ENV+=(
    "-e" "DEPLOY_CHECK_PLAN_FILE=/deploy-reports/go-shkeeper-final-cutover.plan.json"
    "-e" "DEPLOY_CHECK_REPORT_FILE=/deploy-reports/go-shkeeper-final-deploy-check-report.json"
  )
  add_env_if_set deploy DEPLOY_CHECK_ADMIN_USERNAME "${ADMIN_USERNAME:-}"
  add_env_if_set deploy DEPLOY_CHECK_ADMIN_UPDATE_USERNAME "${ADMIN_UPDATE_USERNAME:-}"
  docker run --rm --network "$NETWORK" \
    --user "$UTILITY_DOCKER_USER" \
    "${SECRET_MOUNTS[@]}" \
    -v "$REPORT_DIR:/deploy-reports" \
    "${DEPLOY_ENV[@]}" \
    "${DEPLOY_SECRET_ENVS[@]}" \
    "$GO_SHKEEPER_IMAGE" /app/deploy-check
  require_report_status "$DEPLOY_REPORT" ok "final deploy-check report"
else
  log "deploy_check_skipped; set RUN_DEPLOY_CHECK=1 CONFIRM_REAL_CHAIN_REHEARSAL=GO_SHKEEPER_PRODUCTION for the real-chain rehearsal"
fi

run_goal_audit || true

log "final_readiness_ready report_dir=$REPORT_DIR plan_file=$PLAN_FILE readiness_file=$READINESS_FILE"
