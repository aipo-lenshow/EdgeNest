package logredact

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedact_IPv4(t *testing.T) {
	// The exact leak shape seen in real captures: a REALITY handshake ERROR that
	// carried the client's source IP at a level `warn` could not suppress. The IP
	// here is a documentation-range address (RFC 5737), not a real one.
	in := "ERROR inbound/vless[Reality-v4-8443]: process connection from 203.0.113.45:53363: TLS handshake: REALITY: processed invalid connection"
	out := string(Redact([]byte(in)))
	if strings.Contains(out, "203.0.113.45") {
		t.Fatalf("client IP not redacted: %s", out)
	}
	if !strings.Contains(out, "[ip]:53363") {
		t.Errorf("want [ip] placeholder keeping the port, got: %s", out)
	}
}

func TestRedact_IPv6(t *testing.T) {
	for _, ip := range []string{
		"2606:4700:d0::a29f:c001",
		"2001:db8:85a3::8a2e:370:7334",
		"fe80::1",
	} {
		out := string(Redact([]byte("lookup succeed: " + ip)))
		if strings.Contains(out, ip) {
			t.Errorf("IPv6 %q not redacted: %s", ip, out)
		}
	}
}

// Guard against eating non-IP tokens that share digits/colons with addresses.
func TestRedact_NoFalsePositives(t *testing.T) {
	keep := []string{
		"2026-06-20 07:24:00",                 // timestamp (single-colon HH:MM:SS)
		"[238738731 7ms]",                     // sing-box connection id + duration
		"sing-box 1.13.13 started",            // version string (only 3 dotted groups)
		"\x1b[38;5;188m",                      // ANSI color escape (semicolons)
		"EdgeNest-VLESS-Reality-v4-8443",      // inbound tag
	}
	for _, s := range keep {
		if got := string(Redact([]byte(s))); got != s {
			t.Errorf("false positive: %q became %q", s, got)
		}
	}
}

func TestWriter_GateAndShortWrite(t *testing.T) {
	var buf bytes.Buffer
	w := Writer(&buf)
	line := []byte("from 203.0.113.7 done\n")

	// Disabled: pass-through verbatim.
	SetEnabled(false)
	n, err := w.Write(line)
	if err != nil || n != len(line) {
		t.Fatalf("disabled write: n=%d err=%v want n=%d", n, err, len(line))
	}
	if buf.String() != string(line) {
		t.Errorf("disabled must pass through, got %q", buf.String())
	}

	// Enabled: redacts, but still reports the full input length (no short write
	// — os/exec's output pump treats a short write as an error).
	buf.Reset()
	SetEnabled(true)
	n, err = w.Write(line)
	if err != nil || n != len(line) {
		t.Fatalf("enabled write: n=%d err=%v want n=%d", n, err, len(line))
	}
	if strings.Contains(buf.String(), "203.0.113.7") {
		t.Errorf("enabled must redact, got %q", buf.String())
	}
	SetEnabled(false) // reset global for other tests
}
