// Package v2raystats is a minimal read-only client for sing-box's v2ray_api
// StatsService (the gRPC stats surface enabled by the `with_v2ray_api` build
// tag). It exists for one job: pull per-user (per Client.Email) cumulative
// traffic counters so the panel can enforce byte quotas.
//
// Why this and not clash_api /connections: sing-box's clash_api connection
// metadata does not carry the matched inbound auth user (verified against
// upstream tracker.go for v1.13.12 and v1.13.13 — the MarshalJSON emits only
// network/type/sourceIP/destinationIP/sourcePort/destinationPort/host/dnsMode/
// processPath, never the user). So clash can never attribute bytes to a user on
// a shared multi-user inbound. v2ray_api's StatsService instead maintains
// cumulative read/write counters named `user>>>{email}>>>traffic>>>uplink|
// downlink`, counted at the connection-tracker layer — short-lived connections
// and UDP/QUIC included, so it is an accurate counter, not a poll-sampled soft
// cap. The trade is that the official sing-box release binary is NOT built with
// `with_v2ray_api`, so the panel ships its own build (see scripts/build-singbox.sh).
package v2raystats

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// statsQueryStatsMethod is the gRPC full-method path sing-box actually serves
// the stats QueryStats RPC on — the v2ray-core compat name, not the proto's own
// experimental.v2rayapi name (see QueryUserTraffic for why).
const statsQueryStatsMethod = "/v2ray.core.app.stats.command.StatsService/QueryStats"

// userStatRe matches the per-user counter names sing-box emits:
//   user>>>{email}>>>traffic>>>uplink
//   user>>>{email}>>>traffic>>>downlink
// The email itself can contain anything except ">", so we stop at the next
// ">>>" segment. Matches the v2ray-core / xray-core stats key format exactly.
var userStatRe = regexp.MustCompile(`^user>>>(.+)>>>traffic>>>(uplink|downlink)$`)

// UserTraffic is one user's cumulative byte counters as reported by the stats
// service. Values are monotonic within a single sing-box process lifetime (they
// reset to 0 when sing-box restarts), which the poller accounts for by diffing.
type UserTraffic struct {
	Up   int64
	Down int64
}

// Client talks to a sing-box v2ray_api StatsService over loopback gRPC. The
// stats gRPC server has no auth (sing-box serves it with insecure credentials),
// so it MUST be bound to loopback — same trust model as clash_api on 127.0.0.1.
type Client struct {
	addr string
}

// New builds a client for the StatsService controller at "host:port".
func New(addr string) *Client { return &Client{addr: addr} }

// QueryUserTraffic returns the current cumulative up/down bytes for every user
// the stats service knows about, keyed by email. It dials, queries, and closes
// per call — the poll interval is seconds apart, so a persistent connection
// buys nothing and a fresh dial survives sing-box restarts cleanly.
//
// Reset is false: we read absolute counters and diff in the poller, rather than
// have sing-box zero them on read, so a missed/failed poll never loses bytes.
func (c *Client) QueryUserTraffic(ctx context.Context) (map[string]UserTraffic, error) {
	conn, err := grpc.NewClient(c.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("v2ray_api dial: %w", err)
	}
	defer conn.Close()

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req := &QueryStatsRequest{
		// sing-box filters on the repeated Patterns field (the singular Pattern
		// is deprecated and ignored). Anchored regexp so we only pull user
		// counters, not the per-inbound / per-outbound ones we don't account
		// against quotas. We still re-match client-side with userStatRe.
		Patterns: []string{"^user>>>"},
		Regexp:   true,
		Reset_:   false,
	}
	resp := &QueryStatsResponse{}
	// IMPORTANT: sing-box registers its StatsService under the v2ray-core
	// COMPAT name "v2ray.core.app.stats.command.StatsService", NOT the proto's
	// own "experimental.v2rayapi.StatsService" — so existing v2ray/xray stats
	// clients interoperate. Calling the proto name returns gRPC Unimplemented
	// "unknown service". Verified against a real sing-box + xray build. The
	// request/response messages are wire-compatible either way (same field
	// numbers), so we Invoke the compat path with the vendored messages.
	if err := conn.Invoke(cctx, statsQueryStatsMethod, req, resp); err != nil {
		return nil, fmt.Errorf("v2ray_api QueryStats: %w", err)
	}

	out := make(map[string]UserTraffic)
	for _, st := range resp.GetStat() {
		m := userStatRe.FindStringSubmatch(st.GetName())
		if m == nil {
			continue
		}
		email, dir := m[1], m[2]
		ut := out[email]
		if dir == "uplink" {
			ut.Up = st.GetValue()
		} else {
			ut.Down = st.GetValue()
		}
		out[email] = ut
	}
	return out, nil
}
