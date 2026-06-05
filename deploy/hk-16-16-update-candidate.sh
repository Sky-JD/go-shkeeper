#!/usr/bin/env bash
set -euo pipefail

# Push the current go-shkeeper working tree to hk-16-16 and rebuild the Go
# candidate stack. Runtime files on the server are preserved.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCAL_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

REMOTE_HOST="${HK_SHKEEPER_HOST:-hk-16-16}"
REMOTE_DIR="${HK_SHKEEPER_REMOTE_DIR:-/root/go-shkeeper}"
REMOTE_ARCHIVE="${HK_SHKEEPER_REMOTE_ARCHIVE:-/tmp/go-shkeeper-src-$(date +%Y%m%d%H%M%S).tgz}"
REMOTE_MANAGER_TMP="${HK_SHKEEPER_REMOTE_MANAGER_TMP:-/tmp/shkeeperctl-$(date +%Y%m%d%H%M%S).sh}"
SSH_BIN="${SSH_BIN:-ssh}"
SCP_BIN="${SCP_BIN:-scp}"
TAR_BIN="${TAR_BIN:-tar}"
KEEP_LOCAL_ARCHIVE="${KEEP_LOCAL_ARCHIVE:-0}"
KEEP_REMOTE_ARCHIVE="${KEEP_REMOTE_ARCHIVE:-0}"
SKIP_VERIFY="${HK_SHKEEPER_SKIP_VERIFY:-0}"

log() {
  printf '[hk-update] %s\n' "$*"
}

die() {
  printf '[hk-update] error: %s\n' "$*" >&2
  exit 1
}

shell_quote() {
  local value="${1:-}"
  printf "'%s'" "$(printf '%s' "$value" | sed "s/'/'\\\\''/g")"
}

remote_env_assignment() {
  local name="$1"
  local value="${!name-}"
  if [ -n "$value" ]; then
    printf ' %s=%s' "$name" "$(shell_quote "$value")"
  fi
}

command -v "$TAR_BIN" >/dev/null 2>&1 || die "tar not found"
command -v "$SSH_BIN" >/dev/null 2>&1 || die "ssh not found"
command -v "$SCP_BIN" >/dev/null 2>&1 || die "scp not found"
[ -f "$LOCAL_ROOT/go.mod" ] || die "cannot find go.mod under $LOCAL_ROOT"
[ -d "$LOCAL_ROOT/internal" ] || die "cannot find internal/ under $LOCAL_ROOT"
[ -f "$LOCAL_ROOT/deploy/shkeeperctl.sh" ] || die "cannot find deploy/shkeeperctl.sh under $LOCAL_ROOT"

tmp_dir="$(mktemp -d)"
archive="$tmp_dir/go-shkeeper-src.tgz"
cleanup() {
  if [ "$KEEP_LOCAL_ARCHIVE" = "1" ]; then
    log "kept local archive: $archive"
  else
    rm -rf "$tmp_dir"
  fi
}
trap cleanup EXIT

log "packing $LOCAL_ROOT"
"$TAR_BIN" -czf "$archive" \
  --exclude='.git' \
  --exclude='.idea' \
  --exclude='.playwright-cli' \
  --exclude='.env' \
  --exclude='secrets' \
  --exclude='deploy-reports' \
  --exclude='node_modules' \
  --exclude='tmp' \
  --exclude='output' \
  --exclude='dist' \
  --exclude='build' \
  --exclude='coverage' \
  --exclude='web/admin/node_modules' \
  --exclude='web/admin/.vite' \
  --exclude='docker-compose.example.yml' \
  --exclude='deploy/shkeeperctl.sh' \
  --exclude='deploy/install.sh' \
  --exclude='deploy/hk-docker-debug.sh' \
  -C "$LOCAL_ROOT" .

log "uploading source archive to $REMOTE_HOST:$REMOTE_ARCHIVE"
"$SCP_BIN" "$archive" "$REMOTE_HOST:$REMOTE_ARCHIVE"
log "uploading manager script to $REMOTE_HOST:$REMOTE_MANAGER_TMP"
"$SCP_BIN" "$LOCAL_ROOT/deploy/shkeeperctl.sh" "$REMOTE_HOST:$REMOTE_MANAGER_TMP"

remote_prefix="REMOTE_DIR=$(shell_quote "$REMOTE_DIR") REMOTE_ARCHIVE=$(shell_quote "$REMOTE_ARCHIVE") REMOTE_MANAGER_TMP=$(shell_quote "$REMOTE_MANAGER_TMP") SHKEEPER_KEEP_SOURCE_ARCHIVE=$(shell_quote "$KEEP_REMOTE_ARCHIVE") SHKEEPER_VERIFY_FRONTEND=1 SHKEEPER_SKIP_GIT_PULL=1"
if [ "$SKIP_VERIFY" = "1" ]; then
  remote_prefix+=" SHKEEPER_VERIFY_FRONTEND=0"
fi
for var in SHKEEPER_ENV_FILE SHKEEPER_COMPOSE_FILE SHKEEPER_COMPOSE_FILES SHKEEPER_PROJECT_NAME GO_SHKEEPER_IMAGE SHKEEPER_DRY_RUN; do
  remote_prefix+=$(remote_env_assignment "$var")
done

log "installing manager and running source-upgrade on $REMOTE_HOST"
"$SSH_BIN" "$REMOTE_HOST" "$remote_prefix bash -s" <<'REMOTE_SCRIPT'
set -euo pipefail

log() {
  printf '[hk-update:remote] %s\n' "$*"
}

die() {
  printf '[hk-update:remote] error: %s\n' "$*" >&2
  exit 1
}

[ -n "${REMOTE_DIR:-}" ] || die "REMOTE_DIR is empty"
[ -n "${REMOTE_ARCHIVE:-}" ] || die "REMOTE_ARCHIVE is empty"
[ -n "${REMOTE_MANAGER_TMP:-}" ] || die "REMOTE_MANAGER_TMP is empty"
[ -f "$REMOTE_ARCHIVE" ] || die "archive not found: $REMOTE_ARCHIVE"
[ -f "$REMOTE_MANAGER_TMP" ] || die "manager script not found: $REMOTE_MANAGER_TMP"

mkdir -p "$REMOTE_DIR/deploy"
if [ -f "$REMOTE_DIR/deploy/shkeeperctl.sh" ]; then
  cp -a "$REMOTE_DIR/deploy/shkeeperctl.sh" "/tmp/shkeeperctl.backup.$(date +%Y%m%d%H%M%S).sh"
fi
install -m 0755 "$REMOTE_MANAGER_TMP" "$REMOTE_DIR/deploy/shkeeperctl.sh"
rm -f "$REMOTE_MANAGER_TMP"
cd "$REMOTE_DIR"
log "running shkeeperctl source-upgrade"
bash deploy/shkeeperctl.sh source-upgrade "$REMOTE_ARCHIVE"
REMOTE_SCRIPT

log "done"
