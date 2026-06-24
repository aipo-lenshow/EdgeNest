// Package core defines the node-agnostic data contracts shared between the
// control plane and the node execution plane. Nothing here may import control
// or node packages — this is the neutral seam both sides depend on.
package core

// ---- Engine identifiers ----

const (
	EngineSingbox = "singbox"
	EngineXray    = "xray"
)

// ---- DesiredConfig: what the control plane wants a node to look like ----

// DesiredConfig is rendered by the control-plane orchestrator (one per node)
// and handed to a NodeClient.ApplyConfig. It is intentionally free of any DB
// or transport types so the same struct works for local (in-process) and
// remote (gRPC) node clients.
type DesiredConfig struct {
	Inbounds  []InboundSpec  `json:"inbounds"`
	Outbounds []OutboundSpec `json:"outbounds"`
	Routes    []RouteSpec    `json:"routes"`
	Firewall  FirewallSpec   `json:"firewall"`
	Warp      *WarpSpec      `json:"warp,omitempty"`
	Certs     []CertSpec     `json:"certs,omitempty"`
	Advanced  *AdvancedSpec  `json:"advanced,omitempty"`
	ClashAPI  *ClashAPISpec  `json:"clash_api,omitempty"`
	V2RayAPI  *V2RayAPISpec  `json:"v2ray_api,omitempty"`
	XRayAPI   *XRayAPISpec   `json:"xray_api,omitempty"`
	Overlay   *OverlaySpec   `json:"overlay,omitempty"` // v1: always nil, reserved for Platform (v2)
}

// ClashAPISpec enables sing-box's clash_api controller, which the panel's live
// domain-capture polls (GET /connections) to learn which domains a client's
// real traffic actually reached. Bound to loopback with a secret — it is a
// read-only telemetry surface, never exposed off-box. Behaviour-neutral: it does
// not touch routing/outbounds, so enabling it leaves the data path unchanged.
type ClashAPISpec struct {
	Controller string `json:"controller"` // host:port, always loopback
	Secret     string `json:"secret"`
}

// V2RayAPISpec enables sing-box's v2ray_api StatsService — the only stock
// sing-box surface that reports per-user (per Client.Email) cumulative traffic,
// which the quota enforcer needs (clash_api's connection metadata never carries
// the user; see internal/control/v2raystats). Controller is a loopback gRPC
// listen address with no auth (sing-box serves it insecure), so it MUST stay on
// 127.0.0.1. The rendered users list is derived from the inbound clients, not
// carried here. Behaviour-neutral: a telemetry counter, orthogonal to routing.
//
// Requires a sing-box built with the `with_v2ray_api` tag (the official release
// omits it); the panel ships its own build — see scripts/build-singbox.sh.
type V2RayAPISpec struct {
	Controller string `json:"controller"` // host:port, always loopback
}

// XRayAPISpec enables xray-core's StatsService — the same per-user accounting as
// sing-box's v2ray_api, but for inbounds the wizard hosts on the optional
// xray-core engine (VLESS-XHTTP-Reality / XHTTP-ENC / AnyTLS). xray-core ships
// its stats/api/commander apps in the stock release (no build tag needed,
// unlike sing-box), so this needs no custom binary. Controller is the loopback
// gRPC listen address (api.listen) with no auth, so it MUST stay on 127.0.0.1.
// Behaviour-neutral: a telemetry counter, orthogonal to routing. The traffic
// poller queries it on the same v2ray-core service path as the sing-box source.
type XRayAPISpec struct {
	Controller string `json:"controller"` // host:port, always loopback
}

// InboundSpec describes a single listening inbound. Engine selects which proxy
// engine renders it (see Engine* constants). Settings carries protocol-specific
// fields as already-parsed values; the engine renderer interprets them per Type.
//
// Listen is the literal IP the engine binds to — picked by the wizard from
// NodeCapability.IPv4Addrs / IPv6Addrs. Renderers MUST pass this through as
// the inbound `listen` field; v4 and v6 inbounds on the same port (across
// families) coexist because they occupy distinct (IP, port) sockets.
type InboundSpec struct {
	Tag      string         `json:"tag"`
	Engine   string         `json:"engine"` // singbox | xray
	Type     string         `json:"type"`   // vless | hysteria2 | trojan | ...
	Listen   string         `json:"listen"` // e.g. "1.2.3.4" or "2607::2"
	Port     int            `json:"port"`
	Network  string         `json:"network"` // tcp | udp | both
	Settings map[string]any `json:"settings"`
	Clients  []ClientSpec   `json:"clients"`
	// SubscriptionHost is the literal IP embedded as the URI `server` for
	// this inbound. Normally equal to Listen; the two are separate so the
	// share resolver's Argo / CDN paths can override the URI host without
	// changing the listen socket. Empty = global share-host fallback.
	SubscriptionHost string `json:"subscription_host,omitempty"`
}

// ClientSpec is one credential on an inbound. Email is the stats key and MUST
// match the engine-side user name/email used for traffic accounting.
type ClientSpec struct {
	Email    string `json:"email"`
	UUID     string `json:"uuid,omitempty"`
	Password string `json:"password,omitempty"`
	Flow     string `json:"flow,omitempty"`
}

type OutboundSpec struct {
	Tag      string         `json:"tag"`
	Type     string         `json:"type"` // direct | block | wireguard(warp) | node-link(v2)
	Settings map[string]any `json:"settings,omitempty"`
}

type RouteSpec struct {
	Type     string `json:"type"`  // domain_suffix | domain | ip_cidr | geosite | geoip
	Value    string `json:"value"` // e.g. openai.com
	Outbound string `json:"outbound"`
}

// FirewallSpec lists the ports the node must allow for the above inbounds.
type FirewallSpec struct {
	AllowPorts []PortRule `json:"allow_ports"`
	// PortHops are Hysteria2 inbound port-hopping ranges. Each redirects an
	// inbound UDP port range to the protocol's real listen port via nat
	// PREROUTING REDIRECT (v4+v6), so clients can spray packets across many
	// ports to dodge single-port UDP QoS while the server listens on one.
	PortHops []PortHopRule `json:"port_hops,omitempty"`
}

// PortHopRule maps an inbound UDP port range [Start,End] to ToPort. Hy2 only:
// TUIC has no client-side port-range support in any client, so a redirect for
// it would be dead weight (researched 2026-06-13).
type PortHopRule struct {
	Start  int `json:"start"`
	End    int `json:"end"`
	ToPort int `json:"to_port"`
}

type PortRule struct {
	Port  int    `json:"port"`
	Proto string `json:"proto"` // tcp | udp | both
	Note  string `json:"note"`
}

type WarpSpec struct {
	Enabled    bool   `json:"enabled"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	Address4   string `json:"address4"`
	Address6   string `json:"address6"`
	Reserved   []int  `json:"reserved"`
	Endpoint   string `json:"endpoint"`
}

type CertSpec struct {
	Domain   string `json:"domain"`
	CertPath string `json:"cert_path"`
	KeyPath  string `json:"key_path"`
}

// AdvancedSpec carries opt-in anti-blocking features. Always nil unless the
// user explicitly enables them; the wizard never sets this.
type AdvancedSpec struct {
	CDNEnabled      bool     `json:"cdn_enabled"`
	CDNPreferredIPs []string `json:"cdn_preferred_ips,omitempty"`
	ArgoEnabled     bool     `json:"argo_enabled"`
	ArgoMode        string   `json:"argo_mode,omitempty"` // temp | fixed
	ArgoDomain      string   `json:"argo_domain,omitempty"`
	ArgoToken       string   `json:"argo_token,omitempty"`
	// BlockQUIC: reject forwarded QUIC/STUN at the server route so browsers
	// fall back to TCP/443 through the tunnel. Opt-in, default false.
	BlockQUIC bool `json:"block_quic"`
}

// OverlaySpec is reserved for Platform (v2) overlay networking. v1 leaves it nil.
type OverlaySpec struct {
	Provider  string `json:"provider"` // headscale | wireguard
	JoinKey   string `json:"join_key"`
	OverlayIP string `json:"overlay_ip"`
}

// ---- Results / status returned from a node ----

type ApplyResult struct {
	OK         bool   `json:"ok"`
	RolledBack bool   `json:"rolled_back"`
	Message    string `json:"message"`
}

type EngineStatus struct {
	Running bool   `json:"running"`
	Version string `json:"version"`
	Uptime  int64  `json:"uptime_seconds"`
	Detail  string `json:"detail,omitempty"`
}

type Traffic struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// HealthSnapshot is returned by Heartbeat / SelfCheck. NodeID is filled by the
// control plane based on which node it queried.
type HealthSnapshot struct {
	CPU            float64 `json:"cpu"`
	Mem            float64 `json:"mem"`
	Disk           float64 `json:"disk"`
	PublicIP       string  `json:"public_ip"`
	Country        string  `json:"country"`
	SingboxRunning bool    `json:"singbox_running"`
	XrayRunning    bool    `json:"xray_running"`
	BBR            string  `json:"bbr"`
	Errors         string  `json:"errors,omitempty"`
}
