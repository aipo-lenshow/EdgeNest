package bootstrap

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// publicIPEchoEndpoints are plain-text IPv4 echo services tried in order.
// Each returns the caller's public IPv4 as a single line of text.
var publicIPEchoEndpoints = []string{
	"https://api.ipify.org",
	"https://ipv4.icanhazip.com",
	"https://ifconfig.me/ip",
	"https://checkip.amazonaws.com",
}

// DetectPublicIPv4 tries each echo endpoint in order and returns the first
// valid IPv4 response. Returns "" if every probe fails within the timeout.
//
// Used during first-run provisioning so subscription URIs render against a
// real reachable host instead of returning SHARE_HOST_UNSET on day one.
func DetectPublicIPv4(timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			// Force IPv4: VPSes commonly have an IPv6 address too and we
			// want the v4 the client will actually dial. ipify/icanhazip
			// honour the IPv4-specific hostnames above already, but pinning
			// the dialer is belt+braces.
			DialContext: (&net.Dialer{Timeout: timeout}).DialContext,
		},
	}

	for _, url := range publicIPEchoEndpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "EdgeNest/bootstrap")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		ip := strings.TrimSpace(string(body))
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() == nil {
			continue
		}
		return parsed.To4().String()
	}
	return ""
}
