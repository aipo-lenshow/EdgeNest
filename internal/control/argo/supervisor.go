package argo

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

// State is the lifecycle marker for a tunnel. The UI surfaces this so the
// operator can tell whether a tunnel is actually exposing traffic.
type State string

const (
	StateIdle     State = "idle"     // no tunnel configured / not started
	StateStarting State = "starting" // process running, hostname not yet captured
	StateRunning  State = "running"  // process running, hostname captured (temp) or assumed (named)
	StateFailed   State = "failed"   // process exited or never came up; see Error
)

// Mode selects how the tunnel is provisioned.
type Mode string

const (
	ModeTemp  Mode = "temp"  // free trycloudflare.com subdomain, no operator setup
	ModeNamed Mode = "named" // operator's own domain + tunnel token
)

// Status is a snapshot of the supervisor's current state, safe to copy.
type Status struct {
	State    State     `json:"state"`
	Mode     Mode      `json:"mode"`
	Hostname string    `json:"hostname"` // captured (temp) or operator-supplied (named)
	Error    string    `json:"error,omitempty"`
	Since    time.Time `json:"since"`
}

// trycfRegex captures the trycloudflare.com hostname cloudflared prints to
// stdout in temp mode. cloudflared's banner shape is stable since 2022
// (`https://<random>-<random>-<random>.trycloudflare.com`) and the regex
// tolerates ANSI / box-drawing noise around it.
var trycfRegex = regexp.MustCompile(`https?://([a-z0-9-]+\.trycloudflare\.com)`)

// Supervisor runs at most one cloudflared process and exposes its state.
// All methods are safe to call from multiple goroutines.
type Supervisor struct {
	bin string

	mu     sync.Mutex
	status Status
	cancel context.CancelFunc
	cmd    *exec.Cmd
	done   chan struct{}
}

// NewSupervisor returns a supervisor that will exec `bin` (typically the
// result of BinaryManager.Path). Status() before the first Start* call
// reports StateIdle.
func NewSupervisor(bin string) *Supervisor {
	return &Supervisor{
		bin:    bin,
		status: Status{State: StateIdle, Since: time.Now()},
	}
}

// Status returns a copy of the current state.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// KillStray terminates any cloudflared tunnel processes left over from a
// previous EdgeNest run. The supervisor's process handle lives only in memory,
// so a restart (deploy, crash, reboot) loses track of a running tunnel: the
// child is reparented to init and keeps piping traffic to the loopback origin
// while the fresh supervisor reports Idle — a desync where the panel shows
// "stopped" but a tunnel is live, and the next Start spawns a SECOND cloudflared.
// Calling this once at startup makes the reported state truthful and guarantees
// at most one cloudflared. Best-effort: a missing pkill or no match is fine.
//
// WARP and CDN need no equivalent — both are config-only (regenerated into the
// sing-box config from the DB on startup), with no external daemon to orphan.
func KillStray() {
	// Match ONLY our managed cloudflared binary path, never a generic
	// "cloudflared tunnel": an operator may run their own cloudflared (from a
	// different path, e.g. /usr/local/bin) for an unrelated service, and a blanket
	// pattern would kill it too. The managed copy always lives at DefaultBinDir,
	// which never collides with an operator's own install.
	bin := filepath.Join(DefaultBinDir, "cloudflared")
	if exec.Command("pgrep", "-f", bin).Run() != nil {
		return // nothing of ours is running — no work, and no startup delay below
	}
	// SIGTERM first so the tunnel deregisters cleanly from Cloudflare's edge
	// (avoids a stale connector and the abrupt "signal: killed"); then SIGKILL
	// any child that didn't exit within the grace window. Best-effort throughout.
	_ = exec.Command("pkill", "-TERM", "-f", bin).Run()
	time.Sleep(2 * time.Second)
	_ = exec.Command("pkill", "-KILL", "-f", bin).Run()
}

// StartTemp launches a quick-tunnel against http://127.0.0.1:<localPort>,
// returning once the trycloudflare hostname has been captured from stdout
// or the captureTimeout elapses. The captureTimeout protects against
// cloudflared launching but never printing a hostname (network failure,
// banned IP, etc.) — operators get a clear "started but hostname not yet
// printed" state rather than a hung Start call.
//
// Idempotent: stops any existing tunnel before starting a new one so the
// supervisor never runs two cloudflared processes.
func (s *Supervisor) StartTemp(ctx context.Context, localPort int, captureTimeout time.Duration) error {
	s.Stop()
	url := fmt.Sprintf("http://127.0.0.1:%d", localPort)
	return s.start(ctx, ModeTemp, "", []string{
		"tunnel",
		"--no-autoupdate",
		"--url", url,
	}, captureTimeout)
}

// StartNamed launches a tunnel for the operator's pre-provisioned domain.
// hostname is what cloudflared has been told to route (the operator
// configures this in the Cloudflare Zero Trust dashboard alongside the
// token) — we accept it verbatim so the UI can render the share host
// without re-querying Cloudflare.
//
// captureTimeout has a different meaning here: named tunnels don't print a
// "your tunnel is at <X>" banner; the supervisor flips StateRunning when
// the process has been alive for at least 2 seconds without exiting,
// otherwise StateFailed (token rejected, etc.).
func (s *Supervisor) StartNamed(ctx context.Context, token, hostname string, captureTimeout time.Duration) error {
	s.Stop()
	if token == "" {
		return fmt.Errorf("argo: named tunnel requires a token")
	}
	if hostname == "" {
		return fmt.Errorf("argo: named tunnel requires a hostname")
	}
	return s.start(ctx, ModeNamed, hostname, []string{
		"tunnel",
		"--no-autoupdate",
		"run",
		"--token", token,
	}, captureTimeout)
}

// Stop terminates the running tunnel (if any) and resets state to Idle.
// Blocks until cloudflared exits or 5 seconds elapses.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			// fall through; process may be unresponsive — best-effort kill
		}
	}

	s.mu.Lock()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	}
	s.cancel = nil
	s.cmd = nil
	s.done = nil
	s.status = Status{State: StateIdle, Since: time.Now()}
	s.mu.Unlock()
}

// start is the shared launch path. modeHost is the hostname for named mode
// (empty for temp; captured from stdout instead).
//
// The cloudflared process MUST outlive the HTTP request that started it. We
// deliberately do NOT derive its context from `parent` (the request context):
// once ArgoStart returns, gin cancels the request context (and its own
// WithTimeout/defer cancel fires), which would SIGKILL cloudflared the instant
// the tunnel reached "running" — the classic "signal: killed" right after the
// hostname is captured. Instead the process lifetime is owned by the supervisor
// and ends only when Stop() (or the OS) terminates it. `parent` is intentionally
// unused for the process; it could still bound the startup wait, but the wait is
// already bounded by captureTimeout.
func (s *Supervisor) start(parent context.Context, mode Mode, modeHost string, args []string, captureTimeout time.Duration) error {
	_ = parent // see doc comment: process lifetime is supervisor-owned, not request-bound
	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, s.bin, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("argo: stderr pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("argo: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("argo: start cloudflared: %w", err)
	}

	done := make(chan struct{})
	captured := make(chan string, 1)

	// Scan both stdout and stderr; cloudflared in temp mode tends to print
	// the banner via stderr but versions drift.
	go scanForHostname(stdout, captured)
	go scanForHostname(stderr, captured)

	s.mu.Lock()
	s.cancel = cancel
	s.cmd = cmd
	s.done = done
	s.status = Status{State: StateStarting, Mode: mode, Hostname: modeHost, Since: time.Now()}
	s.mu.Unlock()

	// Reaper goroutine: waits for the process to exit and updates state.
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		defer s.mu.Unlock()
		// Only mark failed if Stop() wasn't the one that cancelled — Stop()
		// sets status back to Idle on its own.
		if s.cmd == cmd {
			s.status.State = StateFailed
			if err != nil {
				s.status.Error = err.Error()
			} else {
				s.status.Error = "cloudflared exited"
			}
			s.status.Since = time.Now()
		}
		close(done)
	}()

	// Wait for either the hostname (temp) or a 2s alive grace period (named).
	deadline := time.NewTimer(captureTimeout)
	defer deadline.Stop()

	switch mode {
	case ModeTemp:
		select {
		case host := <-captured:
			s.mu.Lock()
			if s.cmd == cmd {
				s.status.State = StateRunning
				s.status.Hostname = host
			}
			s.mu.Unlock()
			return nil
		case <-deadline.C:
			// Don't kill — process may still come up; surface as Starting.
			return fmt.Errorf("argo: timed out waiting for trycloudflare hostname after %s", captureTimeout)
		case <-done:
			return fmt.Errorf("argo: cloudflared exited before printing a hostname")
		}
	case ModeNamed:
		select {
		case <-time.After(2 * time.Second):
			s.mu.Lock()
			if s.cmd == cmd && s.status.State == StateStarting {
				s.status.State = StateRunning
			}
			s.mu.Unlock()
			return nil
		case <-done:
			return fmt.Errorf("argo: cloudflared exited (likely invalid token)")
		}
	}
	return nil
}

// scanForHostname reads lines from r and sends the first trycloudflare match
// into captured. Subsequent matches are dropped (channel has capacity 1).
func scanForHostname(r io.Reader, captured chan<- string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if m := trycfRegex.FindStringSubmatch(line); m != nil {
			select {
			case captured <- m[1]:
			default:
				// already captured
			}
		}
	}
}
