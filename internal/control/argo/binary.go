// Package argo manages the cloudflared binary and the lifecycle of a single
// Cloudflare Tunnel that EdgeNest can expose its CDN-eligible inbounds
// through. Two modes are supported:
//
//   - "temp": no operator domain required. cloudflared is started with
//     `--url http://127.0.0.1:<port>` and Cloudflare hands back a random
//     `*.trycloudflare.com` hostname which the supervisor parses out of stdout.
//   - "named": the operator already owns a domain delegated to Cloudflare and
//     has obtained a tunnel token from the Zero Trust dashboard; cloudflared
//     is started with `tunnel run --token <token>` and the operator's domain
//     is the share hostname.
//
// The binary is downloaded from GitHub Releases on first use into
// /etc/edgenest/bin/cloudflared (when EdgeNest can write there) or the local
// state directory; SHA-256 hashes are pinned to defeat tampering.
package argo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Pinned cloudflared release. Bump the version + hashes together when refreshing.
//
// To regenerate hashes:
//
//	curl -L https://github.com/cloudflare/cloudflared/releases/download/<ver>/cloudflared-linux-<arch> | sha256sum
const (
	pinnedVersion = "2025.5.0"
)

// pinnedHashes maps GOOS/GOARCH → SHA-256 of the bare cloudflared binary at
// pinnedVersion. Linux is the only production target — EdgeNest ships as a
// Linux Docker / systemd service. macOS cloudflared releases ship as `.tgz`
// archives under a different filename, so they're intentionally absent from
// the pin table; a darwin host hitting Argo gets ErrUnsupportedPlatform rather
// than a silently-broken download.
//
// To refresh after bumping pinnedVersion:
//
//	for arch in linux-amd64 linux-arm64; do
//	  curl -sL "https://github.com/cloudflare/cloudflared/releases/download/$VERSION/cloudflared-$arch" | sha256sum
//	done
var pinnedHashes = map[string]string{
	"linux/amd64": "a62266fd02041374f1fca0d85694aafdf7e26e171a314467356b471d4ebb2393",
	"linux/arm64": "47e55e6eba2755239f641c2c4f89878643ac0d9eaa127a6c84a2cb43fa2e0f03",
}

// downloadURL returns the GitHub Releases URL for the pinned binary on the
// current host. cloudflared publishes per-arch binaries (no archive) under a
// stable naming scheme; we mirror that exactly.
func downloadURL() string {
	asset := fmt.Sprintf("cloudflared-%s-%s", runtime.GOOS, runtime.GOARCH)
	return fmt.Sprintf("https://github.com/cloudflare/cloudflared/releases/download/%s/%s",
		pinnedVersion, asset)
}

// platformKey returns the pinnedHashes lookup key for this host.
func platformKey() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// ErrUnsupportedPlatform is returned when no pinned hash exists for the
// current GOOS/GOARCH — refusing to install an unverified binary is safer
// than letting the operator silently run something we haven't audited.
var ErrUnsupportedPlatform = errors.New("argo: no pinned cloudflared binary for this platform")

// BinaryManager owns the cached cloudflared binary on disk: where it lives,
// whether it's up to date, and how to fetch it on first use. Path() is the
// only call site downstream code needs — it returns a ready-to-exec absolute
// path, downloading + verifying on first call.
type BinaryManager struct {
	dir  string       // directory to install cloudflared into
	http *http.Client // configurable so tests can inject an in-process server
}

// DefaultBinDir is where EdgeNest keeps its private copy of cloudflared. Used
// both as the BinaryManager default and by KillStray, so the path that launches
// cloudflared and the path that reaps it can never drift apart.
const DefaultBinDir = "/etc/edgenest/bin"

// NewBinaryManager returns a manager that caches cloudflared inside dir. The
// caller is responsible for ensuring dir is writable (typically the runtime
// owner of EdgeNest creates /etc/edgenest/bin during bootstrap).
func NewBinaryManager(dir string) *BinaryManager {
	return &BinaryManager{
		dir:  dir,
		http: &http.Client{Timeout: 5 * time.Minute},
	}
}

// WithHTTPClient is for tests; production callers stick with the default.
func (m *BinaryManager) WithHTTPClient(c *http.Client) *BinaryManager {
	return &BinaryManager{dir: m.dir, http: c}
}

// Path returns the absolute path to a verified cloudflared binary, downloading
// it on first use. Subsequent calls re-verify the on-disk hash and re-download
// only if the file was tampered with or corrupted.
//
// ctx bounds the network operation; pass a deadline of a couple of minutes
// for the first call (binary is ~30 MB).
func (m *BinaryManager) Path(ctx context.Context) (string, error) {
	want, ok := pinnedHashes[platformKey()]
	if !ok {
		return "", ErrUnsupportedPlatform
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return "", fmt.Errorf("argo: create binary dir: %w", err)
	}
	target := filepath.Join(m.dir, "cloudflared")

	// Fast path: file exists, hash matches.
	if hash, err := fileHash(target); err == nil && hash == want {
		return target, nil
	}

	// Slow path: download + verify atomically.
	tmp, err := os.CreateTemp(m.dir, "cloudflared.dl-*")
	if err != nil {
		return "", fmt.Errorf("argo: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once rename succeeds

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL(), nil)
	if err != nil {
		tmp.Close()
		return "", fmt.Errorf("argo: build download request: %w", err)
	}
	req.Header.Set("User-Agent", "EdgeNest/argo")

	resp, err := m.http.Do(req)
	if err != nil {
		tmp.Close()
		return "", fmt.Errorf("argo: fetch cloudflared: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		tmp.Close()
		return "", fmt.Errorf("argo: download status %d", resp.StatusCode)
	}

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body); err != nil {
		tmp.Close()
		return "", fmt.Errorf("argo: copy download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("argo: close tempfile: %w", err)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != want {
		return "", fmt.Errorf("argo: cloudflared hash mismatch: got %s, want %s", got, want)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return "", fmt.Errorf("argo: chmod: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return "", fmt.Errorf("argo: rename: %w", err)
	}
	return target, nil
}

// fileHash returns the SHA-256 of a file in lowercase hex, or an error if the
// file does not exist or can't be read.
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
