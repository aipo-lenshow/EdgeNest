// Package bootstrap performs first-run provisioning: it generates random admin
// credentials, a random panel path and a JWT secret (persisted in Settings),
// ensures the local node exists, and reports the generated credentials once.
package bootstrap

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/auth"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/selfsigned"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// Default location of the wizard-style self-signed cert pair. Kept in sync
// with internal/control/api/inbound_secrets.go — that file's fillTLSDefaults
// references the same path. Provisioned at bootstrap so ad-hoc TLS inbound
// creation (advanced modal, quick-bundle) doesn't need to run the wizard.
const (
	defaultCertsDir     = "/etc/edgenest/certs"
	defaultWizardCertPN = "wizard-fullchain.pem"
	defaultWizardKeyPN  = "wizard-privkey.pem"
)

// Setting keys.
const (
	KeyJWTSecret  = "jwt_secret"
	KeyPanelPath  = "panel_path"
	KeyWizardDone = "wizard_done"
	KeyRunRole    = "run_role"
	// KeyDefaultLang is the panel's first-load language for browsers that
	// don't yet have an explicit choice in localStorage. Seeded once by
	// install.sh via <dataDir>/install.lang; respected forever after unless
	// the operator updates the setting directly.
	KeyDefaultLang = "default_lang"
)

// SupportedLangs is the set of panel UI languages (frontend locale files +
// install.sh). It gates what may be stored as default_lang so install.sh and
// the switcher can seed the first-paint language. Server-side presentation that
// only speaks zh/en (Telegram bot, alerts, digest) falls back to English for
// the rest — see alertrunner.langForChat etc.
var SupportedLangs = []string{"en", "zh", "zh-TW", "fa", "ru", "vi"}

// IsSupportedLang reports whether v is a recognized panel UI language code.
func IsSupportedLang(v string) bool {
	for _, l := range SupportedLangs {
		if v == l {
			return true
		}
	}
	return false
}

const installerLangFile = "install.lang"

// ImportInstallerLang seeds default_lang from <dataDir>/install.lang on the
// very first start. No-op if the setting is already set (operator/admin
// choice wins), or if the hint file is absent / contains an unknown value.
func ImportInstallerLang(s *store.Store, dataDir string) error {
	existing, err := s.GetSetting(KeyDefaultLang)
	if err != nil {
		return err
	}
	if existing != "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dataDir, installerLangFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	v := strings.TrimSpace(string(b))
	if !IsSupportedLang(v) {
		return nil
	}
	return s.SetSetting(KeyDefaultLang, v)
}

// Result reports what bootstrap did. FirstRun is true only on the very first
// startup; Username/Password are non-empty only then. Host is the reachable
// address for the panel (share_host setting or auto-detected public IPv4); it
// is used to render a clickable Panel URL when the server binds to 0.0.0.0.
type Result struct {
	FirstRun  bool
	Username  string
	Password  string
	PanelPath string
	Host      string
}

// Ensure runs idempotent first-run provisioning. On subsequent runs it only
// fills in any missing settings and ensures the local node.
func Ensure(s *store.Store) (Result, error) {
	res := Result{}

	// JWT secret.
	secret, err := s.GetSetting(KeyJWTSecret)
	if err != nil {
		return res, err
	}
	if secret == "" {
		secret, err = auth.RandomHex(32)
		if err != nil {
			return res, err
		}
		if err := s.SetSetting(KeyJWTSecret, secret); err != nil {
			return res, err
		}
	}

	// Panel path.
	panelPath, err := s.GetSetting(KeyPanelPath)
	if err != nil {
		return res, err
	}
	if panelPath == "" {
		panelPath, err = auth.RandomPanelPath()
		if err != nil {
			return res, err
		}
		if err := s.SetSetting(KeyPanelPath, panelPath); err != nil {
			return res, err
		}
	}
	res.PanelPath = panelPath

	// Ensure the local node exists (Lite).
	node, err := s.EnsureLocalNode()
	if err != nil {
		return res, err
	}

	// Auto-detect this host's public IPv4 so subscription URIs render against
	// a reachable address from day one. Without this the operator has to go
	// to Settings → host and fill it manually, and any subscription created
	// before that returns 503 SHARE_HOST_UNSET.
	//
	// Only probes when nothing is set yet (node.PublicIP empty AND share_host
	// setting empty). Failure is non-fatal — the user can still set it by
	// hand from the Settings page.
	if node.PublicIP == "" {
		shareHost, _ := s.GetSetting("share_host")
		if shareHost == "" {
			if ip := DetectPublicIPv4(3 * time.Second); ip != "" {
				node.PublicIP = ip
				node.UpdatedAt = time.Now().Unix()
				if err := s.UpdateNode(node); err != nil {
					log.Printf("bootstrap: persist detected public IP: %v", err)
				}
			} else {
				log.Printf("bootstrap: public IPv4 auto-detect failed; set share_host manually in Settings")
			}
		}
	}

	// Seed share_host from the (possibly just-detected) public IP so the panel
	// UI's subscription Host field is pre-filled instead of blank on first
	// login. Only seeds when the operator has not set it yet — never overwrites
	// a user choice.
	if cur, _ := s.GetSetting("share_host"); cur == "" && node.PublicIP != "" {
		if err := s.SetSetting("share_host", node.PublicIP); err != nil {
			log.Printf("bootstrap: seed share_host from public IP: %v", err)
		}
	}

	// Resolve the host hint for the first-run banner: share_host setting wins,
	// otherwise the local node's PublicIP (which may have just been detected).
	if h, _ := s.GetSetting("share_host"); h != "" {
		res.Host = h
	} else if node.PublicIP != "" {
		res.Host = node.PublicIP
	}

	// Self-signed TLS cert: writeSelfSignedCert is idempotent so this is a
	// no-op after first boot. Without it, TLS-needing protocols (trojan, tuic,
	// hysteria2, anytls, …) fail engine-check when created ad-hoc outside the
	// wizard flow. Failure is non-fatal — operator can still issue real certs
	// from the Certs page, and the engine error surfaces in the audit log.
	//
	// SAN includes every address the user may reasonably dial: edgenest.local
	// (loopback), share_host (operator-set domain or IP), and the install-time
	// v4 / v6 addresses. One cert covers panel HTTPS regardless of which
	// address resolves on the client side — the directive being "无论用户绑定
	// 的是什么, 域名访问都应该能正常工作只要解析到的 IP 在 SAN 里".
	certPath := filepath.Join(defaultCertsDir, defaultWizardCertPN)
	keyPath := filepath.Join(defaultCertsDir, defaultWizardKeyPN)
	cap := core.ReadNodeCapability(core.DefaultCapabilityPath)
	dnsNames := []string{"edgenest.local"}
	ipAddrs := []string{}
	if res.Host != "" {
		if net.ParseIP(res.Host) != nil {
			ipAddrs = append(ipAddrs, res.Host)
		} else {
			dnsNames = append(dnsNames, res.Host)
		}
	}
	// Multi-IP per family (design): the wizard's HostChooser lets the
	// operator pick from every globally-routable IP install.sh probed. The
	// self-signed wizard cert covers ALL of them so a hy2 / trojan / WS+TLS
	// inbound on any v4 or v6 IP terminates TLS with a SAN-matching cert —
	// even though the URI builders also flip insecure=1 for self-signed
	// inbounds (belt-and-suspenders: strict clients that ignore the URI's
	// `insecure=1` still pass hostname verification).
	for _, addr := range cap.IPv4Addrs {
		if addr != "" {
			ipAddrs = append(ipAddrs, addr)
		}
	}
	for _, addr := range cap.IPv6Addrs {
		if addr != "" {
			ipAddrs = append(ipAddrs, addr)
		}
	}
	// Singular fallback for legacy network.json shapes that pre-date the
	// Addrs[] migration — keeps upgrades clean even if the new field isn't
	// yet populated.
	if len(cap.IPv4Addrs) == 0 && cap.IPv4Addr != "" {
		ipAddrs = append(ipAddrs, cap.IPv4Addr)
	}
	if len(cap.IPv6Addrs) == 0 && cap.IPv6Addr != "" {
		ipAddrs = append(ipAddrs, cap.IPv6Addr)
	}
	if err := selfsigned.WriteMultiSAN(selfsigned.Options{
		CommonName:  "edgenest.local",
		DNSNames:    dnsNames,
		IPAddresses: ipAddrs,
	}, certPath, keyPath); err != nil {
		log.Printf("bootstrap: self-signed wizard cert: %v", err)
	}

	// Admin: create on first run only.
	count, err := s.AdminCount()
	if err != nil {
		return res, err
	}
	if count == 0 {
		// Per v0.02 decision: ship a non-default initial username ("EdgeNest")
		// so default scanners that probe "admin" don't get a free attempt.
		username := "EdgeNest"
		password, err := auth.RandomHex(8) // 16-char random password
		if err != nil {
			return res, err
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			return res, err
		}
		admin := &model.Admin{
			Username:           username,
			PasswordHash:       hash,
			MustChangePassword: true,
		}
		if err := s.CreateAdmin(admin); err != nil {
			return res, err
		}
		res.FirstRun = true
		res.Username = username
		res.Password = password
	}

	return res, nil
}

// CredentialsFileName is the one-shot first-run credentials file written under
// the data dir when stdout is NOT a terminal (i.e. the service is running under
// systemd, where stdout is captured into the persistent journal). install.sh
// reads it exactly once and deletes it.
//
// Why a file instead of stdout under systemd: the plaintext password and the
// secret panel path must never reach journald. journald persists by unit name
// independent of the unit file or data dir, so anything logged there survives
// uninstall and stays readable forever via `journalctl -u edgenest`. Keeping
// the secrets out of the journal makes "forgot the password → reset is the only
// recovery" actually true, like every other panel secret.
const CredentialsFileName = "first-run.cred"

// EmitCredentials surfaces the first-run credentials without ever letting the
// plaintext password or the secret panel path reach the systemd journal.
//
//   - Interactive stdout (dev / foreground run): print the banner to the
//     terminal — ephemeral, same as the reset-password CLI.
//   - Non-interactive stdout (systemd service): write a root-only 0600 one-shot
//     file the installer consumes, and log a single non-secret line. Never fall
//     back to stdout on error — that would defeat the entire purpose.
func EmitCredentials(dataDir, listen string, r Result) {
	if !r.FirstRun {
		return
	}
	if stdoutIsTerminal() {
		printCredentialsBanner(os.Stdout, listen, r)
		return
	}
	path := filepath.Join(dataDir, CredentialsFileName)
	if err := writeCredentialsFile(path, listen, r); err != nil {
		log.Printf("first run: admin initialized but writing %s failed: %v "+
			"(run `edgenest reset-pass` to set a known password)", path, err)
		return
	}
	log.Printf("first run: admin initialized; credentials written to %s "+
		"(shown once by the installer, then deleted)", path)
}

// printCredentialsBanner renders the human-facing banner to w.
func printCredentialsBanner(w io.Writer, listen string, r Result) {
	line := "============================================================"
	fmt.Fprintln(w, line)
	fmt.Fprintln(w, " EdgeNest first run — save these credentials:")
	fmt.Fprintf(w, "   Panel URL : http://%s%s\n", panelAuthority(listen, r.Host), r.PanelPath)
	fmt.Fprintf(w, "   Username  : %s\n", r.Username)
	fmt.Fprintf(w, "   Password  : %s\n", r.Password)
	fmt.Fprintln(w, "   (You will be asked to change the password on first login.)")
	fmt.Fprintln(w, line)
}

// writeCredentialsFile writes the installer-consumed key=value one-shot file.
// It removes any stale file first and creates with O_EXCL|0600 so the secrets
// never land in a world-readable or inherited-permission file.
func writeCredentialsFile(path, listen string, r Result) error {
	_ = os.Remove(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "PANEL_URL=http://%s%s\n", panelAuthority(listen, r.Host), r.PanelPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "PANEL_PATH=%s\n", strings.TrimPrefix(r.PanelPath, "/")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "USERNAME=%s\n", r.Username); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "PASSWORD=%s\n", r.Password); err != nil {
		return err
	}
	return f.Close()
}

// stdoutIsTerminal reports whether stdout is a character device (a TTY). Under
// systemd stdout is a pipe to the journal, so this is false and we take the
// file path; a developer running the binary in a shell gets the printed banner.
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// panelAuthority returns the host:port to put in the Panel URL banner.
// listen is the bind string (e.g. "0.0.0.0:1123", ":1123", "[::]:1123",
// "127.0.0.1:1123"). When the host portion is a wildcard the URL it produces
// is not reachable from a browser, so we substitute the detected public host;
// when no host is known we fall back to a "<your-server-ip>" placeholder so
// the operator notices instead of copy-pasting 0.0.0.0.
func panelAuthority(listen, host string) string {
	bindHost, port := splitListen(listen)
	if isWildcardHost(bindHost) {
		if host != "" {
			return net.JoinHostPort(host, port)
		}
		return net.JoinHostPort("<your-server-ip>", port)
	}
	return listen
}

func splitListen(listen string) (host, port string) {
	if listen == "" {
		return "", ""
	}
	if strings.HasPrefix(listen, ":") {
		return "", strings.TrimPrefix(listen, ":")
	}
	h, p, err := net.SplitHostPort(listen)
	if err != nil {
		return listen, ""
	}
	return h, p
}

func isWildcardHost(h string) bool {
	switch h {
	case "", "0.0.0.0", "::", "[::]":
		return true
	}
	return false
}
