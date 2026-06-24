package xray

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
	verifyDelay     = 2 * time.Second
	stopGracePeriod = 5 * time.Second

	supervisorBackoffMin = 1 * time.Second
	supervisorBackoffMax = 30 * time.Second
)

type process struct {
	cmd      *exec.Cmd
	logFile  *os.File
	stopOnce sync.Once
	done     chan struct{}
	waitErr  error
}

// runCheck runs `xray -test -config <path> -format json` and returns combined
// output. Xray's classic CLI accepts -test/-config (single-dash) since v1.x.
// `-format json` is explicit because the staging path is `xray.json.new`
// (atomic-swap pattern), which xray v26 no longer auto-detects from the
// extension — it defaults to format="auto" but only matches `.json` / `.toml`
// / `.yaml` suffixes.
func (e *Engine) runCheck(path string) (string, error) {
	cmd := exec.Command(e.binPath, "-test", "-config", path, "-format", "json")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// detectVersion shells `xray version` and parses the first MAJOR.MINOR.PATCH.
// Sample output: "Xray 1.8.21 (Xray, Penetrates Everything.) Custom ..."
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

// versionMatchesMajor accepts any version whose leading number equals major.
func versionMatchesMajor(got, major string) bool {
	parts := strings.Split(got, ".")
	if len(parts) == 0 {
		return false
	}
	return parts[0] == major
}

func (e *Engine) restartLocked() error {
	_ = e.stopLocked()
	return e.startLocked()
}

func (e *Engine) startLocked() error {
	if _, err := os.Stat(e.configPath); err != nil {
		return fmt.Errorf("config missing: %s", e.configPath)
	}
	_ = os.MkdirAll(e.logDir, 0o750)
	logPath := filepath.Join(e.logDir, "xray.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		logFile = nil
	}
	cmd := exec.Command(e.binPath, "run", "-config", e.configPath, "-format", "json")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if logFile != nil {
		// One wrapper for both streams (single-goroutine write); masks IPs when
		// the privacy toggle is on, transparent otherwise.
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
		return fmt.Errorf("start xray: %w", err)
	}
	p := &process{cmd: cmd, logFile: logFile, done: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	e.proc = p
	e.startedAt = time.Now()
	if e.supervisorStop == nil {
		e.supervisorStop = make(chan struct{})
		go e.supervise()
	}
	return nil
}

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

func (e *Engine) rollbackLocked(bakPath string) error {
	if _, err := os.Stat(bakPath); err != nil {
		return fmt.Errorf("backup missing at %s: %w", bakPath, err)
	}
	if err := copyFile(bakPath, e.configPath); err != nil {
		return fmt.Errorf("restore backup: %w", err)
	}
	return e.restartLocked()
}

func (e *Engine) supervise() {
	backoff := supervisorBackoffMin
	for {
		e.mu.Lock()
		p := e.proc
		stopCh := e.supervisorStop
		e.mu.Unlock()
		if p == nil {
			select {
			case <-stopCh:
				return
			case <-time.After(supervisorBackoffMin):
				continue
			}
		}
		select {
		case <-p.done:
			e.mu.Lock()
			intentional := e.proc != p
			e.mu.Unlock()
			if intentional {
				backoff = supervisorBackoffMin
				continue
			}
			time.Sleep(backoff)
			e.mu.Lock()
			if e.proc != p {
				e.mu.Unlock()
				backoff = supervisorBackoffMin
				continue
			}
			if err := e.startLocked(); err != nil {
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
