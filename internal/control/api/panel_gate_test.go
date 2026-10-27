package api

import (
	"net"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/antiblock"
	"github.com/aipo-lenshow/EdgeNest/internal/control/bootstrap"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

func gateHandler(t *testing.T) *Handler {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetSetting(bootstrap.KeyPanelPath, "/ENPanel-abc")
	return &Handler{store: st, jwtSecret: "s"}
}

func TestPanelRequestAllowed(t *testing.T) {
	h := gateHandler(t)
	ok := func(path, cookie string) bool {
		r := httptest.NewRequest("GET", path, nil)
		if cookie != "" {
			r.Header.Set("Cookie", panelGateCookieName+"="+cookie)
		}
		return h.PanelRequestAllowed(r)
	}
	good := h.panelGateToken("/ENPanel-abc")
	if !ok("/ENPanel-abc", "") || !ok("/ENPanel-abc/login", "") {
		t.Fatal("secret path refused")
	}
	if !ok("/api/v1/inbounds", good) {
		t.Fatal("valid gate cookie refused")
	}
	if ok("/api/v1/inbounds", "") || ok("/api/v1/inbounds", "forged") || ok("/ENPanel-abcd", "") {
		t.Fatal("request without a valid gate let through")
	}
}

// With the switch on, the panel port only accepts loopback connections;
// with it off, everyone gets through as before.
func TestGuardPanelListener(t *testing.T) {
	h := gateHandler(t)
	_ = antiblock.Save(h.store, antiblock.Config{EdgeDomain: "cdn.example.com", PanelViaGatewayOnly: true})
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := h.GuardPanelListener(raw)
	defer ln.Close()
	if !h.panelGatewayOnly.Load() {
		t.Fatal("switch not loaded from settings")
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("x"))
			_ = c.Close()
		}
	}()
	c, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err != nil || buf[0] != 'x' {
		t.Fatalf("loopback must still reach the panel: %v", err)
	}
	// Non-loopback addresses can't be produced from a test host; the filter
	// itself is the same netip check the gateway's cfListener uses.
}

func TestPanelSwitchCannotOutliveGateway(t *testing.T) {
	c, err := antiblock.Normalize(antiblock.Config{EdgeDomain: "cdn.example.com", GatewayMode: antiblock.GatewayExternal, PanelViaGatewayOnly: true})
	if err != nil || c.PanelViaGatewayOnly {
		t.Fatalf("external mode must reopen the panel port: %+v %v", c, err)
	}
	c, _ = antiblock.Normalize(antiblock.Config{PanelViaGatewayOnly: true})
	if c.PanelViaGatewayOnly {
		t.Fatal("no edge domain must reopen the panel port")
	}
}

func TestGuardPanelListenerDropsNonLoopback(t *testing.T) {
	var lan net.IP
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			lan = n.IP
			break
		}
	}
	if lan == nil {
		t.Skip("no non-loopback IPv4 on this host")
	}
	h := gateHandler(t)
	_ = antiblock.Save(h.store, antiblock.Config{EdgeDomain: "cdn.example.com", PanelViaGatewayOnly: true})
	raw, err := net.Listen("tcp", net.JoinHostPort(lan.String(), "0"))
	if err != nil {
		t.Skip(err)
	}
	ln := h.GuardPanelListener(raw)
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("x"))
			_ = c.Close()
		}
	}()
	c, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, _ := c.Read(make([]byte, 1)); n != 0 {
		t.Fatal("a non-loopback client reached the panel while it is gateway-only")
	}
	// Switching it off lets the same client in again, without a restart.
	h.panelGatewayOnly.Store(false)
	c2, _ := net.Dial("tcp", raw.Addr().String())
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, _ := c2.Read(make([]byte, 1)); n != 1 {
		t.Fatal("switch off should reopen the panel port")
	}
}
