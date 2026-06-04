#!/usr/bin/env bash
set -euo pipefail

# Production cutover entrypoint for hk-16-16.
# Defaults to DRY_RUN=1. To stop legacy containers, set:
#   DRY_RUN=0 CONFIRM_PRODUCTION_CUTOVER=GO_SHKEEPER_HK_16_16

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

DRY_RUN="${DRY_RUN:-1}"
CONFIRM_PRODUCTION_CUTOVER="${CONFIRM_PRODUCTION_CUTOVER:-}"
ROLLBACK_ON_FAILURE="${ROLLBACK_ON_FAILURE:-1}"
BUILD_IMAGE="${BUILD_IMAGE:-1}"
NETWORK="${SHKEEPER_DOCKER_NETWORK:-shkeeper_default}"
MARIADB_CONTAINER="${MARIADB_CONTAINER:-mariadb}"
MARIADB_HOST="${MARIADB_HOST:-$MARIADB_CONTAINER}"
COMPOSE_FILE="${GO_SHKEEPER_COMPOSE_FILE:-$SCRIPT_DIR/hk-16-16.modular.example.yml}"
COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-shkeeper}"
CUTOVER_ID="${CUTOVER_ID:-go-cutover-$(date +%Y%m%d%H%M%S)}"
REPORT_DIR="${REPORT_DIR:-/tmp/$CUTOVER_ID}"
GO_SHKEEPER_IMAGE="${GO_SHKEEPER_IMAGE:-go-shkeeper:$CUTOVER_ID}"
CURL_IMAGE="${CURL_IMAGE:-curlimages/curl:8.10.1}"
UTILITY_DOCKER_USER="${UTILITY_DOCKER_USER:-0:0}"

PLAN_FILE="${PLAN_FILE:-$REPORT_DIR/go-shkeeper-final-cutover.plan.json}"
FINAL_DEPLOY_REPORT_FILE="${FINAL_DEPLOY_REPORT_FILE:-$REPORT_DIR/go-shkeeper-final-deploy-check-report.json}"
FINAL_READINESS_REPORT_FILE="${FINAL_READINESS_REPORT_FILE:-$REPORT_DIR/go-shkeeper-final-readiness.json}"
RUNTIME_AUDIT_REPORT="$REPORT_DIR/runtime-audit.json"
MAIN_IMPORT_REPORT="$REPORT_DIR/go-shkeeper-main-import-report.json"
PREFLIGHT_REPORT="$REPORT_DIR/go-shkeeper-cutover-preflight.json"
AUDIT_REPORT="$REPORT_DIR/go-shkeeper-cutover-audit.json"
POST_CUTOVER_REPORT="$REPORT_DIR/go-shkeeper-post-cutover.json"
RELEASE_AUDIT_REPORT="$REPORT_DIR/go-shkeeper-release-audit.json"
GOAL_AUDIT_REPORT="$REPORT_DIR/go-shkeeper-goal-audit.json"
INVENTORY_FILE="$REPORT_DIR/container-inventory.jsonl"
OLD_INVENTORY_FILE="$REPORT_DIR/legacy-container-inventory.jsonl"
CONTAINER_STATS_FILE="$REPORT_DIR/container-stats.jsonl"

LEGACY_CONTAINERS=(${LEGACY_CONTAINERS:-shkeeper bnb-shkeeper bnb_tasks tron-shkeeper tron_tasks})
GO_SERVICES=(${GO_SERVICES:-shkeeper btc-worker ltc-worker doge-worker firo-worker btc-lightning-worker eth-worker tron-worker bnb-worker polygon-worker avalanche-worker arbitrum-worker optimism-worker solana-worker xmr-worker xrp-worker})
GO_CONTAINERS=(${GO_CONTAINERS:-go-shkeeper go-btc-worker go-ltc-worker go-doge-worker go-firo-worker go-btc-lightning-worker go-eth-worker go-tron-worker go-bnb-worker go-polygon-worker go-avalanche-worker go-arbitrum-worker go-optimism-worker go-solana-worker go-xmr-worker go-xrp-worker})

rollback_needed=0
image_built=0

log() {
  printf '%s\n' "$*"
}

dry_run_command() {
  printf '[dry-run]'
  local arg redacted password
  password="${MYSQL_ROOT_PASSWORD:-}"
  for arg in "$@"; do
    redacted="$arg"
    if [ -n "$password" ]; then
      redacted="${redacted//$password/REDACTED}"
    fi
    printf ' %q' "$redacted"
  done
  printf '\n'
}

run() {
  if [ "$DRY_RUN" = "1" ]; then
    dry_run_command "$@"
    return 0
  fi
  "$@"
}

require_execute_confirmation() {
  if [ "$DRY_RUN" = "1" ]; then
    log "dry_run=1; no production containers will be stopped"
    return 0
  fi
  if [ "$CONFIRM_PRODUCTION_CUTOVER" != "GO_SHKEEPER_HK_16_16" ]; then
    echo "Refusing production cutover. Set CONFIRM_PRODUCTION_CUTOVER=GO_SHKEEPER_HK_16_16 and DRY_RUN=0." >&2
    exit 2
  fi
}

require_file() {
  local path="$1"
  local label="$2"
  if [ ! -s "$path" ]; then
    echo "Missing required $label: $path" >&2
    exit 2
  fi
}

require_generated_file() {
  local path="$1"
  local label="$2"
  if [ "$DRY_RUN" = "1" ]; then
    log "[dry-run] would require generated $label: $path"
    return 0
  fi
  require_file "$path" "$label"
}

mysql_root_password() {
  docker exec "$MARIADB_CONTAINER" printenv MYSQL_ROOT_PASSWORD
}

compose() {
  COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT_NAME" GO_SHKEEPER_IMAGE="$GO_SHKEEPER_IMAGE" docker compose -f "$COMPOSE_FILE" "$@"
}

stop_legacy_containers() {
  local container
  for container in "$@"; do
    if docker inspect "$container" >/dev/null 2>&1; then
      run docker stop "$container"
    else
      log "legacy container not present, skipping: $container"
    fi
  done
}

wait_ready() {
  local url="$1"
  local i
  for i in $(seq 1 90); do
    if docker run --rm --network "$NETWORK" "$CURL_IMAGE" -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "ready timeout: $url" >&2
  return 1
}

connect_go_containers_to_network() {
  local container
  for container in "${GO_CONTAINERS[@]}"; do
    if docker inspect "$container" >/dev/null 2>&1; then
      docker network connect "$NETWORK" "$container" >/dev/null 2>&1 || true
    fi
  done
}

rollback() {
  if [ "$rollback_needed" != "1" ] || [ "$ROLLBACK_ON_FAILURE" != "1" ] || [ "$DRY_RUN" = "1" ]; then
    return 0
  fi
  echo "cutover failed; rolling back legacy containers" >&2
  docker rm -f "${GO_CONTAINERS[@]}" >/dev/null 2>&1 || true
  local container
  for container in "${LEGACY_CONTAINERS[@]}"; do
    docker start "$container" >/dev/null 2>&1 || true
  done
}

cleanup() {
  local status=$?
  if [ "$status" -ne 0 ]; then
    rollback
  fi
  exit "$status"
}
trap cleanup EXIT

require_execute_confirmation
mkdir -p "$REPORT_DIR"
if [ "$DRY_RUN" != "1" ]; then
  chmod 0777 "$REPORT_DIR"
fi

require_file "$PLAN_FILE" "final deploy-check plan"
require_file "$FINAL_DEPLOY_REPORT_FILE" "final deploy-check report"
require_file "$FINAL_READINESS_REPORT_FILE" "final readiness report"

MYSQL_ROOT_PASSWORD="${MYSQL_ROOT_PASSWORD:-$(mysql_root_password)}"
if [ -z "$MYSQL_ROOT_PASSWORD" ]; then
  echo "MYSQL_ROOT_PASSWORD is empty in container $MARIADB_CONTAINER" >&2
  exit 2
fi

MARIADB_DATABASE_URL="${MARIADB_DATABASE_URL:-mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/shkeeper}"
LEGACY_MAIN_DATABASE_URL="${LEGACY_MAIN_DATABASE_URL:-mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/shkeeper}"
export MYSQL_ROOT_PASSWORD GO_SHKEEPER_IMAGE

log "cutover_id=$CUTOVER_ID"
log "report_dir=$REPORT_DIR"
log "image=$GO_SHKEEPER_IMAGE"

if [ "$BUILD_IMAGE" = "1" ]; then
  run docker build -t "$GO_SHKEEPER_IMAGE" -f "$REPO_ROOT/Dockerfile" "$REPO_ROOT"
  image_built=1
fi

run docker run --rm \
  --user "$UTILITY_DOCKER_USER" \
  -v "$REPORT_DIR:/deploy-reports" \
  "$GO_SHKEEPER_IMAGE" sh -lc '/app/runtime-audit > /deploy-reports/runtime-audit.json'
require_generated_file "$RUNTIME_AUDIT_REPORT" "runtime audit report"
run docker ps --format '{{json .}}'
if [ "$DRY_RUN" != "1" ]; then
  docker ps --format '{{json .}}' > "$OLD_INVENTORY_FILE"
fi

log "stopping legacy containers: ${LEGACY_CONTAINERS[*]}"
stop_legacy_containers "${LEGACY_CONTAINERS[@]}"
rollback_needed=1

run docker run --rm --network "$NETWORK" \
  --user "$UTILITY_DOCKER_USER" \
  -v "$REPORT_DIR:/deploy-reports" \
  -e MARIADB_DATABASE_URL="$MARIADB_DATABASE_URL" \
  -e LEGACY_MAIN_DATABASE_URL="$LEGACY_MAIN_DATABASE_URL" \
  -e IMPORT_LEGACY_MAIN_REPORT_FILE=/deploy-reports/go-shkeeper-main-import-report.json \
  "$GO_SHKEEPER_IMAGE" /app/import-legacy-main-mariadb
require_generated_file "$MAIN_IMPORT_REPORT" "main import report"

run docker run --rm --network "$NETWORK" \
  --user "$UTILITY_DOCKER_USER" \
  -v "$REPORT_DIR:/deploy-reports" \
  -e MARIADB_DATABASE_URL="$MARIADB_DATABASE_URL" \
  -e CUTOVER_PREFLIGHT_OUTPUT_FILE=/deploy-reports/go-shkeeper-cutover-preflight.json \
  "$GO_SHKEEPER_IMAGE" /app/cutover-preflight
require_generated_file "$PREFLIGHT_REPORT" "cutover preflight report"

run docker run --rm \
  --user "$UTILITY_DOCKER_USER" \
  -v "$PLAN_FILE:/deploy-check.plan.json:ro" \
  -v "$FINAL_DEPLOY_REPORT_FILE:/deploy-check-report.json:ro" \
  -v "$REPORT_DIR:/deploy-reports" \
  -e CUTOVER_AUDIT_PLAN_FILE=/deploy-check.plan.json \
  -e CUTOVER_AUDIT_REPORT_FILES=/deploy-check-report.json \
  -e CUTOVER_AUDIT_IMPORT_REPORT_FILES=/deploy-reports/go-shkeeper-main-import-report.json \
  -e CUTOVER_AUDIT_PREFLIGHT_REPORT_FILES=/deploy-reports/go-shkeeper-cutover-preflight.json \
  -e CUTOVER_AUDIT_OUTPUT_FILE=/deploy-reports/go-shkeeper-cutover-audit.json \
  -e CUTOVER_AUDIT_REQUIRE_IMPORT_SYNC=true \
  -e CUTOVER_AUDIT_REQUIRE_PREFLIGHT=true \
  -e CUTOVER_AUDIT_REQUIRE_ORDER_STATUS_MATRIX=true \
  -e CUTOVER_AUDIT_REQUIRE_WORKER_ADDRESS=true \
  -e CUTOVER_AUDIT_REQUIRE_PAYOUT_TXID=true \
  "$GO_SHKEEPER_IMAGE" /app/cutover-audit
require_generated_file "$AUDIT_REPORT" "cutover audit report"

log "starting Go modular stack services: ${GO_SERVICES[*]}"
if [ "$DRY_RUN" = "1" ]; then
  log "[dry-run] COMPOSE_PROJECT_NAME=$COMPOSE_PROJECT_NAME GO_SHKEEPER_IMAGE=$GO_SHKEEPER_IMAGE docker compose -f $COMPOSE_FILE up -d --no-deps ${GO_SERVICES[*]}"
else
  compose up -d --no-deps "${GO_SERVICES[@]}"
  connect_go_containers_to_network
  wait_ready "http://go-shkeeper:5000/readyz"
fi

if [ "$DRY_RUN" != "1" ]; then
  docker ps --format '{{json .}}' > "$INVENTORY_FILE"
  docker stats --no-stream --format '{{json .}}' "${GO_CONTAINERS[@]}" > "$CONTAINER_STATS_FILE"
  chmod 0444 "$CONTAINER_STATS_FILE"
fi
require_generated_file "$CONTAINER_STATS_FILE" "container stats report"

run docker run --rm --network "$NETWORK" \
  --user "$UTILITY_DOCKER_USER" \
  -v "$REPORT_DIR:/deploy-reports:ro" \
  -e POST_CUTOVER_MAIN_URL="${POST_CUTOVER_MAIN_URL:-http://go-shkeeper:5000}" \
  -e POST_CUTOVER_WORKER_URLS="${POST_CUTOVER_WORKER_URLS:-btc=http://go-btc-worker:6000,ltc=http://go-ltc-worker:6000,doge=http://go-doge-worker:6000,firo=http://go-firo-worker:6000,lightning=http://go-btc-lightning-worker:6000,eth=http://go-eth-worker:6000,tron=http://go-tron-worker:6000,bnb=http://go-bnb-worker:6000,polygon=http://go-polygon-worker:6000,avalanche=http://go-avalanche-worker:6000,arbitrum=http://go-arbitrum-worker:6000,optimism=http://go-optimism-worker:6000,xmr=http://go-xmr-worker:6000,xrp=http://go-xrp-worker:6000,solana=http://go-solana-worker:6000}" \
  -e POST_CUTOVER_DEPLOY_REPORT_FILES=/deploy-reports/go-shkeeper-final-deploy-check-report.json \
  -e POST_CUTOVER_AUDIT_FILE=/deploy-reports/go-shkeeper-cutover-audit.json \
  -e POST_CUTOVER_CONTAINER_INVENTORY_FILE=/deploy-reports/container-inventory.jsonl \
  -e POST_CUTOVER_EXPECTED_CONTAINERS="${POST_CUTOVER_EXPECTED_CONTAINERS:-go-shkeeper=go-shkeeper,go-btc-worker=go-shkeeper,go-ltc-worker=go-shkeeper,go-doge-worker=go-shkeeper,go-firo-worker=go-shkeeper,go-btc-lightning-worker=go-shkeeper,go-eth-worker=go-shkeeper,go-tron-worker=go-shkeeper,go-bnb-worker=go-shkeeper,go-polygon-worker=go-shkeeper,go-avalanche-worker=go-shkeeper,go-arbitrum-worker=go-shkeeper,go-optimism-worker=go-shkeeper,go-solana-worker=go-shkeeper,go-xmr-worker=go-shkeeper,go-xrp-worker=go-shkeeper}" \
  -e POST_CUTOVER_FORBIDDEN_CONTAINERS="${POST_CUTOVER_FORBIDDEN_CONTAINERS:-shkeeper,bnb-shkeeper,bnb_tasks,tron-shkeeper,tron_tasks}" \
  -e POST_CUTOVER_EXPECTED_IMAGES="${POST_CUTOVER_EXPECTED_IMAGES:-go-shkeeper}" \
  -e POST_CUTOVER_FORBIDDEN_IMAGES="${POST_CUTOVER_FORBIDDEN_IMAGES:-ghcr.io/sky-jd/shkeeper.io-zh,vsyshost/bnb-shkeeper,vsyshost/tron-shkeeper,python:}" \
  -e POST_CUTOVER_REQUIRE_AUDIT_ORDER_STATUS_MATRIX=true \
  -e POST_CUTOVER_REQUIRE_AUDIT_PAYOUT_TXID=true \
  -e POST_CUTOVER_OUTPUT_FILE=/deploy-reports/go-shkeeper-post-cutover.json \
  "$GO_SHKEEPER_IMAGE" /app/post-cutover-verify
require_generated_file "$POST_CUTOVER_REPORT" "post-cutover report"

run docker run --rm \
  --user "$UTILITY_DOCKER_USER" \
  -v "$PLAN_FILE:/deploy-check.plan.json:ro" \
  -v "$FINAL_DEPLOY_REPORT_FILE:/deploy-check-report.json:ro" \
  -v "$REPORT_DIR:/deploy-reports" \
  -e RELEASE_AUDIT_PLAN_FILE=/deploy-check.plan.json \
  -e RELEASE_AUDIT_DEPLOY_REPORT_FILES=/deploy-check-report.json \
  -e RELEASE_AUDIT_CUTOVER_AUDIT_FILE=/deploy-reports/go-shkeeper-cutover-audit.json \
  -e RELEASE_AUDIT_POST_CUTOVER_FILE=/deploy-reports/go-shkeeper-post-cutover.json \
  -e RELEASE_AUDIT_REQUIRE_MUTATING=true \
  -e RELEASE_AUDIT_REQUIRE_IMPORT_SYNC=true \
  -e RELEASE_AUDIT_REQUIRE_PREFLIGHT=true \
  -e RELEASE_AUDIT_REQUIRE_ORDER_STATUS_MATRIX=true \
  -e RELEASE_AUDIT_REQUIRE_PAYMENT_COVERAGE=true \
  -e RELEASE_AUDIT_REQUIRE_PAYOUT_COVERAGE=true \
  -e RELEASE_AUDIT_REQUIRE_PAYMENT_ORDER=true \
  -e RELEASE_AUDIT_REQUIRE_PAYOUT_ORDER=true \
  -e RELEASE_AUDIT_REQUIRE_PAYOUT_TXID=true \
  -e RELEASE_AUDIT_REQUIRE_POST_CUTOVER=true \
  -e RELEASE_AUDIT_REQUIRE_WORKER_READY=true \
  -e RELEASE_AUDIT_REQUIRE_WORKER_ADDRESS=true \
  -e RELEASE_AUDIT_OUTPUT_FILE=/deploy-reports/go-shkeeper-release-audit.json \
  "$GO_SHKEEPER_IMAGE" /app/release-audit
require_generated_file "$RELEASE_AUDIT_REPORT" "release audit report"

run docker run --rm \
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
  -e GOAL_AUDIT_OUTPUT_FILE=/deploy-reports/go-shkeeper-goal-audit.json \
  "$GO_SHKEEPER_IMAGE" /app/goal-audit
require_generated_file "$GOAL_AUDIT_REPORT" "goal audit report"

rollback_needed=0
if [ "$DRY_RUN" = "1" ]; then
  log "production_cutover_dry_run_ok report_dir=$REPORT_DIR"
else
  log "production_cutover_ok report_dir=$REPORT_DIR"
fi
