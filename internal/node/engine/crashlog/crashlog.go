// Package crashlog keeps the last few KB an engine subprocess printed, in
// memory only, so the reason it died can be shown in the panel instead of
// just "stopped". The log file on disk is untouched.
package crashlog

import (
	"regexp"
	"strings"
	"sync"

	"github.com/aipo-lenshow/EdgeNest/internal/logredact"
)

const keep = 8 << 10

// Tail is an io.Writer that remembers the last 8 KB written to it.
type Tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *Tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > keep {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-keep:]...)
	}
	t.mu.Unlock()
	return len(p), nil
}

var (
	ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	// sing-box: "+0000 2026-10-30 18:11:33 FATAL[0000] start service: ..."
	// xray:     "2026/10/30 18:11:33 Failed to start: main: ..."
	fatalPrefix = regexp.MustCompile(`^.*?(FATAL\[\d+\]\s*|Failed to start:\s*|panic:\s*)`)
)

// Reason picks the line that explains an exit: the last fatal line if there
// is one (startup and config failures — they name the server's own
// addresses, so they are shown as printed), otherwise the last non-empty
// line, which may concern a client connection and so is redacted when the
// operator has client-IP redaction on.
func (t *Tail) Reason() string {
	t.mu.Lock()
	text := ansi.ReplaceAllString(string(t.buf), "")
	t.mu.Unlock()

	lines := strings.Split(text, "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if loc := fatalPrefix.FindStringSubmatchIndex(l); loc != nil {
			return clip(l[loc[1]:])
		}
		if last == "" {
			last = l
		}
	}
	if logredact.Enabled() {
		last = string(logredact.Redact([]byte(last)))
	}
	return clip(last)
}

func clip(s string) string {
	const max = 400
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
