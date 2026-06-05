#!/usr/bin/env bash
set -euo pipefail

# Run from a repository checkout on a deployment host. This creates an isolated
# MariaDB database and temporary Go containers; it does not replace production.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

NETWORK="${SHKEEPER_DOCKER_NETWORK:-shkeeper_default}"
MARIADB_CONTAINER="${MARIADB_CONTAINER:-mariadb}"
MARIADB_HOST="${MARIADB_HOST:-$MARIADB_CONTAINER}"
LEGACY_MAIN_CONTAINER="${LEGACY_MAIN_CONTAINER:-shkeeper}"
LEGACY_BNB_DB="${LEGACY_BNB_DB:-bnb-shkeeper}"
CURL_IMAGE="${CURL_IMAGE:-curlimages/curl:8.10.1}"
BUILD_IMAGE="${BUILD_IMAGE:-1}"
KEEP_REHEARSAL_REPORTS="${KEEP_REHEARSAL_REPORTS:-0}"
KEEP_REHEARSAL_DB="${KEEP_REHEARSAL_DB:-0}"
REHEARSAL_READY_REQUESTS="${REHEARSAL_READY_REQUESTS:-200}"
REHEARSAL_READY_CONCURRENCY="${REHEARSAL_READY_CONCURRENCY:-50}"
REHEARSAL_ORDER_LIST_REQUESTS="${REHEARSAL_ORDER_LIST_REQUESTS:-200}"
REHEARSAL_ORDER_LIST_CONCURRENCY="${REHEARSAL_ORDER_LIST_CONCURRENCY:-50}"
REHEARSAL_ORDER_LIST_MAX_LATENCY_MS="${REHEARSAL_ORDER_LIST_MAX_LATENCY_MS:-500}"

ID="go-rehearsal-$(date +%Y%m%d%H%M%S)-$$"
DB="go_rehearsal_$(date +%Y%m%d%H%M%S)_$$"
IMAGE="${GO_SHKEEPER_IMAGE:-go-shkeeper:$ID}"
MAIN="go-shkeeper-main-$ID"
BNB_WORKER="go-shkeeper-bnb-$ID"
TRON_WORKER="go-shkeeper-tron-$ID"
WORK_DIR="/tmp/$ID"
MAIN_IMPORT_REPORT="$WORK_DIR/main-import-report.json"
PREFLIGHT_REPORT="$WORK_DIR/cutover-preflight.json"
BNB_SERVERKEY_REPORT="$WORK_DIR/bnb-serverkey-report.json"
TRON_SERVERKEY_REPORT="$WORK_DIR/tron-serverkey-report.json"
PLAN="$WORK_DIR/deploy-check.plan.json"
DEPLOY_REPORT="$WORK_DIR/deploy-check-report.json"
AUDIT_REPORT="$WORK_DIR/cutover-audit.json"
RELEASE_AUDIT_REPORT="$WORK_DIR/release-audit.json"
CONTAINER_STATS_REPORT="$WORK_DIR/container-stats.jsonl"

image_built=0

token() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "${1:-16}"
  else
    printf '%s%s' "$(date +%s%N)" "$$"
  fi
}

MYSQL_ROOT_PASSWORD="$(docker exec "$MARIADB_CONTAINER" printenv MYSQL_ROOT_PASSWORD)"
if [ -z "$MYSQL_ROOT_PASSWORD" ]; then
  echo "MYSQL_ROOT_PASSWORD is empty in container $MARIADB_CONTAINER" >&2
  exit 1
fi
LEGACY_MAIN_DATABASE_URL="${LEGACY_MAIN_DATABASE_URL:-mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/shkeeper}"

API_KEY="stage-api-$(token 12)"
ADMIN_USER="stage-admin-$$"
ADMIN_PASS="stage-admin-pass-$(token 10)"
ADMIN_USER2="stage-admin-updated-$$"
ADMIN_PASS2="stage-admin-updated-pass-$(token 10)"
BNB_USER="stage-bnb-worker-$$"
BNB_PASS="stage-bnb-worker-pass-$(token 10)"
TRON_USER="stage-tron-worker-$$"
TRON_PASS="stage-tron-worker-pass-$(token 10)"
BNB_ACCOUNT_PASSWORD="${BNB_ACCOUNT_PASSWORD:-stage-bnb-account-$(token 16)}"
TRON_ACCOUNT_PASSWORD="${TRON_ACCOUNT_PASSWORD:-stage-tron-account-$(token 16)}"
SECRET_KEY="stage-secret-$(token 16)"
SHKEEPER_BACKEND_KEY="$(docker exec "$LEGACY_MAIN_CONTAINER" printenv SHKEEPER_BACKEND_KEY 2>/dev/null || true)"

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    echo "rehearsal_failed status=$status"
    docker logs --tail 120 "$MAIN" 2>&1 || true
    docker logs --tail 120 "$BNB_WORKER" 2>&1 || true
    docker logs --tail 120 "$TRON_WORKER" 2>&1 || true
    [ -f "$PLAN" ] && cat "$PLAN" || true
  fi
  docker rm -f "$MAIN" "$BNB_WORKER" "$TRON_WORKER" >/dev/null 2>&1 || true
  if [ "$KEEP_REHEARSAL_DB" != "1" ]; then
    docker exec "$MARIADB_CONTAINER" mariadb -uroot -p"$MYSQL_ROOT_PASSWORD" -e "DROP DATABASE IF EXISTS \`$DB\`" >/dev/null 2>&1 || true
  fi
  if [ "$image_built" = "1" ]; then
    docker rmi "$IMAGE" >/dev/null 2>&1 || true
  fi
  if [ "$KEEP_REHEARSAL_REPORTS" != "1" ]; then
    rm -rf "$WORK_DIR"
  else
    echo "kept_rehearsal_reports=$WORK_DIR"
  fi
  exit "$status"
}
trap cleanup EXIT

run_sql() {
  local database="$1"
  shift
  docker exec "$MARIADB_CONTAINER" mariadb -uroot -p"$MYSQL_ROOT_PASSWORD" -N -B "$database" "$@"
}

wait_ready() {
  local url="$1"
  local i
  for i in $(seq 1 60); do
    if docker run --rm --network "$NETWORK" "$CURL_IMAGE" -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "ready timeout: $url" >&2
  return 1
}

mkdir -p "$WORK_DIR"
# Reports are written by the non-root user inside the Go image through bind mounts.
chmod 0777 "$WORK_DIR"

echo "rehearsal_start id=$ID db=$DB image=$IMAGE"

if [ "${GO_SHKEEPER_IMAGE:-}" = "" ] && [ "$BUILD_IMAGE" = "1" ]; then
  docker build -t "$IMAGE" -f "$REPO_ROOT/Dockerfile" "$REPO_ROOT"
  image_built=1
fi

docker run --rm "$IMAGE" /app/runtime-audit

docker exec "$MARIADB_CONTAINER" mariadb -uroot -p"$MYSQL_ROOT_PASSWORD" \
  -e "CREATE DATABASE \`$DB\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"

docker run --rm --network "$NETWORK" \
  -v "$WORK_DIR:/reports" \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/$DB" \
  -e LEGACY_MAIN_DATABASE_URL="$LEGACY_MAIN_DATABASE_URL" \
  -e IMPORT_LEGACY_MAIN_REPORT_FILE=/reports/main-import-report.json \
  "$IMAGE" /app/import-legacy-main-mariadb

if [ ! -s "$MAIN_IMPORT_REPORT" ]; then
  echo "main import did not write report $MAIN_IMPORT_REPORT" >&2
  exit 1
fi
chmod 0444 "$MAIN_IMPORT_REPORT"

if [ -n "$SHKEEPER_BACKEND_KEY" ]; then
  docker run --rm --network "$NETWORK" \
    -e CHAIN_MODULE=BNB \
    -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/$DB" \
    -e LEGACY_ACCOUNTS_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/$LEGACY_BNB_DB" \
    -e ACCOUNT_PASSWORD="$BNB_ACCOUNT_PASSWORD" \
    -e LEGACY_ACCOUNT_DECRYPT_URL="http://$LEGACY_MAIN_CONTAINER:5000/api/v1/BNB/decrypt" \
    -e LEGACY_ACCOUNT_BACKEND_KEY="$SHKEEPER_BACKEND_KEY" \
    "$IMAGE" /app/import-legacy-accounts
else
  echo "warning: SHKEEPER_BACKEND_KEY is empty; skipping BNB legacy account import" >&2
fi

run_sql "$DB" -e "
INSERT INTO wallet (crypto, apikey, enabled, llimit, ulimit, recalc, confirmations, ppolicy, prespolicy)
VALUES
  ('BNB', '$API_KEY', 1, 95, 105, 0, 1, 'MANUAL', 'DISABLE'),
  ('BNB-USDT', '$API_KEY', 1, 95, 105, 0, 1, 'MANUAL', 'DISABLE'),
  ('TRX', '$API_KEY', 1, 95, 105, 0, 1, 'MANUAL', 'DISABLE'),
  ('USDT', '$API_KEY', 1, 95, 105, 0, 1, 'MANUAL', 'DISABLE'),
  ('USDC', '$API_KEY', 1, 95, 105, 0, 1, 'MANUAL', 'DISABLE')
ON DUPLICATE KEY UPDATE
  apikey = VALUES(apikey), enabled = 1, llimit = VALUES(llimit),
  ulimit = VALUES(ulimit), confirmations = VALUES(confirmations);
INSERT INTO exchange_rate (source, crypto, fiat, rate, fee, fixed_fee, fee_policy)
VALUES
  ('manual','BNB','USD',1,0,0,'NO_FEE'),
  ('manual','BNB-USDT','USD',1,0,0,'NO_FEE'),
  ('manual','TRX','USD',1,0,0,'NO_FEE'),
  ('manual','USDT','USD',1,0,0,'NO_FEE'),
  ('manual','USDC','USD',1,0,0,'NO_FEE')
ON DUPLICATE KEY UPDATE
  source = VALUES(source), rate = VALUES(rate), fee = VALUES(fee),
  fixed_fee = VALUES(fixed_fee), fee_policy = VALUES(fee_policy);
"

docker run --rm --network "$NETWORK" \
  -v "$WORK_DIR:/reports" \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/$DB" \
  -e CUTOVER_PREFLIGHT_OUTPUT_FILE=/reports/cutover-preflight.json \
  "$IMAGE" /app/cutover-preflight
chmod 0444 "$PREFLIGHT_REPORT"

ORDER_CRYPTO="$(run_sql "$DB" -e "SELECT crypto FROM invoice WHERE status='UNPAID' AND crypto IN ('USDT','TRX','USDC','BNB-USDT') GROUP BY crypto ORDER BY COUNT(*) DESC LIMIT 1" | head -1 | tr -d '\r')"
if [ -z "$ORDER_CRYPTO" ]; then
  ORDER_CRYPTO="USDT"
  ORDER_LIST_CHECK=false
  ORDER_LIST_MIN=0
else
  ORDER_LIST_CHECK=true
  ORDER_LIST_MIN=1
fi

docker run --rm --network "$NETWORK" \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/$DB" \
  -e ADMIN_USERNAME="$ADMIN_USER" \
  -e ADMIN_PASSWORD="$ADMIN_PASS" \
  "$IMAGE" /app/admin-account

docker run --rm --network "$NETWORK" \
  -v "$WORK_DIR:/reports" \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/$DB" \
  -e WORKER_SERVERKEY_CRYPTOS=BNB,BNB-USDT \
  -e WORKER_USERNAME="$BNB_USER" \
  -e WORKER_PASSWORD="$BNB_PASS" \
  -e WORKER_SERVERKEY_REPORT_FILE=/reports/bnb-serverkey-report.json \
  "$IMAGE" /app/worker-serverkey
chmod 0444 "$BNB_SERVERKEY_REPORT"

docker run --rm --network "$NETWORK" \
  -v "$WORK_DIR:/reports" \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/$DB" \
  -e WORKER_SERVERKEY_CRYPTOS=TRX,USDT,USDC \
  -e WORKER_USERNAME="$TRON_USER" \
  -e WORKER_PASSWORD="$TRON_PASS" \
  -e WORKER_SERVERKEY_REPORT_FILE=/reports/tron-serverkey-report.json \
  "$IMAGE" /app/worker-serverkey
chmod 0444 "$TRON_SERVERKEY_REPORT"

docker run -d --name "$BNB_WORKER" --network "$NETWORK" \
  -e CHAIN_MODULE=BNB \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/$DB" \
  -e ACCOUNT_PASSWORD="$BNB_ACCOUNT_PASSWORD" \
  -e BNB_USERNAME="$BNB_USER" \
  -e BNB_PASSWORD="$BNB_PASS" \
  -e DB_MAX_OPEN_CONNS=12 \
  -e DB_MAX_IDLE_CONNS=4 \
  -e REQUESTS_TIMEOUT=8 \
  "$IMAGE" /app/chain-worker >/dev/null

docker run -d --name "$TRON_WORKER" --network "$NETWORK" \
  -e CHAIN_MODULE=TRON \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/$DB" \
  -e ACCOUNT_PASSWORD="$TRON_ACCOUNT_PASSWORD" \
  -e TRON_USERNAME="$TRON_USER" \
  -e TRON_PASSWORD="$TRON_PASS" \
  -e TRX_USERNAME="$TRON_USER" \
  -e TRX_PASSWORD="$TRON_PASS" \
  -e USDT_USERNAME="$TRON_USER" \
  -e USDT_PASSWORD="$TRON_PASS" \
  -e USDC_USERNAME="$TRON_USER" \
  -e USDC_PASSWORD="$TRON_PASS" \
  -e DB_MAX_OPEN_CONNS=12 \
  -e DB_MAX_IDLE_CONNS=4 \
  -e REQUESTS_TIMEOUT=8 \
  "$IMAGE" /app/chain-worker >/dev/null

docker run -d --name "$MAIN" --network "$NETWORK" \
  -e SHKEEPER_LISTEN=:5000 \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@$MARIADB_HOST:3306/$DB" \
  -e SECRET_KEY="$SECRET_KEY" \
  -e SUGGESTED_WALLET_APIKEY="$API_KEY" \
  -e SHKEEPER_CRYPTOS=BNB,BNB-USDT,TRX,USDT,USDC \
  -e SCHEDULER_ENABLED=false \
  -e BNB_API_SERVER_HOST="$BNB_WORKER" \
  -e BNB_SERVER_PORT=6000 \
  -e BNB_USERNAME="$BNB_USER" \
  -e BNB_PASSWORD="$BNB_PASS" \
  -e TRON_API_SERVER_HOST="$TRON_WORKER" \
  -e TRON_API_SERVER_PORT=6000 \
  -e TRX_USERNAME="$TRON_USER" \
  -e TRX_PASSWORD="$TRON_PASS" \
  -e USDT_USERNAME="$TRON_USER" \
  -e USDT_PASSWORD="$TRON_PASS" \
  -e USDC_USERNAME="$TRON_USER" \
  -e USDC_PASSWORD="$TRON_PASS" \
  -e DB_MAX_OPEN_CONNS=24 \
  -e DB_MAX_IDLE_CONNS=8 \
  -e BALANCE_QUERY_WORKERS=4 \
  -e REQUESTS_TIMEOUT=8 \
  "$IMAGE" /app/shkeeper >/dev/null

wait_ready "http://$BNB_WORKER:6000/readyz"
wait_ready "http://$TRON_WORKER:6000/readyz"
wait_ready "http://$MAIN:5000/readyz"

cat > "$PLAN" <<JSON
{
  "main_url": "http://$MAIN:5000",
  "worker_urls": {
    "bnb": "http://$BNB_WORKER:6000",
    "tron": "http://$TRON_WORKER:6000"
  },
  "api_key": "$API_KEY",
  "admin_username": "$ADMIN_USER",
  "admin_password": "$ADMIN_PASS",
  "admin_update_username": "$ADMIN_USER2",
  "admin_update_password": "$ADMIN_PASS2",
  "crypto": "BNB-USDT",
  "cryptos": ["BNB-USDT", "TRX"],
  "coverage_cryptos": ["BNB-USDT", "TRX"],
  "order_list_check": $ORDER_LIST_CHECK,
  "order_list_status": "UNPAID",
  "order_list_crypto": "$ORDER_CRYPTO",
  "order_list_limit": 20,
  "order_list_min_results": $ORDER_LIST_MIN,
  "order_list_requests": $REHEARSAL_ORDER_LIST_REQUESTS,
  "order_list_concurrency": $REHEARSAL_ORDER_LIST_CONCURRENCY,
  "order_list_max_latency_ms": $REHEARSAL_ORDER_LIST_MAX_LATENCY_MS,
  "order_status_matrix_check": true,
  "order_status_matrix_limit": 100,
  "order_status_matrix_pages": 20,
  "order_status_matrix_min": 2,
  "order_status_matrix_expect_statuses": ["PAID", "UNPAID"],
  "main_status_check": false,
  "worker_status_check": false,
  "mutating": true,
  "require_payment_coverage": true,
  "require_payout_coverage": false,
  "payment_checks": [
    {
      "name": "bnb-usdt-payment",
      "crypto": "BNB-USDT",
      "fiat": "USD",
      "amount": "1",
      "external_id_prefix": "go-rehearsal-bnb-usdt"
    },
    {
      "name": "trx-payment",
      "crypto": "TRX",
      "fiat": "USD",
      "amount": "1",
      "external_id_prefix": "go-rehearsal-trx"
    }
  ],
  "worker_address_checks": [
    {
      "name": "bnb-address",
      "worker": "bnb",
      "crypto": "BNB",
      "username": "$BNB_USER",
      "password": "$BNB_PASS"
    },
    {
      "name": "trx-address",
      "worker": "tron",
      "crypto": "TRX",
      "username": "$TRON_USER",
      "password": "$TRON_PASS"
    }
  ],
  "timeout_seconds": 45,
  "requests": $REHEARSAL_READY_REQUESTS,
  "concurrency": $REHEARSAL_READY_CONCURRENCY,
  "report_file": "/reports/deploy-check-report.json"
}
JSON
chmod 0444 "$PLAN"

docker run --rm --network "$NETWORK" \
  -v "$PLAN:/deploy-check.plan.json:ro" \
  -v "$WORK_DIR:/reports" \
  -e DEPLOY_CHECK_PLAN_FILE=/deploy-check.plan.json \
  "$IMAGE" /app/deploy-check

if [ ! -s "$DEPLOY_REPORT" ]; then
  echo "deploy-check did not write report $DEPLOY_REPORT" >&2
  exit 1
fi
chmod 0444 "$DEPLOY_REPORT"

docker stats --no-stream --format '{{json .}}' "$MAIN" "$BNB_WORKER" "$TRON_WORKER" > "$CONTAINER_STATS_REPORT"
chmod 0444 "$CONTAINER_STATS_REPORT"

docker run --rm \
  -v "$PLAN:/deploy-check.plan.json:ro" \
  -v "$DEPLOY_REPORT:/deploy-check-report.json:ro" \
  -v "$WORK_DIR:/reports" \
  -e CUTOVER_AUDIT_PLAN_FILE=/deploy-check.plan.json \
  -e CUTOVER_AUDIT_REPORT_FILES=/deploy-check-report.json \
  -e CUTOVER_AUDIT_IMPORT_REPORT_FILES=/reports/main-import-report.json \
  -e CUTOVER_AUDIT_PREFLIGHT_REPORT_FILES=/reports/cutover-preflight.json \
  -e CUTOVER_AUDIT_OUTPUT_FILE=/reports/cutover-audit.json \
  -e CUTOVER_AUDIT_REQUIRE_IMPORT_SYNC=true \
  -e CUTOVER_AUDIT_REQUIRE_PREFLIGHT=true \
  -e CUTOVER_AUDIT_COVERAGE_CRYPTOS=BNB-USDT,TRX \
  -e CUTOVER_AUDIT_WORKERS=bnb,tron \
  -e CUTOVER_AUDIT_REQUIRE_MAIN_STATUS=false \
  -e CUTOVER_AUDIT_REQUIRE_ORDER_STATUS_MATRIX=true \
  -e CUTOVER_AUDIT_REQUIRE_PAYOUT_ORDER=false \
  -e CUTOVER_AUDIT_REQUIRE_WORKER_ADDRESS=true \
  "$IMAGE" /app/cutover-audit

if [ ! -s "$AUDIT_REPORT" ]; then
  echo "cutover-audit did not write report $AUDIT_REPORT" >&2
  exit 1
fi
chmod 0444 "$AUDIT_REPORT"

docker run --rm \
  -v "$PLAN:/deploy-check.plan.json:ro" \
  -v "$DEPLOY_REPORT:/deploy-check-report.json:ro" \
  -v "$AUDIT_REPORT:/cutover-audit.json:ro" \
  -v "$WORK_DIR:/reports" \
  -e RELEASE_AUDIT_PLAN_FILE=/deploy-check.plan.json \
  -e RELEASE_AUDIT_DEPLOY_REPORT_FILES=/deploy-check-report.json \
  -e RELEASE_AUDIT_CUTOVER_AUDIT_FILE=/cutover-audit.json \
  -e RELEASE_AUDIT_OUTPUT_FILE=/reports/release-audit.json \
  -e RELEASE_AUDIT_REQUIRE_MUTATING=true \
  -e RELEASE_AUDIT_REQUIRE_IMPORT_SYNC=true \
  -e RELEASE_AUDIT_REQUIRE_PREFLIGHT=true \
  -e RELEASE_AUDIT_REQUIRE_ORDER_STATUS_MATRIX=true \
  -e RELEASE_AUDIT_REQUIRE_PAYMENT_COVERAGE=true \
  -e RELEASE_AUDIT_REQUIRE_PAYOUT_COVERAGE=false \
  -e RELEASE_AUDIT_REQUIRE_PAYMENT_ORDER=true \
  -e RELEASE_AUDIT_REQUIRE_PAYOUT_ORDER=false \
  -e RELEASE_AUDIT_REQUIRE_PAYOUT_TXID=false \
  -e RELEASE_AUDIT_REQUIRE_POST_CUTOVER=false \
  -e RELEASE_AUDIT_REQUIRE_WORKER_READY=true \
  -e RELEASE_AUDIT_REQUIRE_WORKER_ADDRESS=true \
  "$IMAGE" /app/release-audit

if [ ! -s "$RELEASE_AUDIT_REPORT" ]; then
  echo "release-audit did not write report $RELEASE_AUDIT_REPORT" >&2
  exit 1
fi

echo "--- rehearsal database evidence ---"
echo "--- main import report ---"
cat "$MAIN_IMPORT_REPORT"
echo "--- cutover preflight report ---"
cat "$PREFLIGHT_REPORT"
echo "--- release audit report ---"
cat "$RELEASE_AUDIT_REPORT"
echo "--- container stats report ---"
cat "$CONTAINER_STATS_REPORT"
echo "--- worker serverkey reports ---"
cat "$BNB_SERVERKEY_REPORT"
cat "$TRON_SERVERKEY_REPORT"
run_sql "$DB" -e "
SELECT 'wallets', COUNT(*) FROM wallet
UNION ALL SELECT 'invoices', COUNT(*) FROM invoice
UNION ALL SELECT 'chain_accounts', COUNT(*) FROM chain_account
UNION ALL SELECT 'bnb_accounts', COUNT(*) FROM chain_account WHERE module='BNB'
UNION ALL SELECT 'tron_accounts', COUNT(*) FROM chain_account WHERE module='TRON'
UNION ALL SELECT 'encrypted_accounts', COUNT(*) FROM chain_account WHERE private_key_hex LIKE 'v1:%';
"
echo "--- latest rehearsal invoices ---"
run_sql "$DB" -e "SELECT id, crypto, status, external_id, LEFT(addr, 16), amount_fiat, amount_crypto FROM invoice WHERE external_id LIKE 'go-rehearsal-%' ORDER BY id DESC LIMIT 5"

echo "rehearsal_ok id=$ID db=$DB order_crypto=$ORDER_CRYPTO"
