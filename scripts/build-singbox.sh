#!/usr/bin/env bash
#
# build-singbox.sh — build a sing-box binary with the v2ray_api StatsService
# enabled, which the official release binary omits.
#
# Why EdgeNest ships its own sing-box build: per-user traffic quotas need
# per-user (per-email) byte counters, and the ONLY stock sing-box surface that
# exposes them is experimental.v2ray_api's StatsService. That service is gated
# behind the `with_v2ray_api` build tag, and the official release build tags
# (release/DEFAULT_BUILD_TAGS_OTHERS) do not include it. So we rebuild sing-box
# from its official tagged source with exactly the official tag set PLUS
# with_v2ray_api — nothing else changes. clash_api's quota poller used to read
# /connections, but sing-box never stamps the user there (verified upstream), so
# v2ray_api is the only way (the same per-user accounting xray-core exposes natively).
#
# Reproducible: pins the sing-box version, uses the upstream release tag list
# verbatim, and prints the exact tag set so anyone can audit that the only
# difference from an official build is the one stats tag.
#
# Usage:
#   scripts/build-singbox.sh                 # builds linux/amd64 (default)
#   SINGBOX_VERSION=1.13.13 scripts/build-singbox.sh
#   GOARCH=arm64 scripts/build-singbox.sh    # builds linux/arm64
#
# Output: bin/sing-box-<version>-linux-<arch>  (bin/ is gitignored; this is the
# artifact you scp to a VPS or upload as an EdgeNest release asset).

set -euo pipefail

SINGBOX_VERSION="${SINGBOX_VERSION:-1.13.13}"
GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-amd64}"

# The official release tag set for non-Android/Darwin platforms, copied verbatim
# from sing-box release/DEFAULT_BUILD_TAGS_OTHERS, plus with_v2ray_api. If you
# bump SINGBOX_VERSION, re-check that file in the new tag in case upstream
# changed the default set.
OFFICIAL_TAGS="with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_tailscale,with_ccm,with_ocm,badlinkname,tfogo_checklinkname0"
TAGS="${OFFICIAL_TAGS},with_v2ray_api"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="${repo_root}/bin"
out="${out_dir}/sing-box-${SINGBOX_VERSION}-${GOOS}-${GOARCH}"
mkdir -p "${out_dir}"

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

echo "==> sing-box v${SINGBOX_VERSION}  target ${GOOS}/${GOARCH}"
echo "==> tags: ${TAGS}"
echo "==> cloning official source (tag v${SINGBOX_VERSION})…"
git clone --depth 1 --branch "v${SINGBOX_VERSION}" \
  https://github.com/SagerNet/sing-box.git "${work}/sing-box" >/dev/null 2>&1

cd "${work}/sing-box"

# Mirror upstream's ldflags exactly (release/LDFLAGS + the version stamp + strip).
# -checklinkname=0 is REQUIRED: sing-box's common/badtls //go:linkname's into
# private crypto/tls symbols, which the modern Go linker rejects unless this is
# set (the official build sets it too). We avoid the Makefile (it shells out to a
# read_tag helper + ghr); a direct go build with the pinned tag is just as
# reproducible.
LDFLAGS="-X 'github.com/sagernet/sing-box/constant.Version=${SINGBOX_VERSION}' -X internal/godebug.defaultGODEBUG=multipathtcp=0 -checklinkname=0 -s -w -buildid="

echo "==> building…"
CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" \
  go build -v -trimpath -tags "${TAGS}" -ldflags "${LDFLAGS}" \
  -o "${out}" ./cmd/sing-box

echo "==> built: ${out}"
sha256sum "${out}" 2>/dev/null || shasum -a 256 "${out}"
echo "==> done. Verify it serves v2ray_api by running it with an experimental.v2ray_api block."
