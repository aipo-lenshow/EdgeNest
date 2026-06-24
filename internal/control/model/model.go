// Package model defines the GORM data models. All proxy resources carry a
// NodeID so the schema is multi-node ready from day one (Lite uses a single
// auto-provisioned "local" node).
package model

// Admin is the single panel administrator (v1).
type Admin struct {
	ID                 uint   `gorm:"primaryKey" json:"id"`
	Username           string `gorm:"uniqueIndex" json:"username"`
	PasswordHash       string `json:"-"`
	MustChangePassword bool   `json:"must_change_password"`
	// TOTP (2FA) fields. TOTPSecret is the base32 shared secret; it must be
	// stored in plaintext because the server recomputes the code on every
	// login (there is no hashable form). This matches the existing trust model
	// — the admin already has filesystem/DB access — and 2FA's purpose is to
	// stop a remote attacker who only stole the password, not someone with the
	// DB. TOTPPending holds a not-yet-confirmed secret during enrollment so a
	// half-finished setup never locks anyone out. RecoveryCodes is a JSON array
	// of single-use backup codes, consumed as they're used.
	TOTPEnabled   bool   `gorm:"default:false" json:"totp_enabled"`
	TOTPSecret    string `json:"-"`
	TOTPPending   string `json:"-"`
	RecoveryCodes string `gorm:"type:text" json:"-"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

// Setting is a key-value store for panel config (port, path, jwt_secret,
// reality keypair, bbr state, subscription domain, wizard_done, run_role, ...).
type Setting struct {
	Key   string `gorm:"primaryKey" json:"key"`
	Value string `json:"value"`
}

// Node represents a managed node. Built from v1; Lite auto-upserts one
// IsLocal=true row named "local".
type Node struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	Name       string `gorm:"uniqueIndex" json:"name"`
	Role       string `json:"role"` // standalone | edge | ...(v2)
	Region     string `json:"region"`
	PublicIP   string `json:"public_ip"`
	OverlayIP  string `json:"overlay_ip"` // empty in v1
	Status     string `json:"status"`
	Version    string `json:"version"`
	SecretHash string `json:"-"`
	IsLocal    bool   `json:"is_local"`
	LastSeenAt int64  `json:"last_seen_at"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

// Inbound is one protocol listening on one (IP, port) of one node.
//
// Listen + Port together identify the OS socket: same port on different
// listen IPs are two distinct sockets (multi-IP VPS lets v4 SOCKS5:1080 and
// v6 SOCKS5:1080 coexist), so the DB unique constraint is the composite
// (listen, port). Tag is the sing-box / xray inbound name and stays globally
// unique — the wizard generator includes the family suffix (e.g.
// "EdgeNest-VLESS-Reality-v4-8443") so cross-family same-protocol same-port
// inbounds never collide.
type Inbound struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	NodeID  uint   `gorm:"index" json:"node_id"`
	Tag     string `gorm:"uniqueIndex" json:"tag"`
	Engine  string `json:"engine"` // singbox | xray (derived from Type)
	Type    string `json:"type"`
	Remark  string `json:"remark"`
	Listen  string `gorm:"uniqueIndex:idx_inbound_listen_port" json:"listen"`
	Port    int    `gorm:"uniqueIndex:idx_inbound_listen_port" json:"port"`
	Network string `json:"network"` // tcp | udp | both
	// No `default:true` here — see store.CreateInbound. GORM treats Go's
	// zero-value `false` as "field unset" and substitutes the SQL DDL
	// default, which silently flips explicit enabled=false back to true.
	// Default-true semantics live in the API handler instead (enabled=true
	// when the request omits the field).
	Enabled  bool   `json:"enabled"`
	Settings string `gorm:"type:text" json:"settings"` // protocol params as JSON
	// SubscriptionHost is the literal IP address the share resolver embeds
	// as the URI `server` field for this inbound. Set per-inbound by the
	// wizard's HostChooser (Step1) to the IP the user picked from the v4 or
	// v6 list. Normally equals Listen, but the two stay separate so Argo /
	// CDN paths can override the URI host without touching the listen socket.
	//
	// Empty = fall back to the share resolver's global host chain
	// (Settings.share_host > NodeCapability.IPv4Addr > NodeCapability.IPv6Addr
	// > Node.PublicIP). Inbounds created before this field existed
	// land here on upgrade migration.
	SubscriptionHost string   `json:"subscription_host"`
	CreatedAt        int64    `json:"created_at"`
	UpdatedAt        int64    `json:"updated_at"`
	Clients          []Client `gorm:"foreignKey:InboundID" json:"clients,omitempty"`
}

// Client is one credential on an inbound. Email is the stats key.
type Client struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	InboundID   uint   `gorm:"index" json:"inbound_id"`
	Email       string `gorm:"index" json:"email"`
	UUID        string `json:"uuid"`
	Password    string `json:"password"`
	Flow        string `json:"flow"`
	TrafficUp   int64  `gorm:"default:0" json:"traffic_up"`
	TrafficDown int64  `gorm:"default:0" json:"traffic_down"`
	QuotaBytes  int64  `gorm:"default:0" json:"quota_bytes"` // 0 = unlimited
	ExpiryAt    int64  `gorm:"default:0" json:"expiry_at"`   // 0 = never
	// Same reason as Inbound.Enabled — see store.CreateInbound.
	Enabled   bool  `json:"enabled"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// Subscription is a revocable, scoped subscription token.
//
// Self-hosted single-tenant context: the panel admin already has filesystem
// access to the DB, so storing the raw token in plaintext is no worse than
// the existing trust model and lets the admin re-open the URL/QR for any
// subscription later (instead of forcing revoke+reissue on every browser
// close). TokenHash is kept for /sub/:token lookup speed.
type Subscription struct {
	ID              uint   `gorm:"primaryKey" json:"id"`
	Name            string `json:"name"`
	Token           string `gorm:"index" json:"token,omitempty"`
	TokenHash       string `gorm:"uniqueIndex" json:"-"`
	ClientID        uint   `gorm:"index" json:"client_id"`
	AllowedNodes    string `json:"allowed_nodes"`    // JSON []id, empty = all
	AllowedInbounds string `json:"allowed_inbounds"` // JSON []tag, empty = all
	ExpiresAt       int64  `gorm:"default:0" json:"expires_at"`
	Revoked         bool   `gorm:"default:false" json:"revoked"`
	CreatedAt       int64  `json:"created_at"`
}

// WarpConfig holds the WireGuard (WARP) outbound config for a node.
type WarpConfig struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	NodeID     uint   `gorm:"index" json:"node_id"`
	Enabled    bool   `json:"enabled"`
	PrivateKey string `json:"-"`
	PublicKey  string `json:"public_key"`
	Address4   string `json:"address4"`
	Address6   string `json:"address6"`
	Reserved   string `json:"reserved"` // JSON [a,b,c]
	Endpoint   string `json:"endpoint"`
	UpdatedAt  int64  `json:"updated_at"`
}

// RouteRule is a domain/IP → outbound routing rule.
type RouteRule struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	NodeID   uint   `gorm:"index" json:"node_id"`
	Type     string `json:"type"`  // domain_suffix | domain | ip_cidr | geosite | geoip
	Value    string `json:"value"` // openai.com
	Outbound string `json:"outbound"`
	Enabled  bool   `json:"enabled"`
	Order    int    `json:"order"`
	// Source tags where the rule came from so the UI can filter/group it:
	// a preset key ("ai" / "streaming") for preset-applied rules, "custom" for
	// hand-entered ones. Control-plane label only — never reaches the engine.
	// Legacy rows predating this column carry "" and get inferred from preset
	// membership at read time (see api.toRouteDTO).
	Source string `json:"source"`
}

// FirewallRule mirrors a system firewall allow rule managed by the panel.
type FirewallRule struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	NodeID  uint   `gorm:"index" json:"node_id"`
	Port    int    `json:"port"`
	Proto   string `json:"proto"` // tcp | udp | both
	Note    string `json:"note"`
	Managed bool   `json:"managed"` // auto-allowed by panel (recyclable)
}

// Certificate is an ACME-issued certificate for a domain on a node.
type Certificate struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	NodeID      uint   `gorm:"index" json:"node_id"`
	Domain      string `gorm:"index" json:"domain"`
	Mode        string `json:"mode"`         // http-01 | dns-01
	DNSProvider string `json:"dns_provider"` // cloudflare | aliyun | dnspod | ...
	// Email is the ACME account contact captured at issue time. Renewal reads
	// it from the row (not the global acme_email setting) so a cleared setting
	// can't break renewal of an already-issued cert; falls back to the setting
	// for rows issued before this column existed.
	Email     string `json:"email"`
	CertPath  string `json:"cert_path"`
	KeyPath   string `json:"key_path"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
	AutoRenew bool   `gorm:"default:true" json:"auto_renew"`
	LastError string `json:"last_error"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// AdvancedConfig holds opt-in anti-blocking features (default OFF).
type AdvancedConfig struct {
	ID              uint   `gorm:"primaryKey" json:"id"`
	NodeID          uint   `gorm:"index" json:"node_id"`
	CDNEnabled      bool   `gorm:"default:false" json:"cdn_enabled"`
	CDNPreferredIPs string `json:"cdn_preferred_ips"` // JSON
	// NOTE: older DBs may still carry a legacy `cdn_back_host` column. It was a
	// forced-mandatory field the render path never read (SNI/Host always come
	// from the inbound's own real domain), so it was dropped from the model.
	// GORM AutoMigrate leaves the orphan column in place; nothing references it.
	ArgoEnabled bool   `gorm:"default:false" json:"argo_enabled"`
	ArgoMode    string `json:"argo_mode"` // temp | fixed
	ArgoDomain  string `json:"argo_domain"`
	ArgoToken   string `json:"-"`
	// BlockQUIC, when on, makes the server reject forwarded QUIC/STUN so the
	// client's browser falls back to TCP/443 through the tunnel (anti-leak /
	// anti-QoS). Default off — it breaks legitimate HTTP/3 for users who want
	// it, so it's strictly opt-in.
	BlockQUIC bool  `gorm:"default:false" json:"block_quic"`
	// RedactClientIP, when on, raises the sing-box log level to "warn" so the
	// per-connection "inbound connection from <ip>" lines (which carry the
	// client's real source IP) stop being written to sing-box.log. xray already
	// defaults to "warning" and never logs the source IP, so only sing-box needs
	// this. A privacy opt-in for self-hosters who don't want their users' IPs
	// persisted on disk; off by default (operators lose per-connection dial
	// detail when it's on). Touches only log.level — routing/dial bytes are
	// unchanged, so it can never break the v4/v6 baseline.
	RedactClientIP bool  `gorm:"default:false" json:"redact_client_ip"`
	UpdatedAt      int64 `json:"updated_at"`
}

// AuditLog records a sensitive operation.
type AuditLog struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Actor     string `json:"actor"` // admin username / "system"
	Action    string `json:"action"`
	Resource  string `json:"resource"`
	IP        string `json:"ip"`
	Meta      string `gorm:"type:text" json:"meta"` // JSON
	CreatedAt int64  `json:"created_at"`
}

// HealthSnapshot persists a node health sample for the dashboard.
type HealthSnapshot struct {
	ID             uint    `gorm:"primaryKey" json:"id"`
	NodeID         uint    `gorm:"index" json:"node_id"`
	CPU            float64 `json:"cpu"`
	Mem            float64 `json:"mem"`
	Disk           float64 `json:"disk"`
	PublicIP       string  `json:"public_ip"`
	Country        string  `json:"country"`
	SingboxRunning bool    `json:"singbox_running"`
	BBR            string  `json:"bbr"`
	Errors         string  `json:"errors"`
	CreatedAt      int64   `json:"created_at"`
}

// TrafficDaily is a per-user, per-day traffic bucket. The cumulative counters
// on Client (TrafficUp/TrafficDown) answer "total since creation"; these
// buckets add the time dimension so the panel/bot can report month-to-date,
// arbitrary ranges, and trends. The traffic poller upserts a row per active
// (date, email) each tick, adding the same per-tick delta it credits to the
// cumulative counters — so the two stay consistent without touching the
// cumulative semantics. Date is server-local YYYY-MM-DD.
type TrafficDaily struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Date      string `gorm:"uniqueIndex:idx_daily_date_email;not null" json:"date"`
	Email     string `gorm:"uniqueIndex:idx_daily_date_email;not null" json:"email"`
	Up        int64  `gorm:"default:0" json:"up"`
	Down      int64  `gorm:"default:0" json:"down"`
	UpdatedAt int64  `json:"updated_at"`
}

// JoinToken is reserved for Platform (v2) node enrollment. Table created in v1.
type JoinToken struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	TokenHash string `gorm:"uniqueIndex" json:"-"`
	Role      string `json:"role"`
	MaxUses   int    `json:"max_uses"`
	UsedCount int    `json:"used_count"`
	ExpiresAt int64  `json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
}

// AllModels lists every model for AutoMigrate.
func AllModels() []any {
	return []any{
		&Admin{}, &Setting{}, &Node{}, &Inbound{}, &Client{},
		&Subscription{}, &WarpConfig{}, &RouteRule{}, &FirewallRule{},
		&Certificate{}, &AdvancedConfig{}, &AuditLog{}, &HealthSnapshot{},
		&TrafficDaily{}, &JoinToken{},
	}
}
