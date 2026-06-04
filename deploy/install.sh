#!/usr/bin/env bash
set -euo pipefail

REPO_URL="${GO_SHKEEPER_REPO:-https://github.com/Sky-JD/go-shkeeper.git}"
INSTALL_DIR="${GO_SHKEEPER_DIR:-/root/go-shkeeper}"
REF="${GO_SHKEEPER_REF:-main}"

log() {
  printf '[go-shkeeper-install] %s\n' "$*"
}

die() {
  printf '[go-shkeeper-install] error: %s\n' "$*" >&2
  exit 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "missing command: $1"
}

require_cmd git
require_cmd docker
docker compose version >/dev/null 2>&1 || die "docker compose is required"

if [ -d "$INSTALL_DIR/.git" ]; then
  log "updating existing repository: $INSTALL_DIR"
  git -C "$INSTALL_DIR" fetch --tags origin
else
  log "cloning $REPO_URL to $INSTALL_DIR"
  mkdir -p "$(dirname "$INSTALL_DIR")"
  git clone "$REPO_URL" "$INSTALL_DIR"
fi

git -C "$INSTALL_DIR" checkout "$REF"
if git -C "$INSTALL_DIR" symbolic-ref -q HEAD >/dev/null 2>&1; then
  git -C "$INSTALL_DIR" pull --ff-only
fi

cd "$INSTALL_DIR"
log "running shkeeperctl install"
bash deploy/shkeeperctl.sh install
