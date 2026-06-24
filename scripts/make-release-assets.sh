#!/usr/bin/env bash
#
# make-release-assets.sh — produce ALL GitHub Release assets for a version, in
# one shot, so nothing is forgotten. Outputs to dist/:
#
#   edgenest-<ver>-linux-amd64.tar.gz          (source bundle + bin/edgenest + bin/sing-box)
#   edgenest-<ver>-linux-arm64.tar.gz
#   sing-box-<sbver>-with_v2ray_api-linux-amd64.tar.gz   (standalone engine asset)
#   sing-box-<sbver>-with_v2ray_api-linux-arm64.tar.gz
#   SHA256SUMS
#
# WHY THIS EXISTS: install.sh resolves sing-box via a 4-tier fallback, and tier 3
# (EdgeNest Release asset, scripts/install.sh:try_release_singbox) needs the
# standalone `sing-box-<sbver>-with_v2ray_api-linux-<arch>.tar.gz` published on
# the release tag. make-deploy-tarball.sh bundles sing-box INTO the edgenest
# tarball but does NOT emit that standalone asset — so if you only run
# make-deploy-tarball.sh and upload its output, a *pure git clone* on a fresh VPS
# can't find the asset and falls all the way to a slow on-VPS source build (Go +
# compile, minutes; OOMs on <1GB-RAM boxes). EdgeNest needs a sing-box built WITH
# `with_v2ray_api` (per-user quota reads experimental.v2ray_api StatsService; the
# official release binary omits it), so the official binary is never an option.
# This script emits BOTH so clone-installs hit the fast path.
#
# Usage:
#   scripts/make-release-assets.sh
#
# Then upload (the script prints this command at the end):
#   cd dist && gh release upload v<ver> \
#     edgenest-<ver>-linux-amd64.tar.gz edgenest-<ver>-linux-arm64.tar.gz \
#     sing-box-<sbver>-with_v2ray_api-linux-amd64.tar.gz \
#     sing-box-<sbver>-with_v2ray_api-linux-arm64.tar.gz \
#     SHA256SUMS --repo aipo-lenshow/EdgeNest --clobber
#
# The standalone sing-box binaries are CROSS-COMPILED (CGO_ENABLED=0), so this
# runs fine on macOS. They cannot be run-verified on the build host when its OS
# differs from linux; `_singbox_verify` (version + with_v2ray_api) runs on the
# target at install time. To pre-verify, scp each to a matching-arch linux box
# and run `sing-box version`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="$(grep -E '^VERSION' Makefile | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
[ -n "$VERSION" ] || { echo "could not read VERSION from Makefile" >&2; exit 1; }
# SINGBOX_VERSION single source of truth = install.sh (no second pin to drift).
SBVER="$(grep -E '^SINGBOX_VERSION=' scripts/install.sh | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
[ -n "$SBVER" ] || { echo "could not read SINGBOX_VERSION from scripts/install.sh" >&2; exit 1; }

echo "==> EdgeNest v$VERSION  ·  sing-box v$SBVER  ·  building all release assets"
mkdir -p dist

sha() { command -v sha256sum >/dev/null 2>&1 && sha256sum "$1" || shasum -a 256 "$1"; }

first=1
for ARCH in amd64 arm64; do
  echo
  echo "==================== $ARCH ===================="

  # 1. custom sing-box (with_v2ray_api). Always (re)build for reproducibility.
  echo "==> building sing-box v$SBVER ($ARCH)"
  SINGBOX_VERSION="$SBVER" GOOS=linux GOARCH="$ARCH" bash scripts/build-singbox.sh >/dev/null
  SB_BIN="bin/sing-box-${SBVER}-linux-${ARCH}"
  [ -f "$SB_BIN" ] || { echo "sing-box build produced no $SB_BIN" >&2; exit 1; }

  # 2. edgenest deploy tarball (= release asset; bundles source + bin/edgenest +
  #    the sing-box we just built). Build web once (first arch), reuse after.
  if [ "$first" = 1 ]; then
    echo "==> make-deploy-tarball.sh $ARCH (builds web)"
    bash scripts/make-deploy-tarball.sh "$ARCH"
    first=0
  else
    echo "==> make-deploy-tarball.sh $ARCH (SKIP_WEB=1, reuse embed)"
    SKIP_WEB=1 bash scripts/make-deploy-tarball.sh "$ARCH"
  fi

  # 3. standalone sing-box release asset (the asset try_release_singbox expects).
  SB_ASSET="sing-box-${SBVER}-with_v2ray_api-linux-${ARCH}.tar.gz"
  echo "==> packaging $SB_ASSET"
  tar -czf "dist/$SB_ASSET" -C bin "sing-box-${SBVER}-linux-${ARCH}"
done

# 4. SHA256SUMS over the four tarballs (names only, so `sha256sum -c` works from dist/).
echo
echo "==> generating dist/SHA256SUMS"
( cd dist && {
    sha "edgenest-${VERSION}-linux-amd64.tar.gz"
    sha "edgenest-${VERSION}-linux-arm64.tar.gz"
    sha "sing-box-${SBVER}-with_v2ray_api-linux-amd64.tar.gz"
    sha "sing-box-${SBVER}-with_v2ray_api-linux-arm64.tar.gz"
  } | sed "s#dist/##" > SHA256SUMS )
cat dist/SHA256SUMS

echo
echo "==> done. Assets in dist/. Upload with:"
echo
echo "  cd dist && gh release upload v${VERSION} \\"
echo "    edgenest-${VERSION}-linux-amd64.tar.gz edgenest-${VERSION}-linux-arm64.tar.gz \\"
echo "    sing-box-${SBVER}-with_v2ray_api-linux-amd64.tar.gz \\"
echo "    sing-box-${SBVER}-with_v2ray_api-linux-arm64.tar.gz \\"
echo "    SHA256SUMS --repo aipo-lenshow/EdgeNest --clobber"
