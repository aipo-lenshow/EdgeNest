// Package logredact masks IP addresses in the proxy engines' log output before
// it hits disk. It backs the panel's "don't log client IP" privacy toggle.
//
// Why a write-path filter instead of lowering the sing-box log level: real
// captures showed the client's source IP still leaks at ERROR level (REALITY
// "process connection from <ip>: ... invalid connection" handshake failures),
// so no single log level removes every IP without also silencing the warnings
// and errors an operator needs. Filtering the stream removes IPs at every level
// while keeping full log fidelity, and it never touches sing-box.json — so it
// can't drift the rendered-config baseline.
//
// The engines set their process stdout/stderr to Writer(logFile); the writer
// consults a process-global atomic flag on every write, so toggling the panel
// switch takes effect on the live log stream with no engine restart.
package logredact

import (
	"io"
	"regexp"
	"sync/atomic"
)

// placeholder replaces every matched address. Kept short and obviously-not-an-IP
// so redacted logs stay readable ("process connection from [ip]: ...").
const placeholder = "[ip]"

var enabled atomic.Bool

// SetEnabled turns redaction on or off for all wrapped writers. Called at
// startup from the persisted setting and whenever the panel toggle changes.
func SetEnabled(on bool) { enabled.Store(on) }

// Enabled reports the current state.
func Enabled() bool { return enabled.Load() }

var (
	// IPv4: four dotted octets, each 0-255, on word boundaries. The octet bound
	// keeps it from eating version strings / connection IDs (those aren't four
	// 0-255 dotted groups).
	reIPv4 = regexp.MustCompile(`\b(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])(\.(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])){3}\b`)
	// IPv6: the full RFC-4291 form set (compressed :: included). Long but exact,
	// so it won't match HH:MM:SS timestamps (single-colon, no :: and not 8 groups)
	// or ANSI escapes (semicolon-separated).
	reIPv6 = regexp.MustCompile(`(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}|(?:[0-9a-fA-F]{1,4}:){1,7}:|(?:[0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|(?:[0-9a-fA-F]{1,4}:){1,5}(?::[0-9a-fA-F]{1,4}){1,2}|(?:[0-9a-fA-F]{1,4}:){1,4}(?::[0-9a-fA-F]{1,4}){1,3}|(?:[0-9a-fA-F]{1,4}:){1,3}(?::[0-9a-fA-F]{1,4}){1,4}|(?:[0-9a-fA-F]{1,4}:){1,2}(?::[0-9a-fA-F]{1,4}){1,5}|[0-9a-fA-F]{1,4}:(?::[0-9a-fA-F]{1,4}){1,6}|:(?:(?::[0-9a-fA-F]{1,4}){1,7}|:)`)
)

// Redact replaces every IPv4/IPv6 literal in b with the placeholder. IPv6 first
// so its hex groups aren't partially consumed by the IPv4 pass.
func Redact(b []byte) []byte {
	b = reIPv6.ReplaceAll(b, []byte(placeholder))
	b = reIPv4.ReplaceAll(b, []byte(placeholder))
	return b
}

// Writer wraps w so that, while Enabled(), IP addresses are masked before being
// written. When disabled it's a transparent pass-through (zero overhead beyond
// one atomic load). sing-box / xray loggers emit one full line per Write, so
// redacting per write is safe — an address never straddles two writes.
func Writer(w io.Writer) io.Writer { return &writer{w: w} }

type writer struct{ w io.Writer }

func (rw *writer) Write(p []byte) (int, error) {
	if !enabled.Load() {
		return rw.w.Write(p)
	}
	// Report len(p) as written (not len after redaction) so callers — including
	// os/exec's stdout pump — see a complete, non-short write.
	if _, err := rw.w.Write(Redact(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}
