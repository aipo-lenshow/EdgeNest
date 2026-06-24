package unlock

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// browserUA matches the Chrome fingerprint utlsGet presents — keep them in sync
// so UA and TLS together look like a real browser to bot-management edges.
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// Some probe pages are large — TikTok's /explore is ~350KB and the
// "region":"XX" marker sits well past 64KB, so a small cap truncated it and the
// check false-negatived. 512KB comfortably covers the markers we look for.
const probeBodyCap = 512 * 1024

// maxRedirects bounds ProbeFull's redirect following (opt-in per request).
const maxRedirects = 5

// utlsGet performs a GET and returns status + body — the back-compat shape the
// generic OKSub/BlockSub probe path uses. Richer per-service checks (POST,
// custom request headers, reading response headers) go through utlsDo.
func utlsGet(ctx context.Context, rawurl string, dial DialFunc, timeout time.Duration) (int, string, error) {
	st, _, body, err := utlsDo(ctx, "GET", rawurl, nil, "", dial, timeout)
	return st, body, err
}

// utlsDo performs an HTTP request over the supplied dialer using a real Chrome
// TLS ClientHello (uTLS), speaking HTTP/2 or HTTP/1.1 per the negotiated ALPN.
// It supports an arbitrary method, request headers and body, and returns the
// response headers — everything the streaming/region checks need (Spotify/DAZN
// POST, Netflix/Hotstar Location header, etc).
//
// Why uTLS: Cloudflare bot management (claude.ai, parts of streaming) keys on
// the TLS fingerprint (JA3/JA4). The stock Go net/http fingerprint gets 403'd,
// so a plain probe reported "blocked" even where a real browser succeeds — a
// flaky false negative. With a Chrome fingerprint the verdict matches what a
// browser actually sees (verified on a VPS: claude.ai direct=403, via WARP=302).
//
// Redirects are NOT followed (single round-trip) — the streaming checks depend
// on seeing the raw 301/404/403 and the Location header. One-shot: a fresh
// connection per call, fully read + closed here so nothing leaks.
func utlsDo(ctx context.Context, method, rawurl string, headers map[string]string, body string, dial DialFunc, timeout time.Duration) (int, http.Header, string, error) {
	if dial == nil {
		dial = (&net.Dialer{Timeout: 8 * time.Second}).DialContext
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	if method == "" {
		method = "GET"
	}
	u, err := url.Parse(rawurl)
	if err != nil {
		return 0, nil, "", err
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}

	conn, err := dial(cctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return 0, nil, "", err
	}
	defer conn.Close()
	if dl, ok := cctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(cctx, method, rawurl, rdr)
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Plain HTTP: no TLS layer.
	if u.Scheme == "http" {
		return roundTripH1(conn, req)
	}

	// HTTPS: present a Chrome ClientHello.
	uc := utls.UClient(conn, &utls.Config{ServerName: host}, utls.HelloChrome_Auto)
	if err := uc.HandshakeContext(cctx); err != nil {
		return 0, nil, "", err
	}
	if uc.ConnectionState().NegotiatedProtocol == "h2" {
		tr := &http2.Transport{}
		cc, err := tr.NewClientConn(uc)
		if err != nil {
			return 0, nil, "", err
		}
		resp, err := cc.RoundTrip(req)
		if err != nil {
			return 0, nil, "", err
		}
		return readResp(resp)
	}
	return roundTripH1(uc, req)
}

func roundTripH1(conn io.ReadWriter, req *http.Request) (int, http.Header, string, error) {
	if err := req.Write(conn); err != nil {
		return 0, nil, "", err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return 0, nil, "", err
	}
	return readResp(resp)
}

func readResp(resp *http.Response) (int, http.Header, string, error) {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, probeBodyCap))
	return resp.StatusCode, resp.Header, string(body), nil
}
