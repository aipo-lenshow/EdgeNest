// xray.go installs and reports on the xray-core binary. EdgeNest can serve
// most inbound types from sing-box alone, but xray-core is still the best
// home for a handful of niche transports (notably VLESS-XHTTP, where the
// xray maintainers ship the canonical implementation). The panel surface is
// deliberately minimal:
//
//   GET  /api/v1/system/xray/status   — installed?  what version?  pinned version?
//   POST /api/v1/system/xray/install  — fetch the pinned release and lay it down
//
// The Dashboard reads /status for a "xray-core: installed v26.3.27" pill;
// the System Info page mounts /install behind a button so a user who skipped
// it during `bash install.sh` can recover without a re-install.
//
// Install side-effects mirror what install.sh does so an ssh-installed and a
// panel-installed xray are interchangeable:
//   - /usr/local/bin/xray (mode 0755)
//   - /usr/local/share/xray/geoip.dat + geosite.dat (mode 0644)
//
// Hash pinning: not yet enforced — xray-core ships per-arch ZIPs with no
// upstream SHA file we can reproducibly pin against, and the install.sh path
// does not pin either. Both paths rely on HTTPS to GitHub Releases for now;
// hardening this to a pinned SHA-256 of the .zip is tracked as a follow-up.

package system

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// PinnedXrayVersion is the version EdgeNest installs when the operator clicks
// "Install" in the panel. Bumping this is the only thing required to ship a
// new xray-core — refresh the version, leave the rest alone.
const PinnedXrayVersion = "26.3.27"

const (
	XrayBinPath  = "/usr/local/bin/xray"
	XrayShareDir = "/usr/local/share/xray"
)

// XrayStatus is the wire shape returned by the status endpoint.
type XrayStatus struct {
	Installed     bool   `json:"installed"`
	Version       string `json:"version,omitempty"`
	Path          string `json:"path"`
	PinnedVersion string `json:"pinned_version"`
	UpdateAvail   bool   `json:"update_available"`
}

// ReadXrayStatus introspects the host filesystem and returns whether xray
// is installed and at what version. Never returns an error — absence of the
// binary is information, not failure.
func ReadXrayStatus() XrayStatus {
	s := XrayStatus{
		Path:          XrayBinPath,
		PinnedVersion: PinnedXrayVersion,
	}
	if _, err := os.Stat(XrayBinPath); err != nil {
		return s
	}
	s.Installed = true

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, XrayBinPath, "version").Output()
	if err == nil {
		s.Version = parseXrayVersion(string(out))
	}
	if s.Version != "" && s.Version != PinnedXrayVersion {
		s.UpdateAvail = true
	}
	return s
}

// parseXrayVersion extracts the dotted version from the first line of
// `xray version` output. Tolerant of formatting drift between minor releases.
func parseXrayVersion(out string) string {
	line := strings.SplitN(out, "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		return fields[1]
	}
	return ""
}

// ErrXrayUnsupportedArch fires when the running CPU has no upstream xray
// release; refusing to install is safer than silently picking the wrong
// archive name.
var ErrXrayUnsupportedArch = errors.New("xray install: unsupported architecture")

// InstallXray downloads the pinned xray-core release for the current arch,
// extracts xray + geoip.dat + geosite.dat into the standard locations, and
// returns the resulting XrayStatus. All writes are tmp-then-rename so an
// interrupted install never leaves a half-written binary on disk.
//
// The caller (HTTP handler) supplies ctx with a generous deadline; the zip
// is ~25 MB so 5 minutes on a slow link is a reasonable upper bound.
func InstallXray(ctx context.Context) (XrayStatus, error) {
	arch, ok := xrayArchName()
	if !ok {
		return XrayStatus{}, ErrXrayUnsupportedArch
	}
	if runtime.GOOS != "linux" {
		return XrayStatus{}, fmt.Errorf("xray install: production target is Linux, got %s", runtime.GOOS)
	}

	if err := os.MkdirAll(XrayShareDir, 0o755); err != nil {
		return XrayStatus{}, fmt.Errorf("xray install: prepare share dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(XrayBinPath), 0o755); err != nil {
		return XrayStatus{}, fmt.Errorf("xray install: prepare bin dir: %w", err)
	}

	url := fmt.Sprintf(
		"https://github.com/XTLS/Xray-core/releases/download/v%s/Xray-linux-%s.zip",
		PinnedXrayVersion, arch,
	)

	httpClient := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return XrayStatus{}, fmt.Errorf("xray install: build request: %w", err)
	}
	req.Header.Set("User-Agent", "EdgeNest/xray-installer")

	resp, err := httpClient.Do(req)
	if err != nil {
		return XrayStatus{}, fmt.Errorf("xray install: fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return XrayStatus{}, fmt.Errorf("xray install: download status %d for %s", resp.StatusCode, url)
	}

	tmpZip, err := os.CreateTemp("", "edgenest-xray-*.zip")
	if err != nil {
		return XrayStatus{}, fmt.Errorf("xray install: tempfile: %w", err)
	}
	tmpName := tmpZip.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmpZip, resp.Body); err != nil {
		tmpZip.Close()
		return XrayStatus{}, fmt.Errorf("xray install: write zip: %w", err)
	}
	if err := tmpZip.Close(); err != nil {
		return XrayStatus{}, fmt.Errorf("xray install: close zip: %w", err)
	}

	zr, err := zip.OpenReader(tmpName)
	if err != nil {
		return XrayStatus{}, fmt.Errorf("xray install: open zip: %w", err)
	}
	defer zr.Close()

	want := map[string]struct {
		dest string
		mode os.FileMode
	}{
		"xray":         {XrayBinPath, 0o755},
		"geoip.dat":    {filepath.Join(XrayShareDir, "geoip.dat"), 0o644},
		"geosite.dat":  {filepath.Join(XrayShareDir, "geosite.dat"), 0o644},
	}
	extracted := 0
	for _, f := range zr.File {
		spec, ok := want[f.Name]
		if !ok {
			continue
		}
		if err := extractZipFile(f, spec.dest, spec.mode); err != nil {
			return XrayStatus{}, fmt.Errorf("xray install: extract %s: %w", f.Name, err)
		}
		extracted++
	}
	if extracted == 0 {
		return XrayStatus{}, fmt.Errorf("xray install: archive missing xray + geo files")
	}

	return ReadXrayStatus(), nil
}

// xrayArchName maps Go's runtime.GOARCH to the asset suffix XTLS publishes
// on GitHub Releases. Only amd64 + arm64 ship as part of the standard
// EdgeNest target matrix.
func xrayArchName() (string, bool) {
	switch runtime.GOARCH {
	case "amd64":
		return "64", true
	case "arm64":
		return "arm64-v8a", true
	}
	return "", false
}

// extractZipFile writes a single entry of a ZIP archive to dest atomically.
// We write to dest+".tmp" first so an interrupted extraction never replaces
// a previously-working binary with a half-written file.
func extractZipFile(f *zip.File, dest string, mode os.FileMode) error {
	in, err := f.Open()
	if err != nil {
		return err
	}
	defer in.Close()

	tmpDest := dest + ".edgenest-tmp"
	out, err := os.OpenFile(tmpDest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmpDest)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmpDest)
		return err
	}
	if err := os.Chmod(tmpDest, mode); err != nil {
		os.Remove(tmpDest)
		return err
	}
	return os.Rename(tmpDest, dest)
}
