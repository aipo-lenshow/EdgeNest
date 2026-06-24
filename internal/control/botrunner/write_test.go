package botrunner

import (
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"unlimited", 0, true},
		{"无限", 0, true},
		{"10GB", 10 << 30, true},
		{"10gb", 10 << 30, true},
		{"10g", 10 << 30, true},
		{"500MB", 500 << 20, true},
		{"1.5TB", int64(1.5 * (1 << 40)), true},
		{"2048", 2048, true}, // bare number = bytes
		{"abc", 0, false},
		{"10XB", 0, false},
		{"", 0, true},
	}
	for _, c := range cases {
		got, ok := parseSize(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseSize(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseDays(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"30", 30, true},
		{"+30", 30, true},
		{"30days", 30, true},
		{"30天", 30, true},
		{"-1", -1, true},
		{"x", 0, false},
	}
	for _, c := range cases {
		got, ok := parseDays(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseDays(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseExpiry(t *testing.T) {
	loc := time.UTC

	if v, ok := parseExpiry("0", loc); !ok || v != 0 {
		t.Errorf("parseExpiry(0) = (%d,%v), want (0,true)", v, ok)
	}
	if v, ok := parseExpiry("never", loc); !ok || v != 0 {
		t.Errorf("parseExpiry(never) = (%d,%v), want (0,true)", v, ok)
	}

	// Absolute date → end of that day in loc.
	v, ok := parseExpiry("2026-07-01", loc)
	if !ok {
		t.Fatal("parseExpiry(2026-07-01) not ok")
	}
	want := time.Date(2026, 7, 1, 23, 59, 59, 0, loc).Unix()
	if v != want {
		t.Errorf("parseExpiry date = %d, want %d", v, want)
	}

	// +N days → strictly in the future, end-of-day (never collapses to 0/never).
	future, ok := parseExpiry("+30", loc)
	if !ok || future <= time.Now().Unix() {
		t.Errorf("parseExpiry(+30) = (%d,%v), want a future ts", future, ok)
	}

	if _, ok := parseExpiry("garbage", loc); ok {
		t.Error("parseExpiry(garbage) should fail")
	}
}

func TestCanonicalCmd_WriteCommands(t *testing.T) {
	cases := map[string]string{
		"/enable":  "enable",
		"/启用":      "enable",
		"/disable": "disable",
		"/禁用":      "disable",
		"/quota":   "quota",
		"/配额":      "quota",
		"/expire":  "expire",
		"/期限":      "expire",
		"/reset":   "reset",
		"/enforce": "enforce",
		"/create":  "create",
		"/delete":  "delete",
		"/删除":      "delete",
	}
	for tok, want := range cases {
		if got := canonicalCmd(tok, ""); got != want {
			t.Errorf("canonicalCmd(%q) = %q, want %q", tok, got, want)
		}
	}
}
