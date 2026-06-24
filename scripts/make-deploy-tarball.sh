#!/usr/bin/env bash
# make-deploy-tarball.sh — build a deploy tarball = git-archived source + a
# cross-compiled linux binary under bin/, so install.sh's ensure_edgenest_binary
# finds ./bin/edgenest-linux-<arch> and skips the slow source build
# (INSTALL_SOURCE=local, ~30s instead of a ~5min on-VPS `go build`).
#
# Fixes BUGLOG 0-3: a bare `git archive` omits the gitignored bin/ binary, so the
# VPS falls back to a full source rebuild (the 14k regression). This bundles the
# prebuilt binary into the source tree's bin/ for a fast local install.
#
# Usage:
#   scripts/make-deploy-tarball.sh [amd64|arm64]   # default amd64
#   SKIP_WEB=1 scripts/make-deploy-tarball.sh       # reuse current embed (skip `make web`)
#
# Output: dist/edgenest-<version>-linux-<arch>.tar.gz
set -euo pipefail

ARCH="${1:-amd64}"
case "$ARCH" in
  amd64|arm64) ;;
  *) echo "usage: $0 [amd64|arm64]" >&2; exit 1 ;;
esac

# repo root = parent of this script's directory (works from any cwd)
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="$(grep -E '^VERSION' Makefile | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
[ -n "$VERSION" ] || { echo "could not read VERSION from Makefile" >&2; exit 1; }

# The binary is built from the WORKING TREE but the source is archived from HEAD;
# warn if they diverge so a dirty tree doesn't ship a binary that mismatches the
# bundled source.
if ! git diff --quiet HEAD 2>/dev/null; then
  echo "⚠️  working tree has uncommitted changes — binary is built from the working" >&2
  echo "    tree but source is archived from HEAD; they may differ." >&2
fi

# 1. frontend → embed dir (skippable when the embed is already current)
if [ "${SKIP_WEB:-0}" = "1" ]; then
  echo "==> SKIP_WEB=1: reusing existing embed dir (internal/control/web/dist)"
else
  echo "==> building web frontend + syncing embed dir (make web)"
  make web
fi

# 2. cross-compile the linux binary (reproducible: -trimpath, version stamped)
echo "==> cross-compiling linux/$ARCH binary (v$VERSION)"
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
  go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
  -o "dist/edgenest-linux-$ARCH" ./cmd/edgenest

# 3. stage the source from HEAD (git archive respects .gitignore → no bin/, dist/, or dev-only dirs)
STAGE="dist/edgenest-$VERSION-linux-$ARCH"
rm -rf "$STAGE"
mkdir -p "$STAGE"
echo "==> exporting source (git archive HEAD)"
git archive --format=tar HEAD | tar -x -C "$STAGE"

# 4. drop the prebuilt binary into bin/ so install.sh's ensure_edgenest_binary uses it
mkdir -p "$STAGE/bin"
cp "dist/edgenest-linux-$ARCH" "$STAGE/bin/edgenest-linux-$ARCH"

# 4b. bundle the custom sing-box (with_v2ray_api) if it's already been built, so
# the VPS install resolves it via ensure_singbox_binary's local tier instead of a
# slow on-VPS source build. Version is read from install.sh (single source of
# truth — no second pin to drift). Absence is NOT fatal: install.sh falls back to
# the release asset, then to building from source via scripts/build-singbox.sh.
SBVER="$(grep -E '^SINGBOX_VERSION=' scripts/install.sh | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
SB_BIN="bin/sing-box-${SBVER}-linux-${ARCH}"
if [ -n "$SBVER" ] && [ -f "$SB_BIN" ]; then
  echo "==> bundling custom sing-box v$SBVER ($SB_BIN)"
  cp "$SB_BIN" "$STAGE/bin/sing-box-${SBVER}-linux-${ARCH}"
else
  echo "⚠️  custom sing-box ($SB_BIN) not built — tarball omits it; the VPS install" >&2
  echo "    will use the release asset or build from source. Run" >&2
  echo "    'GOARCH=$ARCH SINGBOX_VERSION=$SBVER scripts/build-singbox.sh' first to" >&2
  echo "    bundle it for a fast local install." >&2
fi

# 5. tarball
TARBALL="dist/edgenest-$VERSION-linux-$ARCH.tar.gz"
tar -czf "$TARBALL" -C dist "edgenest-$VERSION-linux-$ARCH"
rm -rf "$STAGE"

echo "==> done"
ls -la "$TARBALL"
if command -v sha256sum >/dev/null 2>&1; then
  echo "tarball sha256: $(sha256sum "$TARBALL" | cut -d' ' -f1)"
elif command -v shasum >/dev/null 2>&1; then
  echo "tarball sha256: $(shasum -a 256 "$TARBALL" | cut -d' ' -f1)"
fi
