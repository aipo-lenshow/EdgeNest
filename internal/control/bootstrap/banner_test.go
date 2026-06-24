package bootstrap

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPanelAuthority(t *testing.T) {
	cases := []struct {
		name   string
		listen string
		host   string
		want   string
	}{
		{"wildcard with detected host", "0.0.0.0:1123", "203.0.113.7", "203.0.113.7:1123"},
		{"wildcard, no host known", "0.0.0.0:1123", "", "<your-server-ip>:1123"},
		{"ipv6 wildcard with host", "[::]:1123", "203.0.113.7", "203.0.113.7:1123"},
		{"port-only with host", ":1123", "203.0.113.7", "203.0.113.7:1123"},
		{"explicit loopback kept as-is", "127.0.0.1:1123", "203.0.113.7", "127.0.0.1:1123"},
		{"explicit ipv4 kept as-is", "203.0.113.7:1123", "", "203.0.113.7:1123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := panelAuthority(tc.listen, tc.host)
			if got != tc.want {
				t.Errorf("panelAuthority(%q, %q) = %q, want %q", tc.listen, tc.host, got, tc.want)
			}
		})
	}
}

func sampleResult() Result {
	return Result{
		FirstRun:  true,
		Username:  "EdgeNest",
		Password:  "007faa983373c6ad",
		PanelPath: "/ENPanel-e30cee56",
		Host:      "203.0.113.7",
	}
}

// The one-shot credentials file must be the ONLY place the plaintext password
// and the secret panel path land under systemd — never the journal. Lock its
// shape, its 0600 permissions, and the no-leading-slash panel path the
// installer's URL builder expects.
func TestWriteCredentialsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CredentialsFileName)
	if err := writeCredentialsFile(path, "0.0.0.0:1123", sampleResult()); err != nil {
		t.Fatalf("writeCredentialsFile: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("cred file perm = %o, want 600", perm)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		"PANEL_URL=http://203.0.113.7:1123/ENPanel-e30cee56\n",
		"PANEL_PATH=ENPanel-e30cee56\n", // no leading slash: installer builds .../${PANEL_PATH}
		"USERNAME=EdgeNest\n",
		"PASSWORD=007faa983373c6ad\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("cred file missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// writeCredentialsFile must never inherit a stale file's permissions: it
// removes any pre-existing file and recreates it 0600.
func TestWriteCredentialsFileReplacesStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CredentialsFileName)
	if err := os.WriteFile(path, []byte("STALE=1\n"), 0o644); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	if err := writeCredentialsFile(path, "0.0.0.0:1123", sampleResult()); err != nil {
		t.Fatalf("writeCredentialsFile: %v", err)
	}
	fi, _ := os.Stat(path)
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("stale file not re-permed: %o, want 600", perm)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "STALE") {
		t.Errorf("stale content survived: %s", b)
	}
}

// EmitCredentials is a no-op when it isn't a first run — no file, no output.
func TestEmitCredentialsNotFirstRun(t *testing.T) {
	dir := t.TempDir()
	EmitCredentials(dir, "0.0.0.0:1123", Result{FirstRun: false})
	if _, err := os.Stat(filepath.Join(dir, CredentialsFileName)); !os.IsNotExist(err) {
		t.Errorf("expected no cred file on non-first-run, stat err = %v", err)
	}
}

// printCredentialsBanner is the TTY-only path; verify it renders the secrets
// for a human (this output goes to a terminal, never to journald).
func TestPrintCredentialsBanner(t *testing.T) {
	var buf bytes.Buffer
	printCredentialsBanner(&buf, "0.0.0.0:1123", sampleResult())
	out := buf.String()
	for _, want := range []string{"203.0.113.7:1123/ENPanel-e30cee56", "EdgeNest", "007faa983373c6ad"} {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing %q\n%s", want, out)
		}
	}
}
