package singbox

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/logredact"
)

const (
	// verifyDelay is how long Apply waits before checking that the new
	// subprocess didn't crash on startup. 2s is enough for sing-box to bind
	// its inbounds and either fail loudly or settle.
	verifyDelay = 2 * time.Second

	// stopGracePeriod is how long Stop waits for SIGTERM to take effect
	// before sending SIGKILL.
	stopGracePeriod = 5 * time.Second

	// supervisorBackoffMin / Max bound the crash-watch restart backoff.
	supervisorBackoffMin = 1 * time.Second
	supervisorBackoffMax = 30 * time.Second
)

// process wraps an os/exec.Cmd plus log routing. One per running sing-box.
type process struct {
	cmd      *exec.Cmd
	logFile  *os.File
	stopOnce sync.Once
	done     chan struct{} // closed when Wait returns
	waitErr  error
}

// runCheck runs `sing-box check -c <path>` and returns its combined output.
func (e *Engine) runCheck(path string) (string, error) {
	cmd := exec.Command(e.binPath, "check", "-c", path)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// detectVersion shells out to `sing-box version` once and parses the first
// "version v1.10.7" line (sing-box prints "sing-box version v1.10.7" or
// "version: 1.10.7" depending on build).
func detectVersion(bin string) (string, error) {
	cmd := exec.Command(bin, "version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return parseVersion(string(out))
}

var versionRe = regexp.MustCompile(`v?(\d+\.\d+\.\d+)`)

func parseVersion(out string) (string, error) {
	m := versionRe.FindStringSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("could not parse version from: %q", strings.TrimSpace(out))
	}
	return m[1], nil
}

// versionMatchesPin accepts the installed version if its MAJOR.MINOR matches
// the pin. PATCH is allowed to differ so security patches don't break us.
func versionMatchesPin(got, pin string) bool {
	gp := strings.Split(got, ".")
	pp := strings.Split(pin, ".")
	if len(gp) < 2 || len(pp) < 2 {
		return false
	}
	return gp[0] == pp[0] && gp[1] == pp[1]
}

// restartLocked stops any running subprocess and starts a fresh one using
// e.configPath. Caller MUST hold e.mu.
func (e *Engine) restartLocked() error {
	_ = e.stopLocked() // ignore errors; we're going to start anew
	return e.startLocked()
}

// startLocked starts a sing-box subprocess. Caller MUST hold e.mu.
func (e *Engine) startLocked() error {
	if _, err := os.Stat(e.configPath); err != nil {
		return fmt.Errorf("config missing: %s", e.configPath)
	}

	// Best-effort log file.
	_ = os.MkdirAll(e.logDir, 0o750)
	logPath := filepath.Join(e.logDir, "sing-box.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		// Logging is non-fatal; fall back to discarding output.
		logFile = nil
	}

	cmd := exec.Command(e.binPath, "run", "-c", e.configPath)
	// Run in its own process group so we can SIGTERM the whole tree on stop.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if logFile != nil {
		// Same wrapper instance for stdout+stderr so os/exec funnels both through
		// one goroutine (no concurrent Write to logFile). The wrapper masks IPs
		// when the privacy toggle is on; transparent otherwise.
		lw := logredact.Writer(logFile)
		cmd.Stdout = lw
		cmd.Stderr = lw
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	}
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return fmt.Errorf("start sing-box: %w", err)
	}

	p := &process{cmd: cmd, logFile: logFile, done: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()

	e.proc = p
	e.startedAt = time.Now()

	// First start launches the supervisor goroutine (one per Engine lifetime).
	if e.supervisorStop == nil {
		e.supervisorStop = make(chan struct{})
		go e.supervise()
	}
	return nil
}

// stopLocked terminates the running subprocess (SIGTERM → grace → SIGKILL).
// Caller MUST hold e.mu. Idempotent: safe to call when not running.
func (e *Engine) stopLocked() error {
	p := e.proc
	if p == nil {
		return nil
	}
	if !e.aliveLocked() {
		e.proc = nil
		return nil
	}
	var sigErr error
	p.stopOnce.Do(func() {
		// Negative PID = process group (because we Setpgid above).
		sigErr = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	})
	select {
	case <-p.done:
	case <-time.After(stopGracePeriod):
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	}
	if p.logFile != nil {
		_ = p.logFile.Close()
	}
	e.proc = nil
	if sigErr != nil && !errors.Is(sigErr, syscall.ESRCH) {
		return fmt.Errorf("signal: %w", sigErr)
	}
	return nil
}

// aliveLocked reports whether the recorded subprocess is still running.
// Caller MUST hold e.mu.
func (e *Engine) aliveLocked() bool {
	p := e.proc
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// rollbackLocked restores from a backup file then restarts. Caller MUST hold e.mu.
func (e *Engine) rollbackLocked(bakPath string) error {
	if _, err := os.Stat(bakPath); err != nil {
		return fmt.Errorf("backup missing at %s: %w", bakPath, err)
	}
	if err := copyFile(bakPath, e.configPath); err != nil {
		return fmt.Errorf("restore backup: %w", err)
	}
	return e.restartLocked()
}

// supervise watches the current subprocess and restarts it on unexpected exit
// using exponential backoff. Exits when Stop is called (e.supervisorStop closed)
// or the engine is reaped.
func (e *Engine) supervise() {
	backoff := supervisorBackoffMin
	for {
		e.mu.Lock()
		p := e.proc
		stopCh := e.supervisorStop
		e.mu.Unlock()
		if p == nil {
			// We were stopped externally; wait for either a new start or shutdown.
			select {
			case <-stopCh:
				return
			case <-time.After(supervisorBackoffMin):
				continue
			}
		}

		select {
		case <-p.done:
			// Subprocess exited. Was it intentional?
			e.mu.Lock()
			intentional := e.proc != p // someone replaced/cleared it under us
			e.mu.Unlock()
			if intentional {
				// Reset backoff; next iteration picks up the new state.
				backoff = supervisorBackoffMin
				continue
			}
			// Crash. Sleep then restart.
			time.Sleep(backoff)
			e.mu.Lock()
			if e.proc != p { // raced with an external restart
				e.mu.Unlock()
				backoff = supervisorBackoffMin
				continue
			}
			if err := e.startLocked(); err != nil {
				// Failed to restart; double backoff and try again.
				e.mu.Unlock()
				if backoff*2 < supervisorBackoffMax {
					backoff *= 2
				} else {
					backoff = supervisorBackoffMax
				}
				continue
			}
			e.mu.Unlock()
			backoff = supervisorBackoffMin
		case <-stopCh:
			return
		}
	}
}
