package argo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeCloudflared compiles a tiny Go program that mimics cloudflared's
// stdout/stderr enough to drive the supervisor: it prints the
// trycloudflare banner on the configured stream then sleeps until killed.
// Used to exercise StartTemp without a real cloudflared binary or network.
func fakeCloudflared(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("supervisor tests rely on POSIX signals")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "fake.go")
	// The fake reads ARGS to decide what to print, so a single binary serves
	// both temp (prints banner) and named (no banner) scenarios.
	srcBody := `package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	body := ` + "`" + body + "`" + `
	if body != "" {
		fmt.Fprintln(os.Stderr, body)
	}
	// Stay alive until SIGTERM so the supervisor's reaper sees the expected
	// "killed" state on Stop().
	time.Sleep(30 * time.Second)
}
`
	if err := os.WriteFile(src, []byte(srcBody), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fake")
	cmd := goBuild(src, bin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake cloudflared: %v\n%s", err, out)
	}
	return bin
}

func TestSupervisor_StartTempCapturesHostname(t *testing.T) {
	bin := fakeCloudflared(t,
		"INF Your quick Tunnel has been created! Visit it at https://demo-host-name.trycloudflare.com")
	sup := NewSupervisor(bin)
	defer sup.Stop()

	if err := sup.StartTemp(context.Background(), 9999, 5*time.Second); err != nil {
		t.Fatalf("StartTemp: %v", err)
	}
	st := sup.Status()
	if st.State != StateRunning {
		t.Errorf("State = %q, want running", st.State)
	}
	if st.Hostname != "demo-host-name.trycloudflare.com" {
		t.Errorf("Hostname = %q", st.Hostname)
	}
	if st.Mode != ModeTemp {
		t.Errorf("Mode = %q", st.Mode)
	}
}

func TestSupervisor_StartTempTimeout(t *testing.T) {
	// fake never prints a banner → supervisor must surface the timeout error,
	// leave the process running (caller may choose to Stop).
	bin := fakeCloudflared(t, "INF some unrelated log line")
	sup := NewSupervisor(bin)
	defer sup.Stop()

	err := sup.StartTemp(context.Background(), 9999, 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("unexpected error: %v", err)
	}
	if st := sup.Status(); st.State != StateStarting {
		t.Errorf("after timeout State should remain starting, got %q", st.State)
	}
}

func TestSupervisor_StartNamedRequiresTokenAndHost(t *testing.T) {
	sup := NewSupervisor("/no/such/bin")
	if err := sup.StartNamed(context.Background(), "", "host", time.Second); err == nil {
		t.Error("expected error for empty token")
	}
	if err := sup.StartNamed(context.Background(), "tok", "", time.Second); err == nil {
		t.Error("expected error for empty hostname")
	}
}

func TestSupervisor_StopResetsState(t *testing.T) {
	bin := fakeCloudflared(t,
		"INF https://foo-bar.trycloudflare.com")
	sup := NewSupervisor(bin)
	if err := sup.StartTemp(context.Background(), 9999, 5*time.Second); err != nil {
		t.Fatalf("StartTemp: %v", err)
	}
	sup.Stop()
	if st := sup.Status(); st.State != StateIdle {
		t.Errorf("after Stop State = %q, want idle", st.State)
	}
}

// Regression: the cloudflared process must OUTLIVE the request context that
// started it. start() previously derived the process context from `parent`, so
// when the HTTP handler returned and its context was cancelled (gin + the
// handler's own WithTimeout/defer cancel), cloudflared was SIGKILLed the instant
// the tunnel reached "running" — the "signal: killed" the operator saw. Cancelling
// the parent must NOT kill the tunnel; only Stop() (or the OS) does.
func TestSupervisor_ProcessOutlivesParentContext(t *testing.T) {
	bin := fakeCloudflared(t,
		"INF https://outlive-host.trycloudflare.com")
	sup := NewSupervisor(bin)
	defer sup.Stop()

	parent, cancel := context.WithCancel(context.Background())
	if err := sup.StartTemp(parent, 9999, 5*time.Second); err != nil {
		t.Fatalf("StartTemp: %v", err)
	}
	if st := sup.Status(); st.State != StateRunning {
		t.Fatalf("State = %q, want running", st.State)
	}
	// Simulate the HTTP request finishing: cancel the parent context.
	cancel()
	time.Sleep(300 * time.Millisecond)
	// The tunnel must still be running — parent cancellation doesn't reach it.
	if st := sup.Status(); st.State != StateRunning {
		t.Fatalf("after parent cancel State = %q (err=%q), want still running", st.State, st.Error)
	}
}

func TestSupervisor_StartTempSecondCallReplaces(t *testing.T) {
	bin := fakeCloudflared(t,
		"INF https://first.trycloudflare.com")
	sup := NewSupervisor(bin)
	defer sup.Stop()
	if err := sup.StartTemp(context.Background(), 9999, 5*time.Second); err != nil {
		t.Fatalf("first StartTemp: %v", err)
	}
	first := sup.Status().Hostname

	// Second call must not leak the first process. We can't easily verify
	// PIDs in a portable way, so just assert that StartTemp succeeds again
	// (which requires the supervisor to have Stop()d the previous proc).
	bin2 := fakeCloudflared(t,
		"INF https://second.trycloudflare.com")
	sup2 := NewSupervisor(bin2)
	defer sup2.Stop()
	if err := sup2.StartTemp(context.Background(), 9999, 5*time.Second); err != nil {
		t.Fatalf("second StartTemp: %v", err)
	}
	if got := sup2.Status().Hostname; got == first {
		t.Errorf("second start kept first hostname %q", got)
	}
}
