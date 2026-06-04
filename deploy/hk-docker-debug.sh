#!/usr/bin/env bash
set -euo pipefail

# Isolated Docker build/debug smoke for hk-16-16.
# It does not stop or replace production containers.

ID="${DEBUG_ID:-$(date +%Y%m%d%H%M%S)}"
IMAGE="${GO_SHKEEPER_IMAGE:-go-shkeeper:debug-$ID}"
NETWORK="${DEBUG_NETWORK:-go-shkeeper-debug-$ID}"
DB="${DEBUG_DB_CONTAINER:-go-shkeeper-debug-db-$ID}"
MAIN="${DEBUG_MAIN_CONTAINER:-go-shkeeper-debug-main-$ID}"
REPORT_DIR="${REPORT_DIR:-/tmp/go-shkeeper-debug-$ID}"
KEEP_DEBUG_CONTAINERS="${KEEP_DEBUG_CONTAINERS:-0}"
MARIADB_IMAGE="${MARIADB_IMAGE:-mariadb:11.4}"
MYSQL_ROOT_PASSWORD="${MYSQL_ROOT_PASSWORD:-debug-root-pass}"
MYSQL_DATABASE="${MYSQL_DATABASE:-go_shkeeper_debug}"
MYSQL_USER="${MYSQL_USER:-shkeeper}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-shkeeper}"
MARIADB_DATABASE_URL="mariadb://$MYSQL_USER:$MYSQL_PASSWORD@$DB:3306/$MYSQL_DATABASE"

mkdir -p "$REPORT_DIR"
chmod 0777 "$REPORT_DIR"

cleanup() {
  set +e
  docker logs "$MAIN" >"$REPORT_DIR/main.log" 2>&1 || true
  docker logs "$DB" >"$REPORT_DIR/mariadb.log" 2>&1 || true
  if [ "$KEEP_DEBUG_CONTAINERS" != "1" ]; then
    docker rm -f "$MAIN" "$DB" >/dev/null 2>&1 || true
    docker network rm "$NETWORK" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

cd "$(dirname "$0")/.."

docker build -t "$IMAGE" .
docker run --rm "$IMAGE" /app/runtime-audit >"$REPORT_DIR/runtime-audit.json"
grep -q '"status": "ok"' "$REPORT_DIR/runtime-audit.json"

docker network create "$NETWORK" >/dev/null
docker run -d --name "$DB" --network "$NETWORK" \
  -e MARIADB_ROOT_PASSWORD="$MYSQL_ROOT_PASSWORD" \
  -e MARIADB_DATABASE="$MYSQL_DATABASE" \
  -e MARIADB_USER="$MYSQL_USER" \
  -e MARIADB_PASSWORD="$MYSQL_PASSWORD" \
  "$MARIADB_IMAGE" >/dev/null

for _ in $(seq 1 60); do
  if docker exec "$DB" mariadb-admin ping -h127.0.0.1 -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" --silent >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
docker exec "$DB" mariadb-admin ping -h127.0.0.1 -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" --silent

docker run -d --name "$MAIN" --network "$NETWORK" \
  -e MARIADB_DATABASE_URL="$MARIADB_DATABASE_URL" \
  -e SECRET_KEY="debug-secret-key" \
  -e SUGGESTED_WALLET_APIKEY="debug-api-key" \
  -e SCHEDULER_ENABLED=false \
  -e SHKEEPER_CRYPTOS=BTC \
  "$IMAGE" >/dev/null

for _ in $(seq 1 60); do
  if docker exec "$MAIN" sh -lc 'wget -qO- http://127.0.0.1:5000/readyz' >"$REPORT_DIR/readyz.json" 2>"$REPORT_DIR/readyz.err"; then
    break
  fi
  sleep 2
done
grep -q '"status":"ready"' "$REPORT_DIR/readyz.json"
docker exec "$MAIN" sh -lc 'wget -qO- http://127.0.0.1:5000/healthz' >"$REPORT_DIR/healthz.json"
grep -q '"status":"ok"' "$REPORT_DIR/healthz.json"

docker run --rm --network "$NETWORK" \
  -e MARIADB_DATABASE_URL="$MARIADB_DATABASE_URL" \
  -e CUTOVER_PREFLIGHT_OUTPUT_FILE=/deploy-reports/cutover-preflight.json \
  -v "$REPORT_DIR:/deploy-reports" \
  "$IMAGE" /app/cutover-preflight
grep -q '"status": "pass"' "$REPORT_DIR/cutover-preflight.json"

docker run --rm --network "$NETWORK" \
  -e MARIADB_DATABASE_URL="$MARIADB_DATABASE_URL" \
  -e ADMIN_USERNAME=admin \
  -e ADMIN_PASSWORD=debug-admin-pass \
  -e ADMIN_ACCOUNT_REPORT_FILE=/deploy-reports/admin-account.json \
  -v "$REPORT_DIR:/deploy-reports" \
  "$IMAGE" /app/admin-account
grep -q '"status": "ok"' "$REPORT_DIR/admin-account.json"

docker exec "$DB" mariadb -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" -N -B "$MYSQL_DATABASE" \
  -e "SELECT COUNT(*) FROM wallet;" >"$REPORT_DIR/wallet-count.txt"

cat >"$REPORT_DIR/summary.txt" <<EOF
status=ok
image=$IMAGE
network=$NETWORK
main_container=$MAIN
mariadb_container=$DB
report_dir=$REPORT_DIR
keep_debug_containers=$KEEP_DEBUG_CONTAINERS
EOF

cat "$REPORT_DIR/summary.txt"
