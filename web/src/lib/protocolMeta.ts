// Protocol Guide metadata: identity, popularity, gating flags, and per-client
// OS support. All display strings live in i18n; this file is pure structural
// data the page and the wizard share.

export const PROTO_IDS = [
  "vless-reality",
  "hysteria2",
  "shadowsocks-2022",
  "vmess-ws-cdn",
  "trojan-tls",
  "vless-ws-cdn",
  "vless-xhttp-reality",
  "vless-xhttp-tls-cdn",
  "tuic-v5",
  "anytls",
  "socks5",
] as const;
export type ProtoId = (typeof PROTO_IDS)[number];

export const CLIENT_IDS = [
  "shadowrocket",
  "v2rayn",
  "v2rayng",
  "hiddify",
  "stash",
  "surge",
  "singboxgui",
  "karing",
  "mihomoparty",
  "nekobox",
  "loon",
  "quantumultx",
  "clashmi",
] as const;
export type ClientId = (typeof CLIENT_IDS)[number];

export const OS_LIST = ["ios", "macos", "win", "linux", "android"] as const;
export type OS = (typeof OS_LIST)[number];

// "tier" maps to the popularity column on the main table.
// main:  ★★★★★ / ★★★★ (mainstream)
// advanced: ★★★ (used by power users)
// experimental: ★★
// tool: ★ (utility / debug)
export type PopularityTier = "main5" | "main4" | "advanced" | "experimental" | "tool";

// "domain" column:  ✗ (no domain needed) / △ (advisory — works self-signed
// without one, upgrades to a real cert / CDN with one). The old "required"
// member died with the unified certificate model: no protocol hard-requires
// a domain any more, only the CDN/Argo-named accelerations do.
export type DomainGate = "none" | "advisory";

// "cdn" / "argo" columns: yes / no / na (SOCKS5)
export type Acceleration = "yes" | "no" | "na";

// ISP risk: rough indicator of how easily the protocol gets identified or
// throttled on networks where DPI is common. Plain-text protocols (SOCKS5)
// rate high; QUIC-based ones (Hy2 / TUIC v5) sit medium because the QUIC
// signature is itself a fingerprint and home-ISP DPI sometimes throttles
// UDP/443. Encrypted TLS-disguised protocols (Reality / SS-2022 / TLS) sit
// low. Wizard cards and the protocol guide both render a coloured strip
// when the level is not "low".
export type IspRisk = "low" | "medium" | "high";

export interface ProtoMeta {
  id: ProtoId;
  tier: PopularityTier;
  domain: DomainGate;
  cdn: Acceleration;
  argo: Acceleration;
  ispRisk: IspRisk;
  // backend "type" for create-inbound calls. Some UI protocols map to the
  // same backend type with different settings.security (xhttp), so we also
  // carry an optional security hint.
  backendType: string;
  security?: "reality" | "tls" | "none";
}

export const PROTO_META: Record<ProtoId, ProtoMeta> = {
  "vless-reality":      { id: "vless-reality",      tier: "main5",        domain: "none",     cdn: "no",  argo: "no",  ispRisk: "low",    backendType: "vless" },
  hysteria2:            { id: "hysteria2",          tier: "main5",        domain: "none",     cdn: "no",  argo: "no",  ispRisk: "medium", backendType: "hysteria2" },
  "shadowsocks-2022":   { id: "shadowsocks-2022",   tier: "main4",        domain: "none",     cdn: "no",  argo: "no",  ispRisk: "low",    backendType: "shadowsocks" },
  "vmess-ws-cdn":       { id: "vmess-ws-cdn",       tier: "advanced",     domain: "advisory", cdn: "yes", argo: "yes", ispRisk: "low",    backendType: "vmess-ws" },
  "trojan-tls":         { id: "trojan-tls",         tier: "advanced",     domain: "advisory", cdn: "no",  argo: "no",  ispRisk: "low",    backendType: "trojan" },
  "vless-ws-cdn":       { id: "vless-ws-cdn",       tier: "advanced",     domain: "advisory", cdn: "yes", argo: "yes", ispRisk: "low",    backendType: "vless-ws" },
  "vless-xhttp-reality":{ id: "vless-xhttp-reality",tier: "experimental", domain: "none",     cdn: "no",  argo: "no",  ispRisk: "low",    backendType: "vless-xhttp", security: "reality" },
  "vless-xhttp-tls-cdn":{ id: "vless-xhttp-tls-cdn",tier: "experimental", domain: "advisory", cdn: "yes", argo: "yes", ispRisk: "low",    backendType: "vless-xhttp", security: "tls" },
  "tuic-v5":            { id: "tuic-v5",            tier: "advanced",     domain: "none",     cdn: "no",  argo: "no",  ispRisk: "medium", backendType: "tuic" },
  anytls:               { id: "anytls",             tier: "experimental", domain: "none",     cdn: "no",  argo: "no",  ispRisk: "low",    backendType: "anytls" },
  socks5:               { id: "socks5",             tier: "tool",         domain: "none",     cdn: "na",  argo: "na",  ispRisk: "high",   backendType: "socks" },
};

// COMPAT_MATRIX[proto][client] = "all" | OS[] | undefined (unsupported).
// "all" = every OS in OS_LIST. Empty array = client name listed in detail but
// no OS supported (we still flag the row to surface deprecation notes).
//
// Refreshed against 2026-06 reality:
//   - Loon 3.3.0 added VLESS-Reality (Vision) + AnyTLS on iOS
//   - Shadowrocket 2.2.64+ + Stash + sing-box GUI 1.12+ + Mihomo Party / ClashMi
//     all picked up AnyTLS during 2025; v2rayN 7.14.3+ exposes AnyTLS too
//   - Surge 5 still has no VLESS / Reality / XHTTP family (AnyTLS v2 added 2026)
//   - Quantumult X 1.5.5+ accepts VLESS-Reality + VLESS-WS, but Hy2 / TUIC /
//     SS-2022 / XHTTP / AnyTLS issues remain open — keep them off the row
//   - XHTTP transport on sing-box-based clients (Hiddify / sing-box GUI) is
//     marked experimental in sing-box 1.12; only Karing has a stable mod
//   - Mihomo / ClashMi explicitly never ship AnyTLS+Reality combo (upstream
//     refusal), so they're off the Reality column even though AnyTLS-only is OK
type ClientCompat = "all" | OS[];
export const COMPAT_MATRIX: Record<ProtoId, Partial<Record<ClientId, ClientCompat>>> = {
  "vless-reality": {
    hiddify: "all",
    singboxgui: "all",
    karing: "all",
    shadowrocket: ["ios", "macos"],
    stash: ["ios", "macos"],
    v2rayn: ["win"],
    v2rayng: ["android"],
    nekobox: ["win", "android"],
    clashmi: ["ios", "macos", "win", "android"],
    loon: ["ios"],
    quantumultx: ["ios"],
  },
  hysteria2: {
    hiddify: "all",
    singboxgui: "all",
    karing: "all",
    shadowrocket: ["ios", "macos"],
    stash: ["ios", "macos"],
    surge: ["ios", "macos"],
    loon: ["ios"],
    v2rayn: ["win"],
    v2rayng: ["android"],
    mihomoparty: ["macos", "win", "linux"],
    nekobox: ["win", "android"],
    clashmi: ["ios", "macos", "win", "android"],
  },
  "shadowsocks-2022": {
    shadowrocket: "all",
    v2rayn: "all",
    v2rayng: "all",
    hiddify: "all",
    stash: "all",
    surge: "all",
    singboxgui: "all",
    karing: "all",
    mihomoparty: "all",
    nekobox: "all",
    loon: "all",
    clashmi: "all",
  },
  "vmess-ws-cdn": {
    shadowrocket: "all",
    v2rayn: "all",
    v2rayng: "all",
    hiddify: "all",
    stash: "all",
    surge: "all",
    singboxgui: "all",
    karing: "all",
    mihomoparty: "all",
    nekobox: "all",
    loon: "all",
    quantumultx: "all",
    clashmi: "all",
  },
  "trojan-tls": {
    shadowrocket: "all",
    v2rayn: "all",
    v2rayng: "all",
    hiddify: "all",
    stash: "all",
    surge: "all",
    singboxgui: "all",
    karing: "all",
    mihomoparty: "all",
    nekobox: "all",
    loon: "all",
    quantumultx: "all",
    clashmi: "all",
  },
  "vless-ws-cdn": {
    hiddify: "all",
    singboxgui: "all",
    karing: "all",
    shadowrocket: ["ios", "macos"],
    stash: ["ios", "macos"],
    v2rayn: ["win"],
    v2rayng: ["android"],
    nekobox: ["win", "android"],
    clashmi: ["ios", "macos", "win", "android"],
    quantumultx: ["ios"],
    mihomoparty: ["macos", "win", "linux"],
  },
  "vless-xhttp-reality": {
    karing: "all",
    hiddify: "all",
    singboxgui: "all",
    shadowrocket: ["ios", "macos"],
    v2rayn: ["win"],
    v2rayng: ["android"],
    nekobox: ["win", "android"],
  },
  "vless-xhttp-tls-cdn": {
    karing: "all",
    // Hiddify is OFF this row (it IS on vless-xhttp-reality above). XHTTP over
    // standard TLS fails to handshake in hiddify-sing-box even with a real,
    // browser-trusted ACME cert and no insecure flag — proven on-device: the
    // exact same node (security=tls, sni=<domain>, no allowInsecure) proxied
    // real traffic via Shadowrocket / v2rayNG / sing-box from the same phone,
    // while Hiddify alone never completed the connection. XHTTP+Reality works
    // on Hiddify, so the break is XHTTP+standard-TLS specific, not cert/config.
    singboxgui: "all",
    shadowrocket: ["ios", "macos"],
    v2rayn: ["win"],
    v2rayng: ["android"],
    nekobox: ["win", "android"],
    mihomoparty: ["macos", "win", "linux"],
    clashmi: ["ios", "macos", "win", "android"],
  },
  "tuic-v5": {
    hiddify: "all",
    singboxgui: "all",
    karing: "all",
    shadowrocket: ["ios", "macos"],
    stash: ["ios", "macos"],
    mihomoparty: ["macos", "win", "linux"],
    nekobox: ["win", "android"],
    v2rayn: ["win"],
    v2rayng: ["android"],
    clashmi: ["ios", "macos", "win", "android"],
  },
  anytls: {
    singboxgui: "all",
    karing: "all",
    hiddify: "all",
    mihomoparty: ["macos", "win", "linux"],
    shadowrocket: ["ios", "macos"],
    stash: ["ios", "macos"],
    loon: ["ios"],
    v2rayn: ["win"],
    nekobox: ["win", "android"],
    clashmi: ["ios", "macos", "win", "android"],
    surge: ["ios", "macos"],
  },
  socks5: {
    shadowrocket: "all",
    v2rayn: "all",
    v2rayng: "all",
    hiddify: "all",
    stash: "all",
    surge: "all",
    singboxgui: "all",
    karing: "all",
    mihomoparty: "all",
    nekobox: "all",
    loon: "all",
    quantumultx: "all",
    clashmi: "all",
  },
};

export function osSupported(c: ClientCompat | undefined, os: OS): boolean {
  if (!c) return false;
  if (c === "all") return true;
  return c.includes(os);
}

export function clientsForProto(p: ProtoId): ClientId[] {
  return CLIENT_IDS.filter((c) => COMPAT_MATRIX[p][c] !== undefined);
}
