// Package orchestrator assembles the per-node DesiredConfig from the control
// plane's DB state and dispatches it through the NodeClient seam.
//
// DISCIPLINE: orchestrator is in control/* — it MAY import store + model + core
// + nodeapi, but MUST NEVER import internal/node/* directly.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/control/auth"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
	"github.com/aipo-lenshow/EdgeNest/internal/core/nodeapi"
)

// Orchestrator builds DesiredConfig from DB state and pushes via NodeClient.
type Orchestrator struct {
	store     *store.Store
	node      nodeapi.NodeClient
	panelPort int // safe-mode: always preserved in AllowPorts; 0 = unknown/skip
}

// New constructs an Orchestrator with panelPort=0 (no safe-mode reservation).
// Use NewWithPanelPort to enable safe-mode.
func New(s *store.Store, nc nodeapi.NodeClient) *Orchestrator {
	return &Orchestrator{store: s, node: nc}
}

// NewWithPanelPort constructs an Orchestrator that pins the panel's HTTP port
// into every firewall payload (safe-mode I7: never lock the operator out).
func NewWithPanelPort(s *store.Store, nc nodeapi.NodeClient, panelPort int) *Orchestrator {
	return &Orchestrator{store: s, node: nc, panelPort: panelPort}
}

// sshPort reads the SSH port from settings. Returns 22 (default) if unset.
// A stored value of "0" disables the SSH safe-mode rule (container deploy).
func (o *Orchestrator) sshPort() int {
	v, _ := o.store.GetSetting("ssh_port")
	if v == "" {
		return 22
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 22
	}
	return n
}

// BuildDesired aggregates inbounds + clients + routes + firewall + warp + certs
// + advanced for a node into a DesiredConfig the node can render.
//
// Disabled inbounds are skipped; disabled clients are skipped.
//
// Returns the assembled DesiredConfig plus the list of effective firewall port
// rules that the caller may persist as managed rules (panel-managed firewall
// state is derived from enabled inbounds — see SyncManagedFirewall).
func (o *Orchestrator) BuildDesired(nodeID uint) (core.DesiredConfig, error) {
	var cfg core.DesiredConfig

	inbounds, err := o.store.ListInbounds(nodeID)
	if err != nil {
		return cfg, fmt.Errorf("list inbounds: %w", err)
	}
	for _, ib := range inbounds {
		if !ib.Enabled {
			continue
		}
		spec, err := buildInboundSpec(ib)
		if err != nil {
			return cfg, fmt.Errorf("build inbound %q: %w", ib.Tag, err)
		}
		cfg.Inbounds = append(cfg.Inbounds, spec)
	}

	// Firewall: derive panel-managed allow ports (inbound-derived + safe-mode
	// SSH/panel reservations) then layer in user-managed rules from the DB.
	// Invariant I7: we NEVER push a firewall payload that would drop the SSH
	// or admin connection. We deliberately keep the managed subset distinct
	// from user rules so Apply only writes the *managed* slice back to the
	// firewall_rules table — user rules are the operator's source of truth
	// and must never be flagged Managed or have their notes overwritten.
	_, full := o.computeAllowPorts(nodeID, inbounds)
	cfg.Firewall = core.FirewallSpec{AllowPorts: full, PortHops: collectPortHops(cfg.Inbounds)}

	// Routes.
	routes, err := o.store.ListRouteRules(nodeID)
	if err != nil {
		return cfg, fmt.Errorf("list routes: %w", err)
	}
	for _, r := range routes {
		if !r.Enabled {
			continue
		}
		cfg.Routes = append(cfg.Routes, core.RouteSpec{
			Type: r.Type, Value: r.Value, Outbound: r.Outbound,
		})
	}

	// WARP.
	if w, err := o.store.GetWarp(nodeID); err != nil {
		return cfg, fmt.Errorf("get warp: %w", err)
	} else if w != nil && w.Enabled {
		cfg.Warp = &core.WarpSpec{
			Enabled:    true,
			PrivateKey: w.PrivateKey,
			PublicKey:  w.PublicKey,
			Address4:   w.Address4,
			Address6:   w.Address6,
			Reserved:   parseReserved(w.Reserved),
			Endpoint:   w.Endpoint,
		}
	}

	// Certs (informational; the inbounds reference their cert paths directly).
	if certs, err := o.store.ListCertificates(nodeID); err != nil {
		return cfg, fmt.Errorf("list certs: %w", err)
	} else {
		for _, c := range certs {
			cfg.Certs = append(cfg.Certs, core.CertSpec{
				Domain: c.Domain, CertPath: c.CertPath, KeyPath: c.KeyPath,
			})
		}
	}

	// Advanced (default off).
	if a, err := o.store.GetAdvanced(nodeID); err != nil {
		return cfg, fmt.Errorf("get advanced: %w", err)
	} else if a != nil {
		cfg.Advanced = &core.AdvancedSpec{
			CDNEnabled:      a.CDNEnabled,
			CDNPreferredIPs: parseJSONStringSlice(a.CDNPreferredIPs),
			ArgoEnabled:     a.ArgoEnabled,
			ArgoMode:        a.ArgoMode,
			ArgoDomain:      a.ArgoDomain,
			ArgoToken:       a.ArgoToken,
			BlockQUIC:       a.BlockQUIC,
		}
	}

	// clash_api: always on so the panel's live domain-capture can poll it any
	// time. Loopback + a persistent secret (generated once). Behaviour-neutral.
	if secret, err := o.clashSecret(); err != nil {
		return cfg, fmt.Errorf("clash secret: %w", err)
	} else {
		cfg.ClashAPI = &core.ClashAPISpec{Controller: ClashController, Secret: secret}
	}

	// v2ray_api: always on so per-user traffic counters exist for quota
	// enforcement. Loopback gRPC, no secret (sing-box serves it insecure), so it
	// is internal telemetry only. The rendered users list is derived from the
	// inbound clients by the sing-box renderer.
	cfg.V2RayAPI = &core.V2RayAPISpec{Controller: V2RayController}

	// xray_api: the same, for inbounds hosted on the optional xray-core engine.
	// Harmless when xray isn't installed / has no inbounds (the renderer only
	// emits a config when there are xray inbounds, and the poller's xray source
	// just errors and is skipped).
	cfg.XRayAPI = &core.XRayAPISpec{Controller: XRayController}

	return cfg, nil
}

// ClashController is the loopback address sing-box's clash_api binds to. Fixed
// and loopback-only: it is internal telemetry, never reachable off-box.
const ClashController = "127.0.0.1:9090"

// V2RayController is the loopback gRPC address sing-box's v2ray_api StatsService
// binds to. Loopback-only and unauthenticated (sing-box serves it insecure), so
// it is never reachable off-box. The quota traffic poller dials it.
const V2RayController = "127.0.0.1:9091"

// XRayController is the loopback gRPC address xray-core's StatsService (api app)
// binds to. Loopback-only and unauthenticated, never reachable off-box. The
// quota traffic poller dials it on the same v2ray-core service path as sing-box.
const XRayController = "127.0.0.1:9092"

// clashSecretKey is the settings key holding the clash_api bearer secret.
const clashSecretKey = "clash_api_secret"

// ClashSecret exposes the persistent clash_api secret so the API layer can
// authenticate to the same loopback controller the rendered config enables.
func (o *Orchestrator) ClashSecret() (string, error) { return o.clashSecret() }

// clashSecret returns the persistent clash_api secret, generating and storing it
// on first use so the controller is never left unauthenticated.
func (o *Orchestrator) clashSecret() (string, error) {
	if s, err := o.store.GetSetting(clashSecretKey); err != nil {
		return "", err
	} else if s != "" {
		return s, nil
	}
	s, err := auth.RandomHex(16)
	if err != nil {
		return "", err
	}
	if err := o.store.SetSetting(clashSecretKey, s); err != nil {
		return "", err
	}
	return s, nil
}

// Apply: build → push through NodeClient → record managed firewall rules in DB
// (so the UI's "managed by panel" tab stays consistent).
//
// nodeID is the model.Node.ID; nodeClientID is the same value as a string
// (NodeClient identifies nodes by string).
func (o *Orchestrator) Apply(ctx context.Context, nodeID uint) (core.ApplyResult, error) {
	cfg, err := o.BuildDesired(nodeID)
	if err != nil {
		return core.ApplyResult{OK: false, Message: "build desired: " + err.Error()}, nil
	}
	res, err := o.node.ApplyConfig(ctx, strconv.FormatUint(uint64(nodeID), 10), cfg)
	if err != nil {
		return res, err
	}
	if res.OK {
		// Re-derive the panel-managed subset (inbound-derived + safe-mode) so
		// we only sync those rows. User rules (already in the DB) are left
		// untouched — sync owns just the rules the panel itself created.
		inbounds, lerr := o.store.ListInbounds(nodeID)
		if lerr != nil {
			res.Message = res.Message + " (firewall sync warning: " + lerr.Error() + ")"
			return res, nil
		}
		managed, _ := o.computeAllowPorts(nodeID, inbounds)
		if err := o.syncManagedFirewallLocked(nodeID, managed); err != nil {
			// Don't fail the apply over a bookkeeping miss — log via Message.
			res.Message = res.Message + " (firewall sync warning: " + err.Error() + ")"
		}
	}
	return res, nil
}

// computeAllowPorts returns two views of the per-node firewall allow list:
//
//   - managed: rules the panel itself owns (inbound-derived ports +
//     safe-mode SSH/panel reservations the user hasn't already covered).
//     These get written back to the firewall_rules table as Managed=true.
//   - full: managed ∪ user-managed rules from the DB. This is what the
//     engine actually needs so packets flow for user-added ports too, but
//     the user rows must NEVER be re-flagged Managed.
//
// Safe-mode is suppressed for any (port, proto) the user has already
// declared: the user's own rule keeps SSH/panel reachable, and emitting a
// parallel managed row would clobber the user's note in syncManagedFirewall.
// Splitting the two views is what stops user rule notes from being
// clobbered by safe-mode (the bug TestApply_SyncsManagedFirewallTable
// guards against).
func (o *Orchestrator) computeAllowPorts(nodeID uint, inbounds []model.Inbound) (managed, full []core.PortRule) {
	userRules, _ := o.store.ListFirewallRules(nodeID)
	managed = deriveAllowPorts(inbounds)
	managed = mergeSafeModePortsSkippingUser(managed, o.sshPort(), o.panelPort, userRules)
	full = append([]core.PortRule(nil), managed...)
	full = mergeUserManagedPorts(full, userRules)
	return managed, full
}

// syncManagedFirewallLocked makes the DB's panel-managed firewall rows match
// the effective allow ports. User-managed rules are NEVER touched.
func (o *Orchestrator) syncManagedFirewallLocked(nodeID uint, want []core.PortRule) error {
	have, err := o.store.ListFirewallRules(nodeID)
	if err != nil {
		return err
	}
	// Index existing managed rules by (port, proto).
	idx := map[string]model.FirewallRule{}
	for _, r := range have {
		if !r.Managed {
			continue
		}
		idx[fwKey(r.Port, r.Proto)] = r
	}
	wantSet := map[string]core.PortRule{}
	for _, w := range want {
		wantSet[fwKey(w.Port, w.Proto)] = w
	}
	// Upsert wanted rules.
	for k, w := range wantSet {
		if _, ok := idx[k]; ok {
			continue
		}
		if err := o.store.UpsertManagedFirewallRule(nodeID, w.Port, w.Proto, w.Note); err != nil {
			return err
		}
	}
	// Delete managed rules no longer wanted.
	for k, r := range idx {
		if _, ok := wantSet[k]; ok {
			continue
		}
		if err := o.store.DeleteManagedFirewallRule(nodeID, r.Port, r.Proto); err != nil {
			return err
		}
	}
	return nil
}

func fwKey(port int, proto string) string {
	return strconv.Itoa(port) + "/" + proto
}

// buildInboundSpec converts a stored Inbound (+ Clients) into core.InboundSpec.
func buildInboundSpec(ib model.Inbound) (core.InboundSpec, error) {
	var settings map[string]any
	if ib.Settings != "" {
		if err := json.Unmarshal([]byte(ib.Settings), &settings); err != nil {
			return core.InboundSpec{}, fmt.Errorf("settings json: %w", err)
		}
	}
	clients := make([]core.ClientSpec, 0, len(ib.Clients))
	for _, c := range ib.Clients {
		if !c.Enabled {
			continue
		}
		clients = append(clients, core.ClientSpec{
			Email: c.Email, UUID: c.UUID, Password: c.Password, Flow: c.Flow,
		})
	}
	listen := ib.Listen
	// When the inbound is bound to an Argo tunnel, force it to listen only on
	// the loopback interface — cloudflared connects from the same host and
	// exposing the port publicly would defeat the whole point of routing
	// through Cloudflare. Override regardless of the user-set Listen because
	// argo_bound=true is the stronger statement of intent.
	if asBool(settings["argo_bound"]) {
		listen = "127.0.0.1"
	}
	return core.InboundSpec{
		Tag:      ib.Tag,
		Engine:   ib.Engine,
		Type:     ib.Type,
		Listen:   listen,
		Port:     ib.Port,
		Network:  ib.Network,
		Settings: settings,
		Clients:  clients,
		// SubscriptionHost is the family-source for the per-inbound route
		// pinning rules in sing-box / xray render. Without copying
		// it through here, render's inboundFamily() always sees "" and the
		// v6 pin rule never gets emitted — main reason shipped a
		// sb.json with NO family pinning at all (cf. debug).
		SubscriptionHost: ib.SubscriptionHost,
	}, nil
}

// asBool normalises the settings map's mixed bool/string boolean shape so
// callers don't need to repeat the type switch. Mirrors share/cdn.go's
// truthyBool but lives here to avoid an orchestrator → share import.
func asBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || x == "yes"
	}
	return false
}

// isLoopbackListen reports whether an inbound binds to loopback only. Such a
// port is contacted locally (an Argo origin reached by cloudflared) and never
// from the public internet, so it must be excluded from the firewall allow
// list — both the host iptables rules and the cloud security-group guidance.
func isLoopbackListen(listen string) bool {
	switch strings.TrimSpace(listen) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// deriveAllowPorts collects unique (port, proto) pairs from enabled inbounds.
// proto is normalised: tcp/udp/both.
func deriveAllowPorts(inbounds []model.Inbound) []core.PortRule {
	type k struct {
		Port  int
		Proto string
	}
	seen := map[k]string{}
	var out []core.PortRule
	for _, ib := range inbounds {
		if !ib.Enabled {
			continue
		}
		// Loopback-bound inbounds (e.g. an Argo-bound VMess-WS on 127.0.0.1)
		// are reached locally by cloudflared, never from the public internet,
		// so they need no host firewall rule and no cloud security-group
		// opening. The decision keys on the *listen address*, not the port: a
		// normal VMess-WS that happens to use 2053 on a public IP is still
		// emitted and still needs opening.
		if isLoopbackListen(ib.Listen) {
			continue
		}
		proto := normaliseProto(ib.Type, ib.Network)
		key := k{Port: ib.Port, Proto: proto}
		if _, dup := seen[key]; dup {
			continue
		}
		note := "edgenest:" + ib.Tag
		seen[key] = note
		out = append(out, core.PortRule{Port: ib.Port, Proto: proto, Note: note})
	}
	return out
}

// mergeSafeModePorts adds SSH + panel port to the allow-list if they aren't
// already there. Both rules are emitted with note="edgenest:safe-mode" so the
// firewall sync table makes the intent visible to the operator.
//
// sshPort=0 → skip SSH (operator opted out via setting "ssh_port" = "0").
// panelPort=0 → skip panel (orchestrator was built with the plain New()).
func mergeSafeModePorts(allow []core.PortRule, sshPort, panelPort int) []core.PortRule {
	return mergeSafeModePortsSkippingUser(allow, sshPort, panelPort, nil)
}

// mergeSafeModePortsSkippingUser is mergeSafeModePorts with one extra guard:
// if the operator already has a user-managed rule (Managed=false) at a port
// safe-mode wants to reserve, we don't emit our own managed row. The user's
// rule already keeps the port open, and adding a parallel managed row would
// later overwrite the user's note when syncManagedFirewallLocked runs.
func mergeSafeModePortsSkippingUser(allow []core.PortRule, sshPort, panelPort int, userRules []model.FirewallRule) []core.PortRule {
	userCovers := func(port int, proto string) bool {
		for _, r := range userRules {
			if !r.Managed && r.Port == port && r.Proto == proto {
				return true
			}
		}
		return false
	}
	add := func(port int, proto, note string) {
		for _, r := range allow {
			if r.Port == port && r.Proto == proto {
				return
			}
		}
		if userCovers(port, proto) {
			return
		}
		allow = append(allow, core.PortRule{Port: port, Proto: proto, Note: note})
	}
	if sshPort > 0 {
		add(sshPort, "tcp", "edgenest:safe-mode-ssh")
	}
	if panelPort > 0 {
		add(panelPort, "tcp", "edgenest:safe-mode-panel")
	}
	return allow
}

// mergeUserManagedPorts copies user (Managed=false) firewall rules into the
// allow-list so the engine layer can preserve them when it pushes nft rules.
// User rules are NEVER stripped — the operator is the source of truth.
func mergeUserManagedPorts(allow []core.PortRule, userRules []model.FirewallRule) []core.PortRule {
	for _, r := range userRules {
		if r.Managed {
			continue // managed rules come from the inbound derivation
		}
		dup := false
		for _, a := range allow {
			if a.Port == r.Port && a.Proto == r.Proto {
				dup = true
				break
			}
		}
		if !dup {
			allow = append(allow, core.PortRule{
				Port: r.Port, Proto: r.Proto, Note: "user:" + r.Note,
			})
		}
	}
	return allow
}

// normaliseProto picks the right firewall proto for an inbound based on
// protocol type. Hysteria2 / TUIC need UDP; VLESS/Trojan/SS-2022 over TCP.
// If Network is "both", we go "both". Otherwise we trust Network or fall back
// to per-type defaults.
func normaliseProto(typ, network string) string {
	switch network {
	case "tcp", "udp", "both":
		return network
	}
	switch typ {
	case "hysteria2", "tuic":
		return "udp"
	case "vless", "vless-ws", "vless-xhttp", "vmess", "vmess-ws", "trojan", "shadowsocks", "socks", "anytls":
		return "tcp"
	default:
		return "tcp"
	}
}

// parseReserved decodes a JSON "[a,b,c]" int slice. Empty/invalid → nil.
func parseReserved(raw string) []int {
	if raw == "" {
		return nil
	}
	var v []int
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}

func parseJSONStringSlice(raw string) []string {
	if raw == "" {
		return nil
	}
	var v []string
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}

// collectPortHops turns Hysteria2 inbounds that declared a port-hopping range
// (settings port_hop_start / port_hop_end) into nat redirect rules pointing at
// the inbound's real listen port. Hy2 only — TUIC clients can't declare a port
// range so a redirect would be dead weight. Malformed / partial ranges are
// skipped silently (the wizard validates before save; this is defence in depth).
func collectPortHops(inbounds []core.InboundSpec) []core.PortHopRule {
	var hops []core.PortHopRule
	for _, in := range inbounds {
		if in.Type != "hysteria2" || in.Settings == nil {
			continue
		}
		start := settingInt(in.Settings["port_hop_start"])
		end := settingInt(in.Settings["port_hop_end"])
		if start <= 0 || end < start || in.Port <= 0 {
			continue
		}
		hops = append(hops, core.PortHopRule{Start: start, End: end, ToPort: in.Port})
	}
	return hops
}

// settingInt coerces a settings map value (JSON-decoded, so numbers arrive as
// float64; UI may also send strings) into an int. Returns 0 on anything it
// can't read as a positive integer.
func settingInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(n))
		return i
	}
	return 0
}
