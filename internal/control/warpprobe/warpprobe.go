// Package warpprobe spins up a one-shot userspace WireGuard tunnel into
// Cloudflare WARP so the panel can probe how a destination behaves *through the
// relay*, side-by-side with the host's direct egress. It is entirely
// self-contained: a netstack TUN + in-process WireGuard device, torn down after
// each run. It never touches the engine's sing-box.json or the host routing
// table, so it cannot affect the live data plane baseline.
package warpprobe

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// reservedBind wraps a WireGuard Bind to handle Cloudflare WARP's 3-byte client
// "reserved" field (bytes 1..3 of the WG message header, which vanilla
// WireGuard leaves zero):
//
//   - OUTGOING: stamp the client's reserved bytes in — WARP keys the session on
//     them and silently drops traffic without them.
//   - INCOMING: zero them back out before wireguard-go parses the packet. WARP
//     sets reserved on its replies too, and wireguard-go reads the first 4
//     bytes as the message-type uint32 — non-zero reserved bytes make every
//     reply look like an "unknown type" and the handshake never completes.
//
// Without the receive-side clear, the handshake initiation goes out fine but the
// response is rejected ("Received message with unknown type") and every dial
// times out. This is the classic WARP-in-userspace gotcha.
type reservedBind struct {
	conn.Bind
	reserved [3]byte
}

func (b *reservedBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	for _, buf := range bufs {
		if len(buf) >= 4 {
			buf[1] = b.reserved[0]
			buf[2] = b.reserved[1]
			buf[3] = b.reserved[2]
		}
	}
	return b.Bind.Send(bufs, ep)
}

// Open wraps each ReceiveFunc so reserved bytes are stripped from inbound
// packets before the WireGuard core inspects them.
func (b *reservedBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	fns, actual, err := b.Bind.Open(port)
	if err != nil {
		return nil, actual, err
	}
	wrapped := make([]conn.ReceiveFunc, len(fns))
	for i, fn := range fns {
		inner := fn
		wrapped[i] = func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
			n, rerr := inner(packets, sizes, eps)
			for j := 0; j < n; j++ {
				if sizes[j] >= 4 {
					packets[j][1] = 0
					packets[j][2] = 0
					packets[j][3] = 0
				}
			}
			return n, rerr
		}
	}
	return wrapped, actual, nil
}

// Tunnel is a live userspace WARP tunnel. Always Close() it — it owns a
// WireGuard device and a netstack instance.
type Tunnel struct {
	dev  *device.Device
	tnet *netstack.Net
}

// Open builds and brings up a WARP tunnel from a stored WarpConfig. The config
// must carry a private key, the Cloudflare peer public key, the assigned
// addresses, the reserved client ID and the endpoint (all populated by the
// /warp/register flow).
func Open(cfg *model.WarpConfig) (*Tunnel, error) {
	if cfg == nil || cfg.PrivateKey == "" || cfg.PublicKey == "" {
		return nil, fmt.Errorf("warp not configured")
	}

	reserved, err := parseReserved(cfg.Reserved)
	if err != nil {
		return nil, err
	}

	locals, err := localAddrs(cfg)
	if err != nil {
		return nil, err
	}

	privHex, err := keyToHex(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	pubHex, err := keyToHex(cfg.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("peer public key: %w", err)
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "engage.cloudflareclient.com:2408"
	}
	// wireguard-go's IpcSet endpoint= takes a literal IP:port — it calls
	// netip.ParseAddr and rejects hostnames. Cloudflare stores the WARP peer as
	// engage.cloudflareclient.com:2408, so resolve it to an IP on the underlay
	// (host resolver, not through the tunnel) before configuring the device.
	endpoint, err = resolveEndpoint(endpoint)
	if err != nil {
		return nil, err
	}

	// DNS through the tunnel: Cloudflare's own resolver, reachable inside WARP.
	tun, tnet, err := netstack.CreateNetTUN(
		locals,
		[]netip.Addr{netip.MustParseAddr("1.1.1.1")},
		1280, // WARP MTU
	)
	if err != nil {
		return nil, fmt.Errorf("create netstack tun: %w", err)
	}

	logLevel := device.LogLevelSilent
	if os.Getenv("EDGENEST_WARP_DEBUG") != "" {
		logLevel = device.LogLevelVerbose
	}
	bind := &reservedBind{Bind: conn.NewDefaultBind(), reserved: reserved}
	dev := device.NewDevice(tun, bind, device.NewLogger(logLevel, "warpprobe"))

	uapi := strings.Join([]string{
		"private_key=" + privHex,
		"public_key=" + pubHex,
		"endpoint=" + endpoint,
		"persistent_keepalive_interval=25",
		"allowed_ip=0.0.0.0/0",
		"allowed_ip=::/0",
	}, "\n")
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return nil, fmt.Errorf("configure wireguard: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("bring tunnel up: %w", err)
	}

	return &Tunnel{dev: dev, tnet: tnet}, nil
}

// Close tears the tunnel down. Safe to call once.
func (t *Tunnel) Close() {
	if t != nil && t.dev != nil {
		t.dev.Close()
	}
}

// DialContext dials addr through the WARP tunnel (resolving the host via the
// tunnel's DNS). Exposed so callers can layer their own TLS (e.g. a browser
// fingerprint via uTLS) over a WARP-egress TCP connection.
func (t *Tunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return t.dialContext(ctx, network, addr)
}

// dialContext resolves the host through the tunnel's DNS (so the answer is what
// WARP's egress sees) and dials it inside the netstack.
func (t *Tunnel) dialContext(ctx context.Context, _ string, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("bad port %q: %w", portStr, err)
	}

	ip, err := netip.ParseAddr(host)
	if err != nil {
		// Not a literal IP — resolve through the tunnel.
		addrs, lerr := t.tnet.LookupContextHost(ctx, host)
		if lerr != nil {
			return nil, fmt.Errorf("resolve %q via warp: %w", host, lerr)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("no addresses for %q", host)
		}
		ip, err = netip.ParseAddr(addrs[0])
		if err != nil {
			return nil, err
		}
	}
	return t.tnet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(ip, uint16(port)))
}

// resolveEndpoint turns a host:port WARP endpoint into ip:port. Cloudflare's
// engage.cloudflareclient.com is a hostname; wireguard-go's IpcSet needs an IP
// literal. Already-IP endpoints pass through unchanged. Prefers IPv4 (the WARP
// underlay is reachable over v4 on virtually every VPS).
func resolveEndpoint(ep string) (string, error) {
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return "", fmt.Errorf("warp endpoint %q: %w", ep, err)
	}
	if _, perr := netip.ParseAddr(host); perr == nil {
		return ep, nil // already a literal IP
	}
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		// Fall back to a well-known Cloudflare WARP endpoint IP so the probe
		// still works if the host can't resolve the engage hostname.
		return net.JoinHostPort("162.159.192.1", port), nil
	}
	for _, ip := range ips {
		if a, e := netip.ParseAddr(ip); e == nil && a.Is4() {
			return net.JoinHostPort(ip, port), nil
		}
	}
	return net.JoinHostPort(ips[0], port), nil
}

// parseReserved decodes the stored JSON "[a,b,c]" into a 3-byte array.
func parseReserved(raw string) ([3]byte, error) {
	var out [3]byte
	if strings.TrimSpace(raw) == "" {
		return out, nil // zero reserved — some free accounts tolerate it
	}
	var v []int
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return out, fmt.Errorf("reserved: %w", err)
	}
	for i := 0; i < 3 && i < len(v); i++ {
		out[i] = byte(v[i])
	}
	return out, nil
}

// localAddrs parses the WARP-assigned v4/v6 addresses (CIDR form) into bare
// netip.Addr values for the netstack interface.
func localAddrs(cfg *model.WarpConfig) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, a := range []string{cfg.Address4, cfg.Address6} {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if i := strings.IndexByte(a, '/'); i >= 0 {
			a = a[:i]
		}
		ip, err := netip.ParseAddr(a)
		if err != nil {
			return nil, fmt.Errorf("warp address %q: %w", a, err)
		}
		out = append(out, ip)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("warp config has no assigned address")
	}
	return out, nil
}

// keyToHex converts a standard base64 WireGuard key into the hex form IpcSet
// expects.
func keyToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", err
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("expected 32-byte key, got %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}
