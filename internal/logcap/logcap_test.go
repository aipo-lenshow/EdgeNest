package logcap

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRotatesPastCeiling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sing-box.log")
	l, err := open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	line := bytes.Repeat([]byte("x"), 39)
	line = append(line, '\n')
	for i := 0; i < 10; i++ {
		if _, err := l.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	_ = l.Close()
	cur, _ := os.Stat(path)
	old, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("no rotated file: %v", err)
	}
	if cur.Size() > 100 || old.Size() > 140 {
		t.Fatalf("sizes not capped: current %d, rotated %d", cur.Size(), old.Size())
	}
}

func TestOversizedFileRotatedOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xray.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("y"), 500), 0o640); err != nil {
		t.Fatal(err)
	}
	l, err := open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = l.Write([]byte("fresh\n"))
	_ = l.Close()
	if b, _ := os.ReadFile(path); string(b) != "fresh\n" {
		t.Fatalf("current log = %q, want only the new line", b)
	}
	if fi, _ := os.Stat(path + ".1"); fi == nil || fi.Size() != 500 {
		t.Fatal("old content not kept in .1")
	}
}

func TestTruncatedBehindOurBackIsNotRotated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sing-box.log")
	l, err := open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = l.Write(bytes.Repeat([]byte("z"), 90))
	if err := os.Truncate(path, 0); err != nil { // the panel's "clear logs"
		t.Fatal(err)
	}
	_, _ = l.Write(bytes.Repeat([]byte("z"), 20))
	_ = l.Close()
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("rotated although the real file was small")
	}
}
