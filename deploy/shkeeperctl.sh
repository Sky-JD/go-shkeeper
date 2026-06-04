#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${SHKEEPER_ENV_FILE:-$ROOT_DIR/.env}"
PROJECT_NAME="${SHKEEPER_PROJECT_NAME:-${COMPOSE_PROJECT_NAME:-go-shkeeper}}"
COMPOSE_FILES_RAW="${SHKEEPER_COMPOSE_FILES:-${SHKEEPER_COMPOSE_FILE:-docker-compose.example.yml}}"
IMAGE="${GO_SHKEEPER_IMAGE:-go-shkeeper:local}"
REPORT_DIR="${REPORT_DIR:-$ROOT_DIR/deploy-reports}"
DOCKER_NETWORK="${SHKEEPER_DOCKER_NETWORK:-${PROJECT_NAME}_default}"
INIT_CRYPTOS="${SHKEEPER_INIT_CRYPTOS:-${SHKEEPER_CRYPTOS:-BTC}}"
DRY_RUN="${SHKEEPER_DRY_RUN:-0}"
SERVICE_LIST=""

export GO_SHKEEPER_IMAGE="$IMAGE"
export COMPOSE_PROJECT_NAME="$PROJECT_NAME"

log() {
  printf '[shkeeperctl] %s\n' "$*"
}

die() {
  printf '[shkeeperctl] error: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage:
  bash deploy/shkeeperctl.sh init
  bash deploy/shkeeperctl.sh install
  bash deploy/shkeeperctl.sh upgrade
  bash deploy/shkeeperctl.sh uninstall
  bash deploy/shkeeperctl.sh start|stop|restart|status|logs|build
  bash deploy/shkeeperctl.sh show-cryptos
  bash deploy/shkeeperctl.sh list-cryptos
  bash deploy/shkeeperctl.sh set-cryptos BTC,TRX,USDT
  bash deploy/shkeeperctl.sh enable-crypto TRX USDT
  bash deploy/shkeeperctl.sh disable-crypto BTC-LIGHTNING
  bash deploy/shkeeperctl.sh admin-password admin /secure/admin_password
  bash deploy/shkeeperctl.sh worker-serverkey BNB,BNB-USDT worker /secure/worker_password
  bash deploy/shkeeperctl.sh debug
  bash deploy/shkeeperctl.sh readiness

Environment:
  SHKEEPER_ENV_FILE         Default: ./.env
  SHKEEPER_COMPOSE_FILE     Default: docker-compose.example.yml
  SHKEEPER_COMPOSE_FILES    Comma-separated compose files; overrides SHKEEPER_COMPOSE_FILE
  SHKEEPER_PROJECT_NAME     Default: go-shkeeper
  GO_SHKEEPER_IMAGE         Default: go-shkeeper:local
  SHKEEPER_INIT_CRYPTOS     Default: BTC
  SHKEEPER_DRY_RUN=1        Print Docker/Git actions without running them
  SHKEEPER_SKIP_GIT_PULL=1  Skip git pull during upgrade
  CONFIRM_UNINSTALL=GO_SHKEEPER is required for uninstall
  PURGE_DATA=1 CONFIRM_PURGE=DELETE_GO_SHKEEPER_DATA also removes compose volumes
EOF
}

trim() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

normalize_crypto() {
  local value
  value="$(printf '%s' "$1" | tr '[:lower:]_' '[:upper:]-' | tr -d '[:space:]')"
  printf '%s' "$value"
}

normalize_crypto_list() {
  local raw part crypto
  local -a out=()
  local -A seen=()
  for raw in "$@"; do
    raw="${raw//;/,}"
    IFS=',' read -r -a parts <<<"$raw"
    for part in "${parts[@]}"; do
      crypto="$(normalize_crypto "$part")"
      if [ -n "$crypto" ] && [ -z "${seen[$crypto]+x}" ]; then
        seen[$crypto]=1
        out+=("$crypto")
      fi
    done
  done
  (IFS=,; printf '%s' "${out[*]}")
}

split_crypto_lines() {
  local raw part crypto
  for raw in "$@"; do
    raw="${raw//;/,}"
    IFS=',' read -r -a parts <<<"$raw"
    for part in "${parts[@]}"; do
      crypto="$(normalize_crypto "$part")"
      if [ -n "$crypto" ]; then
        printf '%s\n' "$crypto"
      fi
    done
  done
}

worker_for_crypto() {
  case "$(normalize_crypto "$1")" in
    BTC) printf '%s\n' "btc-worker" ;;
    BTC-LIGHTNING) printf '%s\n' "btc-lightning-worker" ;;
    LTC) printf '%s\n' "ltc-worker" ;;
    DOGE) printf '%s\n' "doge-worker" ;;
    FIRO|FIRO-SPARK) printf '%s\n' "firo-worker" ;;
    ETH|ETH-USDT|ETH-USDC|ETH-PYUSD) printf '%s\n' "eth-worker" ;;
    TRX|USDT|USDC) printf '%s\n' "tron-worker" ;;
    BNB|BNB-USDT|BNB-USDC) printf '%s\n' "bnb-worker" ;;
    MATIC|POLYGON-USDT|POLYGON-USDC) printf '%s\n' "polygon-worker" ;;
    AVAX|AVALANCHE-USDT|AVALANCHE-USDC) printf '%s\n' "avalanche-worker" ;;
    SOL|SOLANA-USDT|SOLANA-USDC|SOLANA-PYUSD) printf '%s\n' "solana-worker" ;;
    XRP) printf '%s\n' "xrp-worker" ;;
    ARBETH|ARB-USDC|ARB-PYUSD|ARB-TOKEN) printf '%s\n' "arbitrum-worker" ;;
    OPETH|OP-USDT|OP-USDC|OP-TOKEN) printf '%s\n' "optimism-worker" ;;
    XMR) printf '%s\n' "xmr-worker" ;;
    *) return 1 ;;
  esac
}

wallet_env_for_crypto() {
  local crypto
  crypto="$(normalize_crypto "$1")"
  printf '%s_WALLET\n' "${crypto//-/_}"
}

all_known_cryptos() {
  cat <<'EOF'
BTC btc-worker
BTC-LIGHTNING btc-lightning-worker
LTC ltc-worker
DOGE doge-worker
FIRO firo-worker
FIRO-SPARK firo-worker
ETH eth-worker
ETH-USDT eth-worker
ETH-USDC eth-worker
ETH-PYUSD eth-worker
TRX tron-worker
USDT tron-worker
USDC tron-worker
BNB bnb-worker
BNB-USDT bnb-worker
BNB-USDC bnb-worker
MATIC polygon-worker
POLYGON-USDT polygon-worker
POLYGON-USDC polygon-worker
AVAX avalanche-worker
AVALANCHE-USDT avalanche-worker
AVALANCHE-USDC avalanche-worker
SOL solana-worker
SOLANA-USDT solana-worker
SOLANA-USDC solana-worker
SOLANA-PYUSD solana-worker
XRP xrp-worker
ARBETH arbitrum-worker
ARB-USDC arbitrum-worker
ARB-PYUSD arbitrum-worker
ARB-TOKEN arbitrum-worker
OPETH optimism-worker
OP-USDT optimism-worker
OP-USDC optimism-worker
OP-TOKEN optimism-worker
XMR xmr-worker
EOF
}

validate_cryptos() {
  local crypto
  for crypto in $(split_crypto_lines "$@"); do
    worker_for_crypto "$crypto" >/dev/null || die "unsupported crypto: $crypto"
  done
}

env_get() {
  local key="$1"
  local line value
  [ -f "$ENV_FILE" ] || return 1
  line="$(grep -E "^${key}=" "$ENV_FILE" | tail -n 1 || true)"
  [ -n "$line" ] || return 1
  value="${line#*=}"
  value="${value%\"}"
  value="${value#\"}"
  value="${value%\'}"
  value="${value#\'}"
  printf '%s' "$value"
}

env_set() {
  local key="$1"
  local value="$2"
  local tmp
  mkdir -p "$(dirname "$ENV_FILE")"
  tmp="$(mktemp "$ENV_FILE.tmp.XXXXXX")"
  if [ -f "$ENV_FILE" ]; then
    grep -v -E "^${key}=" "$ENV_FILE" >"$tmp" || true
  fi
  printf '%s=%s\n' "$key" "$value" >>"$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$ENV_FILE"
}

env_set_default() {
  local key="$1"
  local value="$2"
  local current
  current="$(env_get "$key" || true)"
  if [ -z "$current" ]; then
    env_set "$key" "$value"
  fi
}

rand_hex() {
  local bytes="${1:-32}"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$bytes"
    return
  fi
  dd if=/dev/urandom bs="$bytes" count=1 2>/dev/null | od -An -tx1 | tr -d ' \n'
}

compose_file_paths=()
compose_file_args=()
IFS=',' read -r -a raw_compose_files <<<"$COMPOSE_FILES_RAW"
for raw_file in "${raw_compose_files[@]}"; do
  raw_file="$(trim "$raw_file")"
  [ -n "$raw_file" ] || continue
  case "$raw_file" in
    /*) compose_file="$raw_file" ;;
    *) compose_file="$ROOT_DIR/$raw_file" ;;
  esac
  compose_file_paths+=("$compose_file")
  compose_file_args+=("-f" "$compose_file")
done

[ "${#compose_file_args[@]}" -gt 0 ] || die "no compose file configured"

compose() {
  if [ "$DRY_RUN" = "1" ]; then
    case "${1:-}" in
      up|down|stop|restart|build|pull|rm)
        printf '[shkeeperctl] dry-run: docker compose --env-file %q -p %q ' "$ENV_FILE" "$PROJECT_NAME"
        printf '%q ' "${compose_file_args[@]}" "$@"
        printf '\n'
        return 0
        ;;
    esac
  fi
  docker compose --env-file "$ENV_FILE" -p "$PROJECT_NAME" "${compose_file_args[@]}" "$@"
}

require_env_file() {
  [ -f "$ENV_FILE" ] || die "$ENV_FILE does not exist; run: bash deploy/shkeeperctl.sh init"
}

load_services() {
  require_env_file
  SERVICE_LIST="$(compose config --services)"
}

service_exists() {
  local service="$1"
  if [ -z "$SERVICE_LIST" ]; then
    load_services
  fi
  grep -Fxq "$service" <<<"$SERVICE_LIST"
}

main_service() {
  if service_exists shkeeper; then
    printf '%s\n' "shkeeper"
    return
  fi
  if service_exists go-shkeeper; then
    printf '%s\n' "go-shkeeper"
    return
  fi
  die "compose file does not define shkeeper or go-shkeeper service"
}

infra_services() {
  local service
  for service in mariadb redis; do
    if service_exists "$service"; then
      printf '%s\n' "$service"
    fi
  done
}

services_for_cryptos() {
  local crypto worker
  local -A seen=()
  for crypto in $(split_crypto_lines "$@"); do
    worker="$(worker_for_crypto "$crypto" || true)"
    if [ -n "$worker" ] && service_exists "$worker" && [ -z "${seen[$worker]+x}" ]; then
      seen[$worker]=1
      printf '%s\n' "$worker"
    fi
  done
}

all_worker_services() {
  all_known_cryptos | awk '{print $2}' | sort -u | while read -r service; do
    if service_exists "$service"; then
      printf '%s\n' "$service"
    fi
  done
}

default_compose_cryptos() {
  local file line value
  for file in "${compose_file_paths[@]}"; do
    [ -f "$file" ] || continue
    line="$(grep -m 1 'SHKEEPER_CRYPTOS:.*SHKEEPER_CRYPTOS:-' "$file" || true)"
    if [ -n "$line" ]; then
      value="$(printf '%s\n' "$line" | sed -n 's/.*SHKEEPER_CRYPTOS:-\([^}]*\)}.*/\1/p')"
      if [ -n "$value" ]; then
        normalize_crypto_list "$value"
        return
      fi
    fi
  done
  normalize_crypto_list "$INIT_CRYPTOS"
}

current_cryptos() {
  local value
  value="$(env_get SHKEEPER_CRYPTOS || true)"
  if [ -n "$value" ]; then
    normalize_crypto_list "$value"
    return
  fi
  default_compose_cryptos
}

write_cryptos() {
  local cryptos="$1"
  [ -n "$cryptos" ] || die "crypto list cannot be empty; use stop or uninstall instead"
  validate_cryptos "$cryptos"
  env_set SHKEEPER_CRYPTOS "$cryptos"
}

set_wallet_envs() {
  local state="$1"
  local crypto key
  shift
  for crypto in $(split_crypto_lines "$@"); do
    key="$(wallet_env_for_crypto "$crypto")"
    env_set "$key" "$state"
  done
}

remove_cryptos_from_list() {
  local current="$1"
  shift
  local -A remove=()
  local -A seen=()
  local -a out=()
  local crypto
  for crypto in $(split_crypto_lines "$@"); do
    remove[$crypto]=1
  done
  for crypto in $(split_crypto_lines "$current"); do
    if [ -z "${remove[$crypto]+x}" ] && [ -z "${seen[$crypto]+x}" ]; then
      seen[$crypto]=1
      out+=("$crypto")
    fi
  done
  (IFS=,; printf '%s' "${out[*]}")
}

list_to_args() {
  local -n source_array="$1"
  local item
  for item in "${source_array[@]}"; do
    printf '%s\n' "$item"
  done
}

init_env() {
  mkdir -p "$(dirname "$ENV_FILE")"
  if [ ! -f "$ENV_FILE" ]; then
    umask 077
    : >"$ENV_FILE"
    log "created $ENV_FILE"
  fi

  local root_password mariadb_password cryptos
  root_password="$(env_get MYSQL_ROOT_PASSWORD || true)"
  if [ -z "$root_password" ]; then
    root_password="$(rand_hex 24)"
  fi
  mariadb_password="$(env_get MARIADB_PASSWORD || true)"
  if [ -z "$mariadb_password" ]; then
    mariadb_password="$(rand_hex 24)"
  fi

  env_set_default MYSQL_ROOT_PASSWORD "$root_password"
  env_set_default MARIADB_ROOT_PASSWORD "$root_password"
  env_set_default MARIADB_PASSWORD "$mariadb_password"
  env_set_default SECRET_KEY "$(rand_hex 32)"
  env_set_default SHKEEPER_BACKEND_KEY "$(rand_hex 32)"
  env_set_default SUGGESTED_WALLET_APIKEY "$(rand_hex 24)"
  env_set_default SHKEEPER_DB_MAX_OPEN_CONNS "48"
  env_set_default SHKEEPER_DB_MAX_IDLE_CONNS "16"
  env_set_default WORKER_DB_MAX_OPEN_CONNS "16"
  env_set_default WORKER_DB_MAX_IDLE_CONNS "8"
  env_set_default MARIADB_DATABASE_URL "mariadb://root:$root_password@mariadb:3306/shkeeper"

  env_set_default BTC_USERNAME "worker"
  env_set_default BTC_PASSWORD "$(rand_hex 18)"
  env_set_default BTC_LIGHTNING_USERNAME "worker"
  env_set_default BTC_LIGHTNING_PASSWORD "$(rand_hex 18)"
  env_set_default LTC_USERNAME "worker"
  env_set_default LTC_PASSWORD "$(rand_hex 18)"
  env_set_default DOGE_USERNAME "worker"
  env_set_default DOGE_PASSWORD "$(rand_hex 18)"
  env_set_default FIRO_USERNAME "worker"
  env_set_default FIRO_PASSWORD "$(rand_hex 18)"
  env_set_default TRON_USERNAME "worker"
  env_set_default TRON_PASSWORD "$(rand_hex 18)"
  env_set_default BNB_USERNAME "worker"
  env_set_default BNB_PASSWORD "$(rand_hex 18)"
  env_set_default ETH_USERNAME "worker"
  env_set_default ETH_PASSWORD "$(rand_hex 18)"
  env_set_default POLYGON_USERNAME "worker"
  env_set_default POLYGON_PASSWORD "$(rand_hex 18)"
  env_set_default AVALANCHE_USERNAME "worker"
  env_set_default AVALANCHE_PASSWORD "$(rand_hex 18)"
  env_set_default ARB_USERNAME "worker"
  env_set_default ARB_PASSWORD "$(rand_hex 18)"
  env_set_default OP_USERNAME "worker"
  env_set_default OP_PASSWORD "$(rand_hex 18)"
  env_set_default SOLANA_USERNAME "worker"
  env_set_default SOLANA_PASSWORD "$(rand_hex 18)"
  env_set_default XRP_USERNAME "worker"
  env_set_default XRP_PASSWORD "$(rand_hex 18)"
  env_set_default MONERO_USERNAME "worker"
  env_set_default MONERO_PASSWORD "$(rand_hex 18)"

  cryptos="$(env_get SHKEEPER_CRYPTOS || true)"
  if [ -z "$cryptos" ]; then
    cryptos="$(normalize_crypto_list "$INIT_CRYPTOS")"
    write_cryptos "$cryptos"
  else
    cryptos="$(normalize_crypto_list "$cryptos")"
    write_cryptos "$cryptos"
  fi
  set_wallet_envs enabled "$cryptos"
  log "enabled cryptos: $cryptos"
}

compose_build_selected() {
  require_env_file
  local cryptos main
  local -a workers=()
  cryptos="$(current_cryptos)"
  main="$(main_service)"
  mapfile -t workers < <(services_for_cryptos "$cryptos")
  compose build "$main" "${workers[@]}"
}

start_selected() {
  require_env_file
  local cryptos main
  local -a infra=()
  local -a workers=()
  cryptos="$(current_cryptos)"
  main="$(main_service)"
  mapfile -t infra < <(infra_services)
  mapfile -t workers < <(services_for_cryptos "$cryptos")

  if [ "${#infra[@]}" -gt 0 ]; then
    compose up -d "${infra[@]}"
  fi
  if [ "${#workers[@]}" -gt 0 ]; then
    compose up -d --no-deps "${workers[@]}"
  fi
  compose up -d --no-deps "$main"
  log "started $main with cryptos: $cryptos"
}

restart_main() {
  require_env_file
  local main
  main="$(main_service)"
  compose up -d --no-deps --force-recreate "$main"
}

stop_unused_workers() {
  local cryptos="$1"
  local -A needed=()
  local worker
  for worker in $(services_for_cryptos "$cryptos"); do
    needed[$worker]=1
  done
  for worker in $(all_worker_services); do
    if [ -z "${needed[$worker]+x}" ]; then
      compose stop "$worker" >/dev/null 2>&1 || true
    fi
  done
}

install_stack() {
  init_env
  compose_build_selected
  start_selected
}

upgrade_stack() {
  require_env_file
  if [ -d "$ROOT_DIR/.git" ] && [ "${SHKEEPER_SKIP_GIT_PULL:-0}" != "1" ]; then
    log "pulling latest source"
    if [ "$DRY_RUN" = "1" ]; then
      printf '[shkeeperctl] dry-run: git -C %q pull --ff-only\n' "$ROOT_DIR"
    else
      git -C "$ROOT_DIR" pull --ff-only
    fi
  fi
  compose_build_selected
  start_selected
}

uninstall_stack() {
  require_env_file
  [ "${CONFIRM_UNINSTALL:-}" = "GO_SHKEEPER" ] || die "set CONFIRM_UNINSTALL=GO_SHKEEPER to uninstall"
  local -a args=("down" "--remove-orphans")
  if [ "${PURGE_DATA:-0}" = "1" ]; then
    [ "${CONFIRM_PURGE:-}" = "DELETE_GO_SHKEEPER_DATA" ] || die "set CONFIRM_PURGE=DELETE_GO_SHKEEPER_DATA to remove volumes"
    args+=("-v")
  fi
  compose "${args[@]}"
}

show_cryptos() {
  printf '%s\n' "$(current_cryptos)"
}

list_cryptos() {
  all_known_cryptos
}

set_cryptos() {
  [ "$#" -gt 0 ] || die "set-cryptos requires at least one crypto"
  init_env
  local cryptos
  cryptos="$(normalize_crypto_list "$@")"
  validate_cryptos "$cryptos"
  write_cryptos "$cryptos"
  set_wallet_envs enabled "$cryptos"
  stop_unused_workers "$cryptos"
  start_selected
  restart_main
}

enable_crypto() {
  [ "$#" -gt 0 ] || die "enable-crypto requires at least one crypto"
  init_env
  validate_cryptos "$@"
  local current next
  current="$(current_cryptos)"
  next="$(normalize_crypto_list "$current" "$@")"
  write_cryptos "$next"
  set_wallet_envs enabled "$@"
  start_selected
  restart_main
}

disable_crypto() {
  [ "$#" -gt 0 ] || die "disable-crypto requires at least one crypto"
  init_env
  validate_cryptos "$@"
  local current next
  current="$(current_cryptos)"
  next="$(remove_cryptos_from_list "$current" "$@")"
  [ -n "$next" ] || die "crypto list cannot be empty; use stop or uninstall instead"
  write_cryptos "$next"
  set_wallet_envs disabled "$@"
  stop_unused_workers "$next"
  restart_main
  log "enabled cryptos: $next"
}

mariadb_url() {
  local value
  value="$(env_get MARIADB_DATABASE_URL || true)"
  [ -n "$value" ] || die "set MARIADB_DATABASE_URL in $ENV_FILE"
  printf '%s' "$value"
}

admin_password() {
  [ "$#" -eq 2 ] || die "admin-password requires: <username> <password-file>"
  require_env_file
  local username="$1"
  local password_file="$2"
  [ -f "$password_file" ] || die "password file does not exist: $password_file"
  mkdir -p "$REPORT_DIR"
  if [ "$DRY_RUN" = "1" ]; then
    printf '[shkeeperctl] dry-run: docker run --rm --network %q -e MARIADB_DATABASE_URL=*** -e ADMIN_USERNAME=%q -e ADMIN_PASSWORD_FILE=/run/secrets/admin_password -v %q:/run/secrets/admin_password:ro -v %q:/deploy-reports %q /app/admin-account\n' "$DOCKER_NETWORK" "$username" "$password_file" "$REPORT_DIR" "$IMAGE"
    return 0
  fi
  docker run --rm --network "$DOCKER_NETWORK" \
    -e MARIADB_DATABASE_URL="$(mariadb_url)" \
    -e ADMIN_USERNAME="$username" \
    -e ADMIN_PASSWORD_FILE=/run/secrets/admin_password \
    -e ADMIN_ACCOUNT_REPORT_FILE=/deploy-reports/go-shkeeper-admin-account.json \
    -v "$password_file:/run/secrets/admin_password:ro" \
    -v "$REPORT_DIR:/deploy-reports" \
    "$IMAGE" /app/admin-account
}

worker_serverkey() {
  [ "$#" -eq 3 ] || die "worker-serverkey requires: <cryptos> <username> <password-file>"
  require_env_file
  local cryptos="$1"
  local username="$2"
  local password_file="$3"
  validate_cryptos "$cryptos"
  [ -f "$password_file" ] || die "password file does not exist: $password_file"
  mkdir -p "$REPORT_DIR"
  if [ "$DRY_RUN" = "1" ]; then
    printf '[shkeeperctl] dry-run: docker run --rm --network %q -e MARIADB_DATABASE_URL=*** -e WORKER_SERVERKEY_CRYPTOS=%q -e WORKER_USERNAME=%q -e WORKER_PASSWORD_FILE=/run/secrets/worker_password -v %q:/run/secrets/worker_password:ro -v %q:/deploy-reports %q /app/worker-serverkey\n' "$DOCKER_NETWORK" "$(normalize_crypto_list "$cryptos")" "$username" "$password_file" "$REPORT_DIR" "$IMAGE"
    return 0
  fi
  docker run --rm --network "$DOCKER_NETWORK" \
    -e MARIADB_DATABASE_URL="$(mariadb_url)" \
    -e WORKER_SERVERKEY_CRYPTOS="$(normalize_crypto_list "$cryptos")" \
    -e WORKER_USERNAME="$username" \
    -e WORKER_PASSWORD_FILE=/run/secrets/worker_password \
    -e WORKER_SERVERKEY_REPORT_FILE=/deploy-reports/go-shkeeper-worker-serverkey.json \
    -v "$password_file:/run/secrets/worker_password:ro" \
    -v "$REPORT_DIR:/deploy-reports" \
    "$IMAGE" /app/worker-serverkey
}

run_debug() {
  if [ "$DRY_RUN" = "1" ]; then
    printf '[shkeeperctl] dry-run: GO_SHKEEPER_IMAGE=%q bash %q\n' "$IMAGE" "$ROOT_DIR/deploy/hk-docker-debug.sh"
    return 0
  fi
  GO_SHKEEPER_IMAGE="$IMAGE" bash "$ROOT_DIR/deploy/hk-docker-debug.sh"
}

run_readiness() {
  if [ "$DRY_RUN" = "1" ]; then
    printf '[shkeeperctl] dry-run: GO_SHKEEPER_IMAGE=%q bash %q\n' "$IMAGE" "$ROOT_DIR/deploy/hk-16-16-final-readiness.sh"
    return 0
  fi
  GO_SHKEEPER_IMAGE="$IMAGE" bash "$ROOT_DIR/deploy/hk-16-16-final-readiness.sh"
}

cmd="${1:-help}"
if [ "$#" -gt 0 ]; then
  shift
fi

case "$cmd" in
  help|-h|--help) usage ;;
  init) init_env ;;
  install) install_stack ;;
  upgrade) upgrade_stack ;;
  uninstall) uninstall_stack ;;
  start) start_selected ;;
  stop) require_env_file; compose stop "$@" ;;
  restart) require_env_file; start_selected; restart_main ;;
  status|ps) require_env_file; compose ps ;;
  logs) require_env_file; compose logs -f "$@" ;;
  build) compose_build_selected ;;
  show-cryptos) show_cryptos ;;
  list-cryptos) list_cryptos ;;
  set-cryptos) set_cryptos "$@" ;;
  enable-crypto) enable_crypto "$@" ;;
  disable-crypto) disable_crypto "$@" ;;
  admin-password) admin_password "$@" ;;
  worker-serverkey) worker_serverkey "$@" ;;
  debug) run_debug ;;
  readiness) run_readiness ;;
  *) usage; die "unknown command: $cmd" ;;
esac
