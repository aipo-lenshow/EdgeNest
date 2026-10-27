package api

import (
	"net"
	"sync/atomic"
	"testing"
)

// An outside connection accepted while the panel port was open is cut on its
// next read once the switch turns on (keep-alive must not outlive it).
func TestPanelConnClosesWhenSwitchTurnsOn(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	var only atomic.Bool
	c := &panelConn{Conn: a, only: &only}
	go func() { _, _ = b.Write([]byte("x")) }()
	buf := make([]byte, 1)
	if n, err := c.Read(buf); err != nil || n != 1 {
		t.Fatalf("read while open: %d %v", n, err)
	}
	only.Store(true)
	if _, err := c.Read(buf); err == nil {
		t.Fatal("read after switch on should fail")
	}
}
