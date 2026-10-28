// Package logcap is an append-only log file with a size ceiling: once the file
// passes MaxBytes it is renamed to <path>.1 (replacing the previous one) and a
// fresh file is started, so an engine log never holds more than about two
// MaxBytes on disk however long the engine runs.
package logcap

import (
	"os"
	"sync"
)

// MaxBytes is the size at which a log file is rotated.
const MaxBytes = 64 << 20

// File is a size-capped log file. Safe for concurrent writers.
type File struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

// Open opens (or creates) path for appending with the default ceiling.
func Open(path string) (*File, error) { return open(path, MaxBytes) }

func open(path string, max int64) (*File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	l := &File{path: path, max: max, f: f}
	if fi, err := f.Stat(); err == nil {
		l.size = fi.Size()
	}
	if l.size > max {
		l.rotate()
	}
	return l, nil
}

func (l *File) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.f.Write(p)
	l.size += int64(n)
	if l.size > l.max {
		// The panel's "clear logs" truncates the file behind our back; trust
		// the file's real size over the running count before rotating.
		if fi, serr := l.f.Stat(); serr == nil {
			l.size = fi.Size()
		}
		if l.size > l.max {
			l.rotate()
		}
	}
	return n, err
}

// rotate moves the current file to <path>.1 and starts a new one. On any
// failure it keeps writing to whatever file it still has open.
func (l *File) rotate() {
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		return
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return
	}
	_ = l.f.Close()
	l.f, l.size = f, 0
}

// Close closes the underlying file.
func (l *File) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
