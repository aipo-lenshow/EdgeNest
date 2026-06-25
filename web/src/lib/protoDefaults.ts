// Single source of truth for per-protocol port baselines and the `advanced`
// payload defaults. Shared by the three creation entry points:
//   - Inbounds.tsx (single-inbound form / standard new + advanced mode)
//   - QuickBundleModal.tsx (一键全套 — creates 10 inbounds at once)
//   - InboundWizard.tsx (3-page recommended-flow wizard)
//
// All three MUST stay aligned, otherwise the user sees three different
// "default" ports for the same protocol across the three buttons.

// Non-privileged baseline ports (>= 1024). EdgeNest binds without
// CAP_NET_BIND. A 0–999 random offset is added at create time to avoid
// collisions on retry / parallel bundle.
export const PROTO_DEFAULT_PORTS: Record<string, number> = {
  vless: 8443,
  hysteria2: 41020,
  trojan: 8843,
  shadowsocks: 8388,
  tuic: 50000,
  vmess: 12345,
  "vless-ws": 12346,
  socks: 11080,
  anytls: 8445,
  "vless-xhttp": 8444,
};

export function randomPortFor(proto: string): number {
  const base = PROTO_DEFAULT_PORTS[proto] ?? 20000;
  return base + Math.floor(Math.random() * 1000);
}

// Per-protocol `advanced` payload starter — what each entry point sends as
// `advanced` to POST /inbounds. Server-side autofill mints secrets and
// everything else not specified here.
export const PROTO_ADVANCED_DEFAULTS: Record<string, Record<string, any>> = {
  vless: { sni: "www.apple.com", server_port_target: 443 },
  hysteria2: { obfs: true, up_mbps: 100, down_mbps: 500, sni: "www.bing.com" },
  trojan: { sni: "www.bing.com", acme_managed: false },
  shadowsocks: { method: "2022-blake3-aes-128-gcm" },
  tuic: { congestion_control: "bbr", sni: "www.bing.com", acme_managed: false },
  vmess: { ws_path: "/vmess", ws_host: "" },
  "vless-ws": { ws_path: "/vless", ws_host: "" },
  "vmess-ws": { ws_path: "/vmess", ws_host: "" },
  socks: { require_auth: true, username: "" },
  anytls: { sni: "www.bing.com", acme_managed: false },
  "vless-xhttp": {
    security: "reality",
    sni: "www.apple.com",
    xhttp_path: "/xhttp",
    xhttp_host: "",
  },
};
