#!/usr/bin/env bash
# EdgeNest self-upgrade safety wrapper.
#
# Invoked three ways, all funnelling here so the logic lives in one place:
#   - Web panel:  systemd-run --unit=edgenest-upgrade --collect bash upgrade.sh [tag]
#   - CLI menu:   bash upgrade.sh [tag]          (foreground, live TTY output)
#   - TG bot:     systemd-run ... bash upgrade.sh [tag]
#
# The web/bot callers MUST launch it via systemd-run (a transient unit outside
# edgenest's cgroup) so it survives `systemctl restart edgenest` — a plain child
# of the panel process would be killed when the service restarts.
#
# What it does: lock -> resolve target stable tag -> verify standard layout ->
# back up binary + DB -> re-run install.sh at the new tag -> health-gate ->
# roll back automatically on failure. Every install path the user could have
# taken upgrades the same fixed runtime paths, anchored on the systemd unit.
set -euo pipefail

DATA_DIR="/etc/edgenest"
INSTALL_BIN="/usr/local/bin/edgenest"
UNIT="/etc/systemd/system/edgenest.service"
REPO_URL="https://github.com/aipo-lenshow/EdgeNest.git"
RELEASES_LATEST="https://api.github.com/repos/aipo-lenshow/EdgeNest/releases/latest"

LOCK="$DATA_DIR/.upgrade.lock"
LOG="$DATA_DIR/upgrade.log"
STATUS="$DATA_DIR/upgrade-status.json"
BIN_BAK="$DATA_DIR/edgenest.prev"
DB="$DATA_DIR/edgenest.db"
DB_BAK="$DATA_DIR/edgenest.db.upgrade-bak"
SRC_HINT="$DATA_DIR/install-source"

TARGET_TAG="${1:-}"
CURRENT_VER=""
PANEL_PORT=""
SRC_DIR=""

ts() { date -u +%s; }
log() { printf '%s %s\n' "$(date -u '+%Y-%m-%d %H:%M:%S')" "$*" | tee -a "$LOG" >&2; }

# write_status STATE FROM TO MSG_ZH MSG_EN — atomic status file the 3 surfaces read.
write_status() {
  local tmp="$STATUS.tmp"
  cat > "$tmp" <<EOF
{"state":"$1","from":"$2","to":"$3","message_zh":"$4","message_en":"$5","ts":$(ts)}
EOF
  mv -f "$tmp" "$STATUS"
}

# Mark the bot to announce the result on its next start (it reads this setting).
notify_pending() {
  command -v sqlite3 >/dev/null 2>&1 || return 0
  # The edgenest service holds the DB, so without a busy-timeout this write fails
  # immediately ("database is locked") and the bot never learns to announce the
  # result after its restart. Wait for the lock instead.
  sqlite3 -cmd ".timeout 5000" "$DB" \
    "INSERT INTO settings(key,value) VALUES('upgrade_notify_pending','1')
     ON CONFLICT(key) DO UPDATE SET value='1';" 2>/dev/null || true
}

# strip leading v, trim spaces
norm_tag() { printf '%s' "$1" | sed 's/^v//' | tr -d '[:space:]'; }

read_current_version() {
  CURRENT_VER=$("$INSTALL_BIN" --version 2>/dev/null | awk 'NR==1{print $NF}' || true)
  CURRENT_VER=$(norm_tag "${CURRENT_VER:-}")
}

# Port from the unit's --listen (e.g. [::]:2087 -> 2087). Anchored on the unit
# so a non-default port is honoured.
read_panel_port() {
  local listen
  listen=$(awk -F'--listen ' '/ExecStart=/{print $2}' "$UNIT" 2>/dev/null | awk 'NR==1{print $1}')
  PANEL_PORT=$(printf '%s' "$listen" | sed -E 's/.*:([0-9]+)$/\1/')
  [ -n "$PANEL_PORT" ] || PANEL_PORT="2087"
}

# Resolve target tag: arg wins, else cached latest_version in the DB, else the
# public /releases/latest endpoint (which already excludes pre-releases).
resolve_target() {
  if [ -n "$TARGET_TAG" ]; then TARGET_TAG=$(norm_tag "$TARGET_TAG"); return; fi
  if command -v sqlite3 >/dev/null 2>&1; then
    TARGET_TAG=$(sqlite3 -cmd ".timeout 5000" "$DB" "SELECT value FROM settings WHERE key='latest_version';" 2>/dev/null || true)
  fi
  if [ -z "$TARGET_TAG" ]; then
    local body
    body=$(curl -fsSL -m 10 "$RELEASES_LATEST" 2>/dev/null || true)
    TARGET_TAG=$(printf '%s' "$body" | awk -F'"' '/"tag_name"/{print $4; exit}')
  fi
  TARGET_TAG=$(norm_tag "${TARGET_TAG:-}")
}

# Locate a repo checkout to run install.sh from. Prefer the recorded original
# clone (keeps the user's tree in sync); else a fresh shallow clone in /tmp.
locate_source() {
  local hint=""
  [ -f "$SRC_HINT" ] && hint=$(awk 'NR==1' "$SRC_HINT" 2>/dev/null || true)
  if [ -n "$hint" ] && [ -d "$hint/.git" ] && [ -f "$hint/scripts/install.sh" ]; then
    log "using recorded source clone: $hint"
    # We run as root, but on a non-root install the clone lives in the operator's
    # home and is owned by them; modern git refuses to touch a differently-owned
    # repo ("fatal: detected dubious ownership") — which made fetch/checkout fail
    # and silently leave the tree on the OLD version, so install.sh reinstalled
    # the old binary and the health gate rolled the upgrade back every time on
    # non-root installs. safe.directory='*' trusts the repo for these calls.
    git -c safe.directory='*' -C "$hint" fetch --tags --force origin >>"$LOG" 2>&1
    git -c safe.directory='*' -C "$hint" checkout -f "v$TARGET_TAG" >>"$LOG" 2>&1 \
      || git -c safe.directory='*' -C "$hint" checkout -f "$TARGET_TAG" >>"$LOG" 2>&1
    # Verify the checkout actually landed on the target version (defence in depth:
    # if it didn't — ownership, detached tag, whatever — fall through to a fresh
    # root-owned clone rather than reinstalling the running version and rolling back).
    if grep -q "EDGENEST_VERSION:-${TARGET_TAG}}" "$hint/scripts/install.sh" 2>/dev/null; then
      SRC_DIR="$hint"
      return 0
    fi
    log "in-place checkout did not reach v$TARGET_TAG (repo ownership?); fresh clone instead"
  fi
  log "fresh clone to temp"
  SRC_DIR=$(mktemp -d)
  git clone --depth 1 --branch "v$TARGET_TAG" "$REPO_URL" "$SRC_DIR" >>"$LOG" 2>&1 \
    || git clone --depth 1 --branch "$TARGET_TAG" "$REPO_URL" "$SRC_DIR" >>"$LOG" 2>&1
  [ -f "$SRC_DIR/scripts/install.sh" ]
}

backup() {
  log "backing up binary + DB"
  cp -f "$INSTALL_BIN" "$BIN_BAK"
  if command -v sqlite3 >/dev/null 2>&1 && [ -f "$DB" ]; then
    sqlite3 -cmd ".timeout 5000" "$DB" ".backup '$DB_BAK'" 2>>"$LOG" || cp -f "$DB" "$DB_BAK"
  elif [ -f "$DB" ]; then
    cp -f "$DB" "$DB_BAK"
  fi
}

health_ok() {
  # Poll /api/health (root path, unauthenticated) until version==target & ok.
  local url="http://127.0.0.1:${PANEL_PORT}/api/health" i body ver st
  for i in $(seq 1 30); do
    body=$(curl -fsS -m 4 "$url" 2>/dev/null || true)
    if [ -n "$body" ]; then
      # /api/health is single-line nested JSON: {"success":true,"data":{…,
      # "version":"X","status":"ok",…}}. The old `awk -F'"' '/"version"/{print $4}'`
      # printed the 4th quote-delimited token of the whole line — "data" — never the
      # version, so health_ok ALWAYS failed and every real upgrade rolled back (the
      # no-op path returns before this, which is why smoke tests missed it). Match the
      # exact "version":"…" / "status":"…" fields instead; the leading quote keeps
      # "version" from matching inside "latest_version".
      ver=$(printf '%s' "$body" | grep -oE '"version":"[^"]*"' | head -1 | sed 's/.*:"//;s/"$//')
      st=$(printf '%s'  "$body" | grep -oE '"status":"[^"]*"'  | head -1 | sed 's/.*:"//;s/"$//')
      if [ "$(norm_tag "$ver")" = "$TARGET_TAG" ] && [ "$st" = "ok" ]; then
        return 0
      fi
    fi
    sleep 2
  done
  return 1
}

rollback() {
  log "ROLLBACK: restoring previous binary + DB"
  local ok=1
  if [ -f "$BIN_BAK" ]; then install -m 0755 "$BIN_BAK" "$INSTALL_BIN" || ok=0; else ok=0; fi
  [ -f "$DB_BAK" ] && { cp -f "$DB_BAK" "$DB" || ok=0; }
  systemctl restart edgenest >>"$LOG" 2>&1 || ok=0
  sleep 3
  read_current_version
  if [ "$ok" = "1" ]; then
    log "rolled back to ${CURRENT_VER:-previous}"
    write_status "rolledback" "$CURRENT_VER" "$TARGET_TAG" \
      "升级失败，已自动回滚到 v${CURRENT_VER}，服务正常。" \
      "Upgrade failed; rolled back to v${CURRENT_VER}. Service is healthy."
  else
    log "ROLLBACK FAILED — manual reinstall required"
    write_status "manual" "$CURRENT_VER" "$TARGET_TAG" \
      "升级失败且无法自动回滚。建议：bash scripts/uninstall.sh（保数据）→ git clone 最新版 → 重装。" \
      "Upgrade failed and auto-rollback did not recover. Recommended: bash scripts/uninstall.sh (keeps data) -> git clone latest -> reinstall."
  fi
  notify_pending
}

main() {
  mkdir -p "$DATA_DIR"
  read_current_version
  read_panel_port
  resolve_target

  log "upgrade requested: current=${CURRENT_VER:-?} target=${TARGET_TAG:-?} port=${PANEL_PORT}"

  if [ -z "$TARGET_TAG" ]; then
    write_status "manual" "$CURRENT_VER" "" \
      "无法确定目标版本（拉取最新稳定版失败）。" \
      "Could not determine target version (failed to fetch latest stable)."
    log "no target tag resolved; abort"; exit 1
  fi
  if [ "$TARGET_TAG" = "$CURRENT_VER" ]; then
    write_status "success" "$CURRENT_VER" "$TARGET_TAG" \
      "已是最新稳定版 v${CURRENT_VER}。" "Already on the latest stable v${CURRENT_VER}."
    log "already up to date"; exit 0
  fi

  # Pre-flight: only upgrade an installer-managed, standard-layout node.
  if ! grep -q "ExecStart=${INSTALL_BIN} " "$UNIT" 2>/dev/null; then
    write_status "manual" "$CURRENT_VER" "$TARGET_TAG" \
      "检测到非标准安装（systemd 单元未指向标准路径），为安全起见不自动升级。请按 README 重装。" \
      "Non-standard install detected (systemd unit does not use the standard path). Refusing to auto-upgrade; please reinstall per the README."
    log "non-standard layout; refusing"; exit 1
  fi

  write_status "running" "$CURRENT_VER" "$TARGET_TAG" \
    "正在升级到 v${TARGET_TAG}…" "Upgrading to v${TARGET_TAG}…"

  backup

  if ! locate_source; then
    log "failed to obtain source for v$TARGET_TAG"; rollback; exit 1
  fi

  log "running installer at $SRC_DIR (tag v$TARGET_TAG)"
  if ! ( cd "$SRC_DIR" && EDGENEST_NONINTERACTIVE=1 bash scripts/install.sh --yes ) >>"$LOG" 2>&1; then
    log "installer failed"; rollback; exit 1
  fi

  if health_ok; then
    read_current_version
    log "upgrade OK -> v${CURRENT_VER}"
    write_status "success" "$CURRENT_VER" "$TARGET_TAG" \
      "已升级到 v${CURRENT_VER}。" "Upgraded to v${CURRENT_VER}."
    notify_pending
    rm -f "$DB_BAK"
    exit 0
  fi

  log "health check failed after install"; rollback; exit 1
}

# Serialize: never let two upgrades run at once.
exec 9>"$LOCK"
if ! flock -n 9; then
  log "another upgrade is already running"; exit 1
fi

main "$@"
