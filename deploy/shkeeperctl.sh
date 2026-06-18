#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${SHKEEPER_ENV_FILE:-$ROOT_DIR/.env}"
PROJECT_NAME="${SHKEEPER_PROJECT_NAME:-${COMPOSE_PROJECT_NAME:-go-shkeeper}}"
COMPOSE_FILES_RAW="${SHKEEPER_COMPOSE_FILES:-${SHKEEPER_COMPOSE_FILE:-}}"
IMAGE="${GO_SHKEEPER_IMAGE:-go-shkeeper:local}"
REPORT_DIR="${REPORT_DIR:-$ROOT_DIR/deploy-reports}"
DOCKER_NETWORK="${SHKEEPER_DOCKER_NETWORK:-${PROJECT_NAME}_default}"
INIT_CRYPTOS="${SHKEEPER_INIT_CRYPTOS:-${SHKEEPER_CRYPTOS:-BTC}}"
DRY_RUN="${SHKEEPER_DRY_RUN:-0}"
INTERACTIVE="${SHKEEPER_INTERACTIVE:-auto}"
MANAGER_BIN="${SHKEEPERCTL_BIN:-/usr/local/bin/shkeeperctl}"
SECRET_DIR="${SHKEEPER_SECRET_DIR:-$ROOT_DIR/secrets}"
UTILITY_DOCKER_USER="${SHKEEPER_UTILITY_DOCKER_USER:-0:0}"
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

color_enabled() {
  [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]
}

color_text() {
  local code="$1"
  shift
  if color_enabled; then
    printf '\033[%sm%s\033[0m' "$code" "$*"
  else
    printf '%s' "$*"
  fi
}

usage() {
  cat <<'EOF'
Usage:
  shkeeperctl
  bash deploy/shkeeperctl.sh panel
  bash deploy/install.sh
  bash deploy/shkeeperctl.sh install-manager
  bash deploy/shkeeperctl.sh init
  bash deploy/shkeeperctl.sh configure
  bash deploy/shkeeperctl.sh install
  bash deploy/shkeeperctl.sh upgrade
  bash deploy/shkeeperctl.sh pull-upgrade
  bash deploy/shkeeperctl.sh source-upgrade /tmp/go-shkeeper-src.tgz
  bash deploy/shkeeperctl.sh uninstall
  bash deploy/shkeeperctl.sh start|stop|restart|status|logs|build
  bash deploy/shkeeperctl.sh show-cryptos
  bash deploy/shkeeperctl.sh list-cryptos
  bash deploy/shkeeperctl.sh set-cryptos BTC,TRX,USDT
  bash deploy/shkeeperctl.sh enable-crypto TRX USDT
  bash deploy/shkeeperctl.sh disable-crypto BTC-LIGHTNING
  bash deploy/shkeeperctl.sh show-config
  bash deploy/shkeeperctl.sh set-api-key /secure/api_key
  bash deploy/shkeeperctl.sh set-secret-key /secure/secret_key
  bash deploy/shkeeperctl.sh set-backend-key /secure/backend_key
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
  SHKEEPERCTL_BIN           Default: /usr/local/bin/shkeeperctl
  SHKEEPER_SECRET_DIR       Default: ./secrets
  SHKEEPER_UTILITY_DOCKER_USER Default: 0:0 for one-shot admin CLIs
  SHKEEPER_INIT_CRYPTOS     Default: BTC
  SHKEEPER_HOST             Default: 127.0.0.1
  SHKEEPER_PORT             Default: 5000
  SHKEEPER_INTERACTIVE      auto, 1, or 0. Default: auto
  SHKEEPER_MANAGED_REDIS=1  Start/status the compose redis service. Default: off
  SHKEEPER_DRY_RUN=1        Print Docker/Git actions without running them
  SHKEEPER_SKIP_GIT_PULL=1  Skip git pull during upgrade
  SHKEEPER_SOURCE_ARCHIVE   Source archive used by source-upgrade
  SHKEEPER_KEEP_SOURCE_ARCHIVE=1 keeps source archive after source-upgrade
  SHKEEPER_VERIFY_FRONTEND=0 skips rebuilt frontend marker check
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

evm_network_for_crypto() {
  case "$(normalize_crypto "$1")" in
    BNB|BNB-USDT|BNB-USDC) printf '%s\n' "BNB" ;;
    ETH|ETH-USDT|ETH-USDC|ETH-PYUSD) printf '%s\n' "ETH" ;;
    MATIC|POLYGON-USDT|POLYGON-USDC) printf '%s\n' "POLYGON" ;;
    AVAX|AVALANCHE-USDT|AVALANCHE-USDC) printf '%s\n' "AVALANCHE" ;;
    ARBETH|ARB-USDC|ARB-PYUSD|ARB-TOKEN) printf '%s\n' "ARB" ;;
    OPETH|OP-USDT|OP-USDC|OP-TOKEN) printf '%s\n' "OP" ;;
    *) return 1 ;;
  esac
}

evm_networks_for_cryptos() {
  local crypto network
  local -A seen=()
  for crypto in $(split_crypto_lines "$@"); do
    network="$(evm_network_for_crypto "$crypto" || true)"
    if [ -n "$network" ] && [ -z "${seen[$network]+x}" ]; then
      seen[$network]=1
      printf '%s\n' "$network"
    fi
  done
}

evm_default_fullnode_url() {
  case "$1" in
    BNB) printf '%s\n' "https://bsc-rpc.publicnode.com" ;;
    ETH) printf '%s\n' "https://ethereum-rpc.publicnode.com" ;;
    POLYGON) printf '%s\n' "https://polygon-bor-rpc.publicnode.com" ;;
    AVALANCHE) printf '%s\n' "https://avalanche-c-chain-rpc.publicnode.com" ;;
    ARB) printf '%s\n' "https://arbitrum-one-rpc.publicnode.com" ;;
    OP) printf '%s\n' "https://optimism-rpc.publicnode.com" ;;
    *) return 1 ;;
  esac
}

evm_default_chain_id() {
  case "$1" in
    BNB) printf '%s\n' "56" ;;
    ETH) printf '%s\n' "1" ;;
    POLYGON) printf '%s\n' "137" ;;
    AVALANCHE) printf '%s\n' "43114" ;;
    ARB) printf '%s\n' "42161" ;;
    OP) printf '%s\n' "10" ;;
    *) return 1 ;;
  esac
}

evm_default_token_contract() {
  case "$(normalize_crypto "$1")" in
    BNB-USDT) printf '%s\n' "0x55d398326f99059fF775485246999027B3197955" ;;
    BNB-USDC) printf '%s\n' "0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d" ;;
    ETH-USDT) printf '%s\n' "0xdAC17F958D2ee523a2206206994597C13D831ec7" ;;
    ETH-USDC) printf '%s\n' "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48" ;;
    POLYGON-USDT) printf '%s\n' "0xc2132D05D31c914a87C6611C10748AEb04B58e8F" ;;
    *) return 1 ;;
  esac
}

evm_default_token_decimals() {
  case "$(normalize_crypto "$1")" in
    BNB-USDT|BNB-USDC) printf '%s\n' "18" ;;
    ETH-USDT|ETH-USDC|ETH-PYUSD|POLYGON-USDT|POLYGON-USDC|AVALANCHE-USDT|AVALANCHE-USDC|ARB-USDC|ARB-PYUSD|OP-USDT|OP-USDC) printf '%s\n' "6" ;;
    *) return 1 ;;
  esac
}

evm_token_contract_key() {
  local crypto
  crypto="$(normalize_crypto "$1")"
  case "$crypto" in
    *-USDT|*-USDC|*-PYUSD|ARB-TOKEN|OP-TOKEN)
      printf '%s_CONTRACT\n' "${crypto//-/_}"
      ;;
    *) return 1 ;;
  esac
}

crypto_catalog() {
  cat <<'EOF'
BTC BITCOIN btc-worker Bitcoin
BTC-LIGHTNING BITCOIN btc-lightning-worker Lightning
LTC LITECOIN ltc-worker Litecoin
DOGE DOGECOIN doge-worker Dogecoin
FIRO FIRO firo-worker Firo
FIRO-SPARK FIRO firo-worker Firo-Spark
ETH ETHEREUM eth-worker Ethereum
ETH-USDT ETHEREUM eth-worker ERC20-USDT
ETH-USDC ETHEREUM eth-worker ERC20-USDC
ETH-PYUSD ETHEREUM eth-worker ERC20-PYUSD
TRX TRON tron-worker Tron
USDT TRON tron-worker TRC20-USDT
USDC TRON tron-worker TRC20-USDC
BNB BSC bnb-worker BNB
BNB-USDT BSC bnb-worker BEP20-USDT
BNB-USDC BSC bnb-worker BEP20-USDC
MATIC POLYGON polygon-worker Polygon-MATIC
POLYGON-USDT POLYGON polygon-worker Polygon-USDT
POLYGON-USDC POLYGON polygon-worker Polygon-USDC
AVAX AVALANCHE avalanche-worker Avalanche-AVAX
AVALANCHE-USDT AVALANCHE avalanche-worker Avalanche-USDT
AVALANCHE-USDC AVALANCHE avalanche-worker Avalanche-USDC
SOL SOLANA solana-worker Solana
SOLANA-USDT SOLANA solana-worker SPL-USDT
SOLANA-USDC SOLANA solana-worker SPL-USDC
SOLANA-PYUSD SOLANA solana-worker SPL-PYUSD
XRP XRP xrp-worker XRP
ARBETH ARBITRUM arbitrum-worker Arbitrum-ETH
ARB-USDC ARBITRUM arbitrum-worker Arbitrum-USDC
ARB-PYUSD ARBITRUM arbitrum-worker Arbitrum-PYUSD
ARB-TOKEN ARBITRUM arbitrum-worker Arbitrum-token
OPETH OPTIMISM optimism-worker Optimism-ETH
OP-USDT OPTIMISM optimism-worker Optimism-USDT
OP-USDC OPTIMISM optimism-worker Optimism-USDC
OP-TOKEN OPTIMISM optimism-worker Optimism-token
XMR XMR xmr-worker Monero
EOF
}

all_known_cryptos() {
  crypto_catalog | awk '{print $1, $3}'
}

validate_cryptos() {
  local crypto
  for crypto in $(split_crypto_lines "$@"); do
    worker_for_crypto "$crypto" >/dev/null || die "unsupported crypto: $crypto"
  done
}

is_interactive() {
  case "$INTERACTIVE" in
    1|true|yes|on) return 0 ;;
    0|false|no|off) return 1 ;;
  esac
  [ -t 0 ] && [ -t 1 ]
}

valid_port() {
  local value="$1"
  [[ "$value" =~ ^[0-9]+$ ]] || return 1
  [ "$value" -ge 1 ] && [ "$value" -le 65535 ]
}

prompt_value() {
  local label="$1"
  local default="$2"
  local value
  while true; do
    printf '%s [%s]: ' "$label" "$default" >&2
    IFS= read -r value
    value="$(trim "$value")"
    if [ -z "$value" ]; then
      value="$default"
    fi
    printf '%s' "$value"
    return
  done
}

prompt_port() {
  local default="$1"
  local value
  while true; do
    value="$(prompt_value "宿主机端口" "$default")"
    if valid_port "$value"; then
      printf '%s' "$value"
      return
    fi
    printf '\n端口必须是 1-65535。\n' >&2
  done
}

print_crypto_menu() {
  local index=1
  local crypto network worker label
  printf '\n可用币种/网络：\n'
  while read -r crypto network worker label; do
    printf '  %2d) %-16s network=%-10s worker=%-22s %s\n' "$index" "$crypto" "$network" "$worker" "$label"
    index=$((index + 1))
  done < <(crypto_catalog)
}

crypto_by_index() {
  local want="$1"
  local index=1
  local crypto network worker label
  while read -r crypto network worker label; do
    if [ "$index" -eq "$want" ]; then
      printf '%s' "$crypto"
      return 0
    fi
    index=$((index + 1))
  done < <(crypto_catalog)
  return 1
}

crypto_exists() {
  local want
  want="$(normalize_crypto "$1")"
  crypto_catalog | awk '{print $1}' | grep -Fxq "$want"
}

cryptos_for_network() {
  local want
  want="$(normalize_crypto "$1")"
  crypto_catalog | awk -v network="$want" '$2 == network {print $1}'
}

selection_to_cryptos() {
  local selection="$1"
  local default="$2"
  local token start end i crypto network_items
  local -a out=()
  selection="$(trim "$selection")"
  if [ -z "$selection" ]; then
    normalize_crypto_list "$default"
    return
  fi
  selection="${selection//;/,}"
  selection="${selection// /,}"
  IFS=',' read -r -a tokens <<<"$selection"
  for token in "${tokens[@]}"; do
    token="$(trim "$token")"
    [ -n "$token" ] || continue
    if [ "$(printf '%s' "$token" | tr '[:lower:]' '[:upper:]')" = "ALL" ]; then
      while read -r crypto _; do
        out+=("$crypto")
      done < <(all_known_cryptos)
      continue
    fi
    if [[ "$token" =~ ^[0-9]+-[0-9]+$ ]]; then
      start="${token%-*}"
      end="${token#*-}"
      [ "$start" -le "$end" ] || die "invalid crypto range: $token"
      for ((i = start; i <= end; i++)); do
        crypto="$(crypto_by_index "$i" || true)"
        [ -n "$crypto" ] || die "invalid crypto number: $i"
        out+=("$crypto")
      done
      continue
    fi
    if [[ "$token" =~ ^[0-9]+$ ]]; then
      crypto="$(crypto_by_index "$token" || true)"
      [ -n "$crypto" ] || die "invalid crypto number: $token"
      out+=("$crypto")
      continue
    fi
    crypto="$(normalize_crypto "$token")"
    if crypto_exists "$crypto"; then
      out+=("$crypto")
      continue
    fi
    mapfile -t network_items < <(cryptos_for_network "$crypto")
    if [ "${#network_items[@]}" -gt 0 ]; then
      out+=("${network_items[@]}")
      continue
    fi
    die "unsupported crypto or network: $token"
  done
  normalize_crypto_list "${out[@]}"
}

configure_wizard() {
  is_interactive || return 0
  printf '\nSHKeeper first-run configuration\n'
  printf '按回车使用默认值；币种可输入编号、范围、币种名、网络名或 all。\n'

  local root_password default_root_password mariadb_password default_mariadb_password
  local secret_key default_secret_key
  local host port default_cryptos selection cryptos
  default_root_password="$(root_password_value)"
  if [ -z "$default_root_password" ] || is_placeholder_env_value MYSQL_ROOT_PASSWORD "$default_root_password"; then
    default_root_password="$(rand_hex 24)"
  fi
  if root_password_needs_attention; then
    root_password="$(prompt_value "MariaDB root password" "$default_root_password")"
    printf '\n'
    env_set MYSQL_ROOT_PASSWORD "$root_password"
    env_set MARIADB_ROOT_PASSWORD "$root_password"
  fi

  default_mariadb_password="$(env_get MARIADB_PASSWORD || true)"
  if [ -z "$default_mariadb_password" ] || is_placeholder_env_value MARIADB_PASSWORD "$default_mariadb_password"; then
    default_mariadb_password="$(rand_hex 24)"
  fi
  if env_value_needs_attention MARIADB_PASSWORD; then
    mariadb_password="$(prompt_value "MariaDB app password" "$default_mariadb_password")"
    printf '\n'
    env_set MARIADB_PASSWORD "$mariadb_password"
  fi

  default_secret_key="$(env_get SECRET_KEY || true)"
  if [ -z "$default_secret_key" ] || is_placeholder_env_value SECRET_KEY "$default_secret_key"; then
    default_secret_key="$(rand_hex 32)"
  fi
  if env_value_needs_attention SECRET_KEY; then
    secret_key="$(prompt_value "Cookie SECRET_KEY" "$default_secret_key")"
    printf '\n'
    env_set SECRET_KEY "$secret_key"
  fi
  host="$(prompt_value "绑定 IP" "${SHKEEPER_HOST:-$(env_get SHKEEPER_HOST || printf '127.0.0.1')}")"
  printf '\n'
  port="$(prompt_port "${SHKEEPER_PORT:-$(env_get SHKEEPER_PORT || printf '5000')}")"
  printf '\n'
  default_cryptos="$(env_get SHKEEPER_CRYPTOS || true)"
  if [ -z "$default_cryptos" ]; then
    default_cryptos="$(normalize_crypto_list "$INIT_CRYPTOS")"
  fi
  print_crypto_menu
  printf '\n选择启用的币种/网络 [%s]: ' "$default_cryptos"
  IFS= read -r selection
  cryptos="$(selection_to_cryptos "$selection" "$default_cryptos")"
  prepare_cryptos_for_enable "$cryptos"

  env_set SHKEEPER_HOST "$host"
  env_set SHKEEPER_PORT "$port"
  write_cryptos "$cryptos"
  set_wallet_envs enabled "$cryptos"
  log "configured host=$host port=$port cryptos=$cryptos"
}

env_get() {
  local key="$1"
  local line value
  [ -f "$ENV_FILE" ] || return 1
  line="$(sed '1s/^\xEF\xBB\xBF//' "$ENV_FILE" | grep -E "^${key}=" | tail -n 1 || true)"
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

ensure_env_config_value() {
  local key="$1"
  local label="$2"
  local default="${3:-}"
  local required="${4:-1}"
  local current value
  current="$(env_get "$key" || true)"
  [ -n "$current" ] && return 0
  value="$default"
  if is_interactive; then
    value="$(prompt_value "$label" "$default")"
    printf '\n' >&2
  fi
  if [ -z "$value" ] && [ "$required" = "1" ]; then
    die "$key is required for selected cryptos"
  fi
  if [ -n "$value" ]; then
    env_set "$key" "$value"
    log "configured $key"
  fi
}

configure_evm_network_env() {
  local network="$1"
  local fullnode_key chain_id_key account_password_key default_password
  fullnode_key="${network}_FULLNODE_URL"
  chain_id_key="${network}_CHAIN_ID"
  account_password_key="${network}_ACCOUNT_PASSWORD"
  default_password="$(rand_hex 18)"
  ensure_env_config_value "$fullnode_key" "$network RPC URL" "$(evm_default_fullnode_url "$network")" 1
  ensure_env_config_value "$chain_id_key" "$network chain id" "$(evm_default_chain_id "$network")" 1
  ensure_env_config_value "$account_password_key" "$network account password" "$default_password" 1
}

configure_evm_crypto_env() {
  local crypto="$1"
  local network contract_key decimals_key default_contract default_decimals
  network="$(evm_network_for_crypto "$crypto" || true)"
  [ -n "$network" ] || return 0
  configure_evm_network_env "$network"

  contract_key="$(evm_token_contract_key "$crypto" || true)"
  [ -n "$contract_key" ] || return 0
  decimals_key="${contract_key%_CONTRACT}_DECIMALS"
  default_contract="$(evm_default_token_contract "$crypto" || true)"
  default_decimals="$(evm_default_token_decimals "$crypto" || true)"
  ensure_env_config_value "$contract_key" "$crypto contract address" "$default_contract" 1
  ensure_env_config_value "$decimals_key" "$crypto decimals" "$default_decimals" 1
}

configure_runtime_env_for_cryptos() {
  local crypto
  for crypto in $(split_crypto_lines "$@"); do
    case "$(normalize_crypto "$crypto")" in
      TRX|USDT|USDC)
        ensure_env_config_value "TRON_ACCOUNT_PASSWORD" "TRON account password" "$(rand_hex 18)" 1
        ;;
      SOL|SOLANA-USDT|SOLANA-USDC|SOLANA-PYUSD)
        ensure_env_config_value "SOLANA_ACCOUNT_PASSWORD" "SOLANA account password" "$(rand_hex 18)" 1
        ;;
    esac
    configure_evm_crypto_env "$crypto"
  done
}

rand_hex() {
  local bytes="${1:-32}"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$bytes"
    return
  fi
  dd if=/dev/urandom bs="$bytes" count=1 2>/dev/null | od -An -tx1 | tr -d ' \n'
}

read_secret_file() {
  local file="$1"
  [ -f "$file" ] || die "secret file does not exist: $file"
  tr -d '\r\n' <"$file"
}

prompt_secret_to_file() {
  local label="$1"
  local file="$2"
  local first second
  mkdir -p "$(dirname "$file")"
  while true; do
    printf '%s: ' "$label" >&2
    IFS= read -r first
    printf '再次输入%s: ' "$label" >&2
    IFS= read -r second
    if [ -z "$first" ]; then
      printf '不能为空。\n' >&2
      continue
    fi
    if [ "$first" != "$second" ]; then
      printf '两次输入不一致。\n' >&2
      continue
    fi
    umask 077
    printf '%s' "$first" >"$file"
    chmod 600 "$file"
    printf '%s' "$file"
    return
  done
}

set_env_secret_file() {
  [ "$#" -eq 2 ] || die "set-env-secret requires: <env-key> <secret-file>"
  local key="$1"
  local file="$2"
  local value
  value="$(read_secret_file "$file")"
  [ -n "$value" ] || die "$file is empty"
  env_set "$key" "$value"
  log "updated $key in $ENV_FILE"
}

redacted_state() {
  local key="$1"
  local value
  value="$(env_get "$key" || true)"
  if [ -n "$value" ]; then
    printf 'set'
  else
    printf 'missing'
  fi
}

compose_file_paths=()
compose_file_args=()

configured_compose_files_raw() {
  local raw
  raw="${SHKEEPER_COMPOSE_FILES:-}"
  [ -n "$raw" ] || raw="${SHKEEPER_COMPOSE_FILE:-}"
  [ -n "$raw" ] || raw="$(env_get SHKEEPER_COMPOSE_FILES || true)"
  [ -n "$raw" ] || raw="$(env_get SHKEEPER_COMPOSE_FILE || true)"
  [ -n "$raw" ] || raw="docker-compose.example.yml"
  printf '%s' "$raw"
}

reload_compose_files() {
  local raw_file compose_file
  local -a raw_compose_files=()
  COMPOSE_FILES_RAW="$(configured_compose_files_raw)"
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
  SERVICE_LIST=""
  [ "${#compose_file_args[@]}" -gt 0 ] || die "no compose file configured"
}

reload_compose_files

compose() {
  if [ "$DRY_RUN" = "1" ]; then
    case "${1:-}" in
      up|down|stop|restart|build|pull|rm|run)
        printf '[shkeeperctl] dry-run: docker compose --env-file %q -p %q ' "$ENV_FILE" "$PROJECT_NAME"
        printf '%q ' "${compose_file_args[@]}" "$@"
        printf '\n'
        return 0
        ;;
    esac
  fi
  docker compose --env-file "$ENV_FILE" -p "$PROJECT_NAME" "${compose_file_args[@]}" "$@"
}

docker_compose_available() {
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1
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
  for service in mariadb; do
    if service_exists "$service"; then
      printf '%s\n' "$service"
    fi
  done
  if [ "${SHKEEPER_MANAGED_REDIS:-0}" = "1" ] && service_exists redis; then
    printf '%s\n' "redis"
  fi
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

missing_worker_services_for_cryptos() {
  local crypto worker
  local -A seen=()
  load_services
  for crypto in $(split_crypto_lines "$@"); do
    worker="$(worker_for_crypto "$crypto" || true)"
    if [ -n "$worker" ] && ! service_exists "$worker" && [ -z "${seen[$crypto:$worker]+x}" ]; then
      seen[$crypto:$worker]=1
      printf '%s:%s\n' "$crypto" "$worker"
    fi
  done
}

compose_file_defines_worker() {
  local file="$1"
  local service="$2"
  [ -f "$file" ] || return 1
  grep -Eq "^[[:space:]]{2}${service}:" "$file"
}

ensure_compose_services_for_cryptos() {
  local cryptos="$1"
  local missing pair crypto worker modular_rel modular_file unresolved
  missing="$(missing_worker_services_for_cryptos "$cryptos" || true)"
  [ -z "$missing" ] && return 0

  modular_rel="deploy/modular.example.yml"
  modular_file="$ROOT_DIR/$modular_rel"
  unresolved=""
  while IFS=: read -r crypto worker; do
    [ -n "$crypto" ] || continue
    if ! compose_file_defines_worker "$modular_file" "$worker"; then
      unresolved="${unresolved}${crypto}:${worker}"$'\n'
    fi
  done <<<"$missing"

  if [ -z "$unresolved" ]; then
    export SHKEEPER_COMPOSE_FILE="$modular_rel"
    unset SHKEEPER_COMPOSE_FILES
    env_set SHKEEPER_COMPOSE_FILE "$modular_rel"
    reload_compose_files
    missing="$(missing_worker_services_for_cryptos "$cryptos" || true)"
    if [ -z "$missing" ]; then
      log "switched compose file to $modular_rel for selected workers"
      return 0
    fi
  fi

  printf '%s\n' "$missing" >&2
  die "current compose files do not define every required worker service"
}

prepare_cryptos_for_enable() {
  local cryptos="$1"
  configure_runtime_env_for_cryptos "$cryptos"
  ensure_compose_services_for_cryptos "$cryptos"
  require_worker_services_available "$cryptos"
}

require_worker_services_available() {
  local missing
  missing="$(missing_worker_services_for_cryptos "$@" || true)"
  [ -z "$missing" ] && return 0
  printf '%s\n' "$missing" >&2
  die "current compose files do not define every required worker service; use SHKEEPER_COMPOSE_FILE=deploy/modular.example.yml or choose only supported cryptos"
}

all_worker_services() {
  all_known_cryptos | awk '{print $2}' | sort -u | while read -r service; do
    if service_exists "$service"; then
      printf '%s\n' "$service"
    fi
  done
}

desired_services() {
  load_services
  {
    infra_services
    main_service
    services_for_cryptos "$(current_cryptos)"
  } | awk 'NF && !seen[$0]++'
}

stack_status_key() {
  if [ ! -f "$ENV_FILE" ]; then
    printf '%s' "unconfigured"
    return
  fi
  if ! docker_compose_available; then
    printf '%s' "docker-missing"
    return
  fi

  local desired running_services all_services service
  local desired_count=0
  local running_count=0
  desired="$(desired_services 2>/dev/null || true)"
  if [ -z "$desired" ]; then
    printf '%s' "unknown"
    return
  fi
  running_services="$(compose ps --services --filter status=running 2>/dev/null || true)"
  all_services="$(compose ps --all --services 2>/dev/null || true)"

  while IFS= read -r service; do
    [ -n "$service" ] || continue
    desired_count=$((desired_count + 1))
    if grep -Fxq "$service" <<<"$running_services"; then
      running_count=$((running_count + 1))
    fi
  done <<<"$desired"

  if [ "$desired_count" -gt 0 ] && [ "$running_count" -eq "$desired_count" ]; then
    printf '%s' "running"
    return
  fi
  if [ "$running_count" -gt 0 ]; then
    printf '%s' "partial"
    return
  fi
  if [ -n "$all_services" ]; then
    printf '%s' "stopped"
    return
  fi
  printf '%s' "not-installed"
}

stack_status_label() {
  case "$1" in
    running) printf '%s' "运行中" ;;
    partial) printf '%s' "部分运行" ;;
    stopped) printf '%s' "已停止" ;;
    not-installed) printf '%s' "未安装/未启动" ;;
    unconfigured) printf '%s' "未配置" ;;
    docker-missing) printf '%s' "Docker 不可用" ;;
    *) printf '%s' "未知" ;;
  esac
}

colored_stack_status() {
  local key label
  key="$(stack_status_key)"
  label="$(stack_status_label "$key")"
  case "$key" in
    running) color_text 32 "$label" ;;
    partial) color_text 33 "$label" ;;
    stopped|not-installed|unconfigured|docker-missing) color_text 31 "$label" ;;
    *) color_text 36 "$label" ;;
  esac
}

colored_redacted_state() {
  local key="$1"
  local state
  state="$(redacted_state "$key")"
  if [ "$state" = "set" ]; then
    color_text 32 "已设置"
  else
    color_text 31 "缺失"
  fi
}

print_status_summary() {
  local host="?"
  local port="?"
  local cryptos="?"
  local missing=""
  if [ -f "$ENV_FILE" ]; then
    host="$(env_get SHKEEPER_HOST || printf '?')"
    port="$(env_get SHKEEPER_PORT || printf '?')"
    cryptos="$(current_cryptos 2>/dev/null || printf '?')"
    missing="$(missing_worker_services_for_cryptos "$cryptos" 2>/dev/null || true)"
  fi
  printf 'SHKeeper 状态: %s\n' "$(colored_stack_status)"
  printf '配置: %s:%s  币种: %s\n' "$host" "$port" "$cryptos"
  if [ -n "$missing" ]; then
    printf '缺失 worker: %s\n' "$(color_text 31 "$(printf '%s' "$missing" | paste -sd ',' -)")"
  fi
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

needs_initial_config() {
  root_password_needs_attention && return 0
  env_value_needs_attention MARIADB_PASSWORD && return 0
  env_value_needs_attention SECRET_KEY && return 0
  env_value_needs_attention SHKEEPER_HOST && return 0
  env_value_needs_attention SHKEEPER_PORT && return 0
  env_value_needs_attention SHKEEPER_CRYPTOS && return 0
  return 1
}

placeholder_value_for_key() {
  case "$1" in
    MYSQL_ROOT_PASSWORD|MARIADB_ROOT_PASSWORD) printf '%s' "change-root-password" ;;
    MARIADB_PASSWORD) printf '%s' "change-db-password" ;;
    SECRET_KEY) printf '%s' "change-cookie-secret" ;;
    SHKEEPER_BACKEND_KEY) printf '%s' "change-backend-secret" ;;
    SUGGESTED_WALLET_APIKEY) printf '%s' "change-wallet-apikey" ;;
    *) return 1 ;;
  esac
}

is_placeholder_env_value() {
  local key="$1"
  local value
  local placeholder
  value="$(trim "${2:-}")"
  [ -n "$value" ] || return 1
  placeholder="$(placeholder_value_for_key "$key" || true)"
  [ -n "$placeholder" ] && [ "$value" = "$placeholder" ]
}

env_value_needs_attention() {
  local key="$1"
  local current
  current="$(env_get "$key" || true)"
  [ -z "$current" ] && return 0
  is_placeholder_env_value "$key" "$current"
}

root_password_value() {
  local mysql_password mariadb_password
  mysql_password="$(env_get MYSQL_ROOT_PASSWORD || true)"
  mariadb_password="$(env_get MARIADB_ROOT_PASSWORD || true)"
  if [ -n "$mysql_password" ] && ! is_placeholder_env_value MYSQL_ROOT_PASSWORD "$mysql_password"; then
    printf '%s' "$mysql_password"
    return
  fi
  if [ -n "$mariadb_password" ] && ! is_placeholder_env_value MARIADB_ROOT_PASSWORD "$mariadb_password"; then
    printf '%s' "$mariadb_password"
    return
  fi
  printf '%s' ""
}

root_password_needs_attention() {
  local mysql_password mariadb_password
  mysql_password="$(env_get MYSQL_ROOT_PASSWORD || true)"
  mariadb_password="$(env_get MARIADB_ROOT_PASSWORD || true)"
  if [ -n "$mysql_password" ] && ! is_placeholder_env_value MYSQL_ROOT_PASSWORD "$mysql_password"; then
    return 1
  fi
  if [ -n "$mariadb_password" ] && ! is_placeholder_env_value MARIADB_ROOT_PASSWORD "$mariadb_password"; then
    return 1
  fi
  return 0
}

env_issue_label() {
  local key="$1"
  if env_value_needs_attention "$key"; then
    if [ -n "$(env_get "$key" || true)" ]; then
      printf '%s (placeholder)' "$key"
    else
      printf '%s (missing)' "$key"
    fi
  fi
  return 0
}

log_env_issues() {
  local issue
  if root_password_needs_attention; then
    if [ -n "$(root_password_value)" ]; then
      log "env requires setup: MYSQL_ROOT_PASSWORD/MARIADB_ROOT_PASSWORD (placeholder)"
    else
      log "env requires setup: MYSQL_ROOT_PASSWORD/MARIADB_ROOT_PASSWORD (missing)"
    fi
  fi
  for issue in \
    "$(env_issue_label MARIADB_PASSWORD)" \
    "$(env_issue_label SECRET_KEY)" \
    "$(env_issue_label SHKEEPER_HOST)" \
    "$(env_issue_label SHKEEPER_PORT)" \
    "$(env_issue_label SHKEEPER_CRYPTOS)"; do
    if [ -n "$issue" ]; then
      log "env requires setup: $issue"
    fi
  done
}

set_env_value_if_missing_or_placeholder() {
  local key="$1"
  local value="$2"
  if env_value_needs_attention "$key"; then
    env_set "$key" "$value"
    log "configured $key"
    return
  fi
  env_set_default "$key" "$value"
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
  local created_env=0
  mkdir -p "$(dirname "$ENV_FILE")"
  if [ ! -f "$ENV_FILE" ]; then
    umask 077
    : >"$ENV_FILE"
    created_env=1
    log "created $ENV_FILE"
  fi

  if { [ "$created_env" = "1" ] || [ "${SHKEEPER_CONFIGURE:-0}" = "1" ] || needs_initial_config; } && is_interactive; then
    configure_wizard
  fi

  local root_password mariadb_password cryptos
  if [ "$created_env" != "1" ] && needs_initial_config; then
    log_env_issues
  fi

  root_password="$(root_password_value)"
  if [ -z "$root_password" ]; then
    root_password="$(rand_hex 24)"
  fi
  mariadb_password="$(env_get MARIADB_PASSWORD || true)"
  if [ -z "$mariadb_password" ] || is_placeholder_env_value MARIADB_PASSWORD "$mariadb_password"; then
    mariadb_password="$(rand_hex 24)"
  fi

  set_env_value_if_missing_or_placeholder MYSQL_ROOT_PASSWORD "$root_password"
  set_env_value_if_missing_or_placeholder MARIADB_ROOT_PASSWORD "$root_password"
  set_env_value_if_missing_or_placeholder MARIADB_PASSWORD "$mariadb_password"
  set_env_value_if_missing_or_placeholder SECRET_KEY "$(rand_hex 32)"
  env_set_default SHKEEPER_BACKEND_KEY "$(rand_hex 32)"
  env_set_default SUGGESTED_WALLET_APIKEY "$(rand_hex 24)"
  env_set_default SHKEEPER_DB_MAX_OPEN_CONNS "48"
  env_set_default SHKEEPER_DB_MAX_IDLE_CONNS "16"
  env_set_default WORKER_DB_MAX_OPEN_CONNS "16"
  env_set_default WORKER_DB_MAX_IDLE_CONNS "8"
  env_set_default SHKEEPER_HOST "${SHKEEPER_HOST:-127.0.0.1}"
  env_set_default SHKEEPER_PORT "${SHKEEPER_PORT:-5000}"
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
  else
    cryptos="$(normalize_crypto_list "$cryptos")"
  fi
  prepare_cryptos_for_enable "$cryptos"
  write_cryptos "$cryptos"
  set_wallet_envs enabled "$cryptos"
  log "enabled cryptos: $cryptos"
}

configure_stack() {
  mkdir -p "$(dirname "$ENV_FILE")"
  if [ ! -f "$ENV_FILE" ]; then
    umask 077
    : >"$ENV_FILE"
    log "created $ENV_FILE"
  fi
  configure_wizard
  init_env
}

install_manager() {
  local mode="${1:-optional}"
  local bin="$MANAGER_BIN"
  local bindir
  bindir="$(dirname "$bin")"
  if [ "$DRY_RUN" = "1" ]; then
    printf '[shkeeperctl] dry-run: install manager wrapper at %q\n' "$bin"
    return 0
  fi
  if ! mkdir -p "$bindir" >/dev/null 2>&1; then
    [ "$mode" = "required" ] && die "cannot create $bindir; run as root or set SHKEEPERCTL_BIN"
    log "cannot create $bindir; skip manager shortcut"
    return 0
  fi
  if ! cat >"$bin" <<EOF
#!/usr/bin/env bash
cd "$ROOT_DIR"
exec bash "$ROOT_DIR/deploy/shkeeperctl.sh" "\$@"
EOF
  then
    [ "$mode" = "required" ] && die "cannot write $bin; run as root or set SHKEEPERCTL_BIN"
    log "cannot write $bin; skip manager shortcut"
    return 0
  fi
  chmod 0755 "$bin"
  log "installed manager shortcut: $bin"
}

compose_build_selected() {
  require_env_file
  local main
  main="$(main_service)"
  compose build "$main"
}

start_selected() {
  require_env_file
  local include_infra="${1:-with-infra}"
  local cryptos main
  local -a infra=()
  local -a up_args=("up" "-d" "--no-build")
  local -a workers=()
  cryptos="$(current_cryptos)"
  main="$(main_service)"
  if [ "$include_infra" = "with-infra" ]; then
    mapfile -t infra < <(infra_services)
  fi
  mapfile -t workers < <(services_for_cryptos "$cryptos")

  if [ "${#infra[@]}" -gt 0 ]; then
    compose up -d "${infra[@]}"
  fi
  if [ "${#workers[@]}" -gt 0 ]; then
    compose "${up_args[@]}" --no-deps "${workers[@]}"
  fi
  compose "${up_args[@]}" --no-deps "$main"
  log "started $main with cryptos: $cryptos"
}

restart_main() {
  require_env_file
  local no_build="${1:-}"
  local main
  local -a args=("up" "-d" "--no-deps" "--force-recreate")
  main="$(main_service)"
  [ "$no_build" = "no-build" ] && args+=("--no-build")
  compose "${args[@]}" "$main"
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

is_git_worktree() {
  git -C "$ROOT_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1
}

install_stack() {
  init_env
  if [ "${SHKEEPER_INSTALL_MANAGER:-1}" != "0" ]; then
    install_manager optional
  fi
  compose_build_selected
  start_selected
}

upgrade_stack() {
  require_env_file
  if is_git_worktree && [ "${SHKEEPER_SKIP_GIT_PULL:-0}" != "1" ]; then
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

apply_source_archive() {
  local archive="$1"
  [ -n "$archive" ] || die "源码归档路径不能为空"
  [ -f "$archive" ] || die "源码归档不存在: $archive"

  local backup_dir path
  backup_dir="/tmp/go-shkeeper-preserve-$(date +%Y%m%d%H%M%S)"
  if [ "$DRY_RUN" = "1" ]; then
    printf '[shkeeperctl] dry-run: preserve runtime files under %q and extract %q into %q\n' "$backup_dir" "$archive" "$ROOT_DIR"
    return 0
  fi

  mkdir -p "$ROOT_DIR" "$backup_dir"
  for path in .env docker-compose.example.yml secrets deploy-reports deploy/shkeeperctl.sh deploy/install.sh deploy/docker-debug.sh; do
    if [ -e "$ROOT_DIR/$path" ]; then
      mkdir -p "$backup_dir/$(dirname "$path")"
      cp -a "$ROOT_DIR/$path" "$backup_dir/$path"
    fi
  done
  if [ -d "$ROOT_DIR/deploy" ]; then
    while IFS= read -r path; do
      path="${path#$ROOT_DIR/}"
      mkdir -p "$backup_dir/$(dirname "$path")"
      cp -a "$ROOT_DIR/$path" "$backup_dir/$path"
    done < <(find "$ROOT_DIR/deploy" -maxdepth 1 -type f \( -name '*.local.yml' -o -name '*.modular.example.yml' -o -name 'hk-*.yml' \) 2>/dev/null || true)
  fi

  for path in cmd internal web deploy Dockerfile go.mod go.sum README.md .dockerignore .gitignore; do
    rm -rf "$ROOT_DIR/$path"
  done

  log "extracting source archive: $archive"
  tar -xzf "$archive" -C "$ROOT_DIR"

  for path in .env docker-compose.example.yml secrets deploy-reports deploy/shkeeperctl.sh deploy/install.sh deploy/docker-debug.sh; do
    if [ -e "$backup_dir/$path" ]; then
      rm -rf "$ROOT_DIR/$path"
      mkdir -p "$(dirname "$ROOT_DIR/$path")"
      cp -a "$backup_dir/$path" "$ROOT_DIR/$path"
    fi
  done
  if [ -d "$backup_dir/deploy" ]; then
    while IFS= read -r path; do
      path="${path#$backup_dir/}"
      mkdir -p "$(dirname "$ROOT_DIR/$path")"
      cp -a "$backup_dir/$path" "$ROOT_DIR/$path"
    done < <(find "$backup_dir/deploy" -maxdepth 1 -type f \( -name '*.local.yml' -o -name '*.modular.example.yml' -o -name 'hk-*.yml' \) 2>/dev/null || true)
  fi
  chmod +x "$ROOT_DIR"/deploy/*.sh >/dev/null 2>&1 || true
  log "source archive applied; preserved runtime backup: $backup_dir"
}

ready_url() {
  local port
  port="$(env_get SHKEEPER_PORT || true)"
  [ -n "$port" ] || port=5000
  printf 'http://127.0.0.1:%s/readyz' "$port"
}

wait_ready() {
  require_env_file
  local url body
  url="$(ready_url)"
  if [ "$DRY_RUN" = "1" ]; then
    printf '[shkeeperctl] dry-run: wait for %q\n' "$url"
    return 0
  fi
  log "waiting for $url"
  for _ in $(seq 1 90); do
    if command -v wget >/dev/null 2>&1; then
      body="$(wget -qO- "$url" 2>/dev/null || true)"
    else
      body="$(curl -fsS "$url" 2>/dev/null || true)"
    fi
    if printf '%s' "$body" | grep -q '"status":"ready"'; then
      log "readyz ok"
      return 0
    fi
    sleep 2
  done
  die "readyz did not become ready: $url"
}

verify_frontend_markers() {
  require_env_file
  [ "${SHKEEPER_VERIFY_FRONTEND:-1}" != "0" ] || {
    log "frontend marker verification skipped"
    return 0
  }
  if [ "$DRY_RUN" = "1" ]; then
    printf '[shkeeperctl] dry-run: verify frontend markers in main container\n'
    return 0
  fi
  local main container
  main="$(main_service)"
  container="$(compose ps -q "$main" | head -n 1)"
  [ -n "$container" ] || die "cannot find main container for $main"
  docker exec "$container" sh -lc "strings /app/shkeeper | grep -q '/api/v1/admin/wallets' && strings /app/shkeeper | grep -q '/api/v1/admin/rates' && strings /app/shkeeper | grep -q '/api/v1/admin/cryptos' && strings /app/shkeeper | grep -q '/api/v1/admin/wallet-import'"
  log "frontend markers found in $main"
}

source_upgrade_stack() {
  local archive
  archive="${1:-${SHKEEPER_SOURCE_ARCHIVE:-}}"
  apply_source_archive "$archive"
  SHKEEPER_SKIP_GIT_PULL=1 upgrade_stack
  wait_ready
  verify_frontend_markers
  if [ "${SHKEEPER_KEEP_SOURCE_ARCHIVE:-0}" != "1" ] && [ "$DRY_RUN" != "1" ]; then
    rm -f "$archive"
    log "removed source archive: $archive"
  fi
  log "source_upgrade_ok root_dir=$ROOT_DIR"
}

pull_upgrade_stack() {
  upgrade_stack
  wait_ready
  verify_frontend_markers
  log "pull_upgrade_ok root_dir=$ROOT_DIR"
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

show_config() {
  INTERACTIVE=0 init_env
  cat <<EOF
status=$(stack_status_label "$(stack_status_key)")
root_dir=$ROOT_DIR
env_file=$ENV_FILE
compose_files=$COMPOSE_FILES_RAW
project=$PROJECT_NAME
image=$IMAGE
manager_bin=$MANAGER_BIN
host=$(env_get SHKEEPER_HOST || true)
port=$(env_get SHKEEPER_PORT || true)
cryptos=$(current_cryptos)
SECRET_KEY=$(redacted_state SECRET_KEY)
SHKEEPER_BACKEND_KEY=$(redacted_state SHKEEPER_BACKEND_KEY)
SUGGESTED_WALLET_APIKEY=$(redacted_state SUGGESTED_WALLET_APIKEY)
MARIADB_DATABASE_URL=$(redacted_state MARIADB_DATABASE_URL)
EOF
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
  prepare_cryptos_for_enable "$cryptos"
  write_cryptos "$cryptos"
  set_wallet_envs enabled "$cryptos"
  stop_unused_workers "$cryptos"
  start_selected no-infra
  restart_main no-build
  log "enabled cryptos: $cryptos"
}

enable_crypto() {
  [ "$#" -gt 0 ] || die "enable-crypto requires at least one crypto"
  init_env
  validate_cryptos "$@"
  local current next
  current="$(current_cryptos)"
  next="$(normalize_crypto_list "$current" "$@")"
  prepare_cryptos_for_enable "$next"
  write_cryptos "$next"
  set_wallet_envs enabled "$@"
  start_selected no-infra
  restart_main no-build
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

set_api_key() {
  [ "$#" -eq 1 ] || die "set-api-key requires: <api-key-file>"
  set_env_secret_file SUGGESTED_WALLET_APIKEY "$1"
}

set_secret_key() {
  [ "$#" -eq 1 ] || die "set-secret-key requires: <secret-key-file>"
  set_env_secret_file SECRET_KEY "$1"
}

set_backend_key() {
  [ "$#" -eq 1 ] || die "set-backend-key requires: <backend-key-file>"
  set_env_secret_file SHKEEPER_BACKEND_KEY "$1"
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
  local service
  [ -f "$password_file" ] || die "password file does not exist: $password_file"
  mkdir -p "$REPORT_DIR"
  service="$(main_service)"
  if [ "$DRY_RUN" = "1" ]; then
    printf '[shkeeperctl] dry-run: docker compose run --rm --no-deps --user %q -e MARIADB_DATABASE_URL=*** -e ADMIN_USERNAME=%q -e ADMIN_PASSWORD_FILE=/run/secrets/admin_password -v %q:/run/secrets/admin_password:ro -v %q:/deploy-reports %q /app/admin-account\n' "$UTILITY_DOCKER_USER" "$username" "$password_file" "$REPORT_DIR" "$service"
    return 0
  fi
  compose run --rm --no-deps --user "$UTILITY_DOCKER_USER" \
    -e MARIADB_DATABASE_URL="$(mariadb_url)" \
    -e ADMIN_USERNAME="$username" \
    -e ADMIN_PASSWORD_FILE=/run/secrets/admin_password \
    -e ADMIN_ACCOUNT_REPORT_FILE=/deploy-reports/go-shkeeper-admin-account.json \
    -v "$password_file:/run/secrets/admin_password:ro" \
    -v "$REPORT_DIR:/deploy-reports" \
    "$service" /app/admin-account
}

worker_serverkey() {
  [ "$#" -eq 3 ] || die "worker-serverkey requires: <cryptos> <username> <password-file>"
  require_env_file
  local cryptos="$1"
  local username="$2"
  local password_file="$3"
  local service
  validate_cryptos "$cryptos"
  [ -f "$password_file" ] || die "password file does not exist: $password_file"
  mkdir -p "$REPORT_DIR"
  service="$(main_service)"
  if [ "$DRY_RUN" = "1" ]; then
    printf '[shkeeperctl] dry-run: docker compose run --rm --no-deps --user %q -e MARIADB_DATABASE_URL=*** -e WORKER_SERVERKEY_CRYPTOS=%q -e WORKER_USERNAME=%q -e WORKER_PASSWORD_FILE=/run/secrets/worker_password -v %q:/run/secrets/worker_password:ro -v %q:/deploy-reports %q /app/worker-serverkey\n' "$UTILITY_DOCKER_USER" "$(normalize_crypto_list "$cryptos")" "$username" "$password_file" "$REPORT_DIR" "$service"
    return 0
  fi
  compose run --rm --no-deps --user "$UTILITY_DOCKER_USER" \
    -e MARIADB_DATABASE_URL="$(mariadb_url)" \
    -e WORKER_SERVERKEY_CRYPTOS="$(normalize_crypto_list "$cryptos")" \
    -e WORKER_USERNAME="$username" \
    -e WORKER_PASSWORD_FILE=/run/secrets/worker_password \
    -e WORKER_SERVERKEY_REPORT_FILE=/deploy-reports/go-shkeeper-worker-serverkey.json \
    -v "$password_file:/run/secrets/worker_password:ro" \
    -v "$REPORT_DIR:/deploy-reports" \
    "$service" /app/worker-serverkey
}

panel_pause() {
  printf '\n按回车继续...' >&2
  IFS= read -r _ || true
}

panel_prompt() {
  local label="$1"
  local default="${2:-}"
  local value
  if [ -n "$default" ]; then
    printf '%s [%s]: ' "$label" "$default" >&2
  else
    printf '%s: ' "$label" >&2
  fi
  IFS= read -r value
  value="$(trim "$value")"
  if [ -z "$value" ]; then
    value="$default"
  fi
  printf '%s' "$value"
}

panel_set_admin_password() {
  local username file
  username="$(panel_prompt "管理员用户名" "admin")"
  file="$(prompt_secret_to_file "管理员密码" "$SECRET_DIR/admin_password")"
  admin_password "$username" "$file"
}

panel_set_api_key() {
  local file
  file="$(prompt_secret_to_file "钱包 API Key" "$SECRET_DIR/api_key")"
  set_api_key "$file"
}

panel_set_secret_key() {
  local file
  file="$(prompt_secret_to_file "Cookie SECRET_KEY" "$SECRET_DIR/secret_key")"
  set_secret_key "$file"
}

panel_set_backend_key() {
  local file
  file="$(prompt_secret_to_file "Backend Key" "$SECRET_DIR/backend_key")"
  set_backend_key "$file"
}

panel_set_worker_serverkey() {
  local cryptos username file default_cryptos
  default_cryptos="$(current_cryptos)"
  cryptos="$(panel_prompt "写入 worker serverkey 的币种" "$default_cryptos")"
  username="$(panel_prompt "worker 用户名" "worker")"
  file="$(prompt_secret_to_file "worker 密码" "$SECRET_DIR/worker_password")"
  worker_serverkey "$cryptos" "$username" "$file"
}

panel_source_upgrade() {
  if [ -n "${SHKEEPER_SOURCE_ARCHIVE:-}" ]; then
    source_upgrade_stack "$SHKEEPER_SOURCE_ARCHIVE"
    return 0
  fi
  if is_git_worktree; then
    pull_upgrade_stack
    return 0
  fi
  die "当前目录不是 Git 仓库，无法自动拉取；请设置 SHKEEPER_SOURCE_ARCHIVE=/tmp/go-shkeeper-src.tgz 后再执行"
}

panel_uninstall() {
  local confirm purge
  confirm="$(panel_prompt "输入 GO_SHKEEPER 确认卸载" "")"
  [ "$confirm" = "GO_SHKEEPER" ] || die "uninstall cancelled"
  purge="$(panel_prompt "是否同时删除数据卷？输入 DELETE_GO_SHKEEPER_DATA 删除，直接回车保留" "")"
  if [ "$purge" = "DELETE_GO_SHKEEPER_DATA" ]; then
    CONFIRM_UNINSTALL=GO_SHKEEPER PURGE_DATA=1 CONFIRM_PURGE=DELETE_GO_SHKEEPER_DATA uninstall_stack
  else
    CONFIRM_UNINSTALL=GO_SHKEEPER uninstall_stack
  fi
}

run_panel() {
  is_interactive || die "panel requires an interactive terminal"
  local choice value
  while true; do
    printf '\nGo SHKeeper 管理面板\n'
    print_status_summary
    printf '密钥: API=%s  Cookie=%s  Backend=%s  MariaDB=%s\n' "$(colored_redacted_state SUGGESTED_WALLET_APIKEY)" "$(colored_redacted_state SECRET_KEY)" "$(colored_redacted_state SHKEEPER_BACKEND_KEY)" "$(colored_redacted_state MARIADB_DATABASE_URL)"
    cat <<'EOF'
  1) 安装/启动
  2) 更新并重启
  3) 停止
  4) 卸载
  5) 状态
  6) 日志
  7) 配置 IP/端口/币种
  8) 启用币种
  9) 禁用币种
 10) 设置管理员账号密码
 11) 设置钱包 API Key
 12) 设置 Cookie SECRET_KEY
 13) 设置 Backend Key
 14) 写入 worker serverkey
 15) 显示配置摘要
 16) 安装/刷新 shkeeperctl 短命令
 17) Docker 调试
 18) 最终就绪检查
 19) 源码升级并验证
  0) 退出
EOF
    printf '选择: ' >&2
    IFS= read -r choice
    case "$(trim "$choice")" in
      1) install_stack; panel_pause ;;
      2) upgrade_stack; panel_pause ;;
      3) require_env_file; compose stop; panel_pause ;;
      4) panel_uninstall; panel_pause ;;
      5) print_status_summary; require_env_file; compose ps; panel_pause ;;
      6) require_env_file; compose logs -f ;;
      7) configure_stack; panel_pause ;;
      8) value="$(panel_prompt "启用币种/网络")"; value="$(selection_to_cryptos "$value" "")"; enable_crypto "$value"; panel_pause ;;
      9) value="$(panel_prompt "禁用币种/网络")"; value="$(selection_to_cryptos "$value" "")"; disable_crypto "$value"; panel_pause ;;
      10) panel_set_admin_password; panel_pause ;;
      11) panel_set_api_key; panel_pause ;;
      12) panel_set_secret_key; panel_pause ;;
      13) panel_set_backend_key; panel_pause ;;
      14) panel_set_worker_serverkey; panel_pause ;;
      15) show_config; panel_pause ;;
      16) install_manager required; panel_pause ;;
      17) run_debug; panel_pause ;;
      18) run_readiness; panel_pause ;;
      19) panel_source_upgrade; panel_pause ;;
      0) return 0 ;;
      *) printf '未知选项。\n' >&2; panel_pause ;;
    esac
  done
}

run_debug() {
  if [ "$DRY_RUN" = "1" ]; then
    printf '[shkeeperctl] dry-run: GO_SHKEEPER_IMAGE=%q bash %q\n' "$IMAGE" "$ROOT_DIR/deploy/docker-debug.sh"
    return 0
  fi
  GO_SHKEEPER_IMAGE="$IMAGE" bash "$ROOT_DIR/deploy/docker-debug.sh"
}

run_readiness() {
  if [ "$DRY_RUN" = "1" ]; then
    printf '[shkeeperctl] dry-run: GO_SHKEEPER_IMAGE=%q bash %q\n' "$IMAGE" "$ROOT_DIR/deploy/final-readiness.sh"
    return 0
  fi
  GO_SHKEEPER_IMAGE="$IMAGE" bash "$ROOT_DIR/deploy/final-readiness.sh"
}

if [ "$#" -eq 0 ]; then
  if is_interactive; then
    cmd="panel"
  else
    cmd="help"
  fi
else
  cmd="$1"
  shift
fi

case "$cmd" in
  help|-h|--help) usage ;;
  panel|menu) run_panel ;;
  install-manager) install_manager required ;;
  init) init_env ;;
  configure) configure_stack ;;
  install) install_stack ;;
  upgrade) upgrade_stack ;;
  pull-upgrade|auto-upgrade) pull_upgrade_stack ;;
  source-upgrade) source_upgrade_stack "$@" ;;
  uninstall) uninstall_stack ;;
  start) start_selected ;;
  stop) require_env_file; compose stop "$@" ;;
  restart) require_env_file; start_selected; restart_main ;;
  status|ps) print_status_summary; require_env_file; compose ps ;;
  logs) require_env_file; compose logs -f "$@" ;;
  build) compose_build_selected ;;
  show-config) show_config ;;
  show-cryptos) show_cryptos ;;
  list-cryptos) list_cryptos ;;
  set-cryptos) set_cryptos "$@" ;;
  enable-crypto) enable_crypto "$@" ;;
  disable-crypto) disable_crypto "$@" ;;
  set-api-key) set_api_key "$@" ;;
  set-secret-key) set_secret_key "$@" ;;
  set-backend-key) set_backend_key "$@" ;;
  admin-password) admin_password "$@" ;;
  worker-serverkey) worker_serverkey "$@" ;;
  debug) run_debug ;;
  readiness) run_readiness ;;
  *) usage; die "unknown command: $cmd" ;;
esac
