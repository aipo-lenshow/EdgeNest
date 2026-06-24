// TopologyDiagram renders one of 14 schematic protocol/concept topologies the
// Protocol Guide uses to show users what flows where. All drawings use
// currentColor + the emerald accent so they track the active theme without
// per-theme styling. The visual style is intentionally simple — boxes,
// arrows, short labels — readability beats render fidelity.
//
// The diagrams are organised by `kind`:
//   - 11 protocol kinds: vless-reality, hysteria2, shadowsocks-2022,
//     vmess-ws-cdn, trojan-tls, vless-ws-cdn, vless-xhttp-reality,
//     vless-xhttp-tls-cdn, tuic-v5, anytls, socks5
//   - 3 concept kinds: cdn, argo, warp
//
// When adding a new kind, supply a switch case below — never inline a new
// kind at a call site. The wrapper handles aspect ratio + theme.

import type { TFunction } from "i18next";

export type TopologyKind =
  | "vless-reality"
  | "hysteria2"
  | "shadowsocks-2022"
  | "vmess-ws-cdn"
  | "trojan-tls"
  | "vless-ws-cdn"
  | "vless-xhttp-reality"
  | "vless-xhttp-tls-cdn"
  | "tuic-v5"
  | "anytls"
  | "socks5"
  | "cdn"
  | "argo"
  | "warp";

export default function TopologyDiagram({
  kind,
  t,
}: {
  kind: TopologyKind;
  t: TFunction;
}) {
  return (
    <div className="w-full overflow-hidden rounded-lg border border-black/10 dark:border-white/10 bg-black/[0.02] dark:bg-white/[0.02] p-3">
      <svg
        viewBox="0 0 600 220"
        xmlns="http://www.w3.org/2000/svg"
        className="w-full h-auto text-black/80 dark:text-white/80"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
      >
        {body(kind, t)}
      </svg>
    </div>
  );
}

function body(kind: TopologyKind, t: TFunction) {
  const emerald = "#10b981";
  const orange = "#f97316";
  const blue = "#3b82f6";

  // Common primitives
  const box = (x: number, y: number, w: number, h: number, label: string, color = "currentColor") => (
    <g>
      <rect x={x} y={y} width={w} height={h} rx={8} stroke={color} />
      <text
        x={x + w / 2}
        y={y + h / 2 + 4}
        textAnchor="middle"
        fontSize="12"
        fill={color}
        stroke="none"
      >
        {label}
      </text>
    </g>
  );

  const arrow = (x1: number, y1: number, x2: number, y2: number, dashed = false, color = "currentColor") => (
    <g>
      <defs>
        <marker
          id={`arrow-${color.replace("#", "")}`}
          viewBox="0 0 10 10"
          refX="9"
          refY="5"
          markerWidth="6"
          markerHeight="6"
          orient="auto-start-reverse"
        >
          <path d="M0,0 L10,5 L0,10 z" fill={color} />
        </marker>
      </defs>
      <line
        x1={x1}
        y1={y1}
        x2={x2}
        y2={y2}
        stroke={color}
        strokeDasharray={dashed ? "5,4" : "0"}
        markerEnd={`url(#arrow-${color.replace("#", "")})`}
      />
    </g>
  );

  const label = (x: number, y: number, text: string, color = "currentColor", size = 11) => (
    <text x={x} y={y} fontSize={size} fill={color} stroke="none" textAnchor="middle">
      {text}
    </text>
  );

  const client = (x: number, y: number) => box(x, y, 100, 50, t("guide.topology.client"));
  const vps = (x: number, y: number, port?: string) =>
    box(x, y, 110, 50, port ? `VPS · ${port}` : "VPS", emerald);
  const cf = (x: number, y: number) => box(x, y, 100, 50, "Cloudflare", orange);

  switch (kind) {
    case "vless-reality":
      return (
        <>
          {client(20, 90)}
          {vps(480, 90, "8443")}
          {label(300, 60, t("guide.topology.realityFake"), blue)}
          {arrow(120, 100, 480, 100, false, blue)}
          {label(300, 130, t("guide.topology.realityActual"), emerald)}
          {arrow(120, 140, 480, 140, false, emerald)}
          {label(300, 170, t("guide.topology.realitySeen"))}
        </>
      );
    case "hysteria2":
      return (
        <>
          {client(20, 90)}
          {vps(480, 90, "41020")}
          {label(300, 70, "UDP · QUIC · BBR", emerald)}
          {/* salamander obfs wave */}
          <path
            d="M 130 115 q 20 -15 40 0 q 20 15 40 0 q 20 -15 40 0 q 20 15 40 0 q 20 -15 40 0 q 20 15 40 0 q 20 -15 40 0 q 20 15 40 0"
            stroke={emerald}
            strokeWidth="1.5"
          />
          {label(300, 150, t("guide.topology.hy2Obfs"))}
        </>
      );
    case "shadowsocks-2022":
      return (
        <>
          {client(20, 90)}
          {vps(480, 90, "8388")}
          {arrow(120, 115, 480, 115)}
          {label(300, 100, "AEAD · Blake3 PSK", emerald)}
          {label(300, 150, t("guide.topology.ssNoTls"))}
        </>
      );
    case "vmess-ws-cdn":
    case "vless-ws-cdn":
      return (
        <>
          {client(20, 90)}
          {cf(250, 90)}
          {vps(480, 90)}
          {arrow(120, 115, 250, 115)}
          {arrow(350, 115, 480, 115, true)}
          {label(185, 95, "WS+TLS")}
          {label(415, 95, t("guide.topology.wsHidden"))}
        </>
      );
    case "trojan-tls":
      return (
        <>
          {client(20, 90)}
          {vps(480, 50, "443")}
          {box(370, 140, 220, 50, t("guide.topology.trojanFakeSite"), blue)}
          {arrow(120, 100, 480, 75)}
          {arrow(120, 140, 480, 165, true, blue)}
          {label(300, 90, t("guide.topology.trojanAuthOk"), emerald)}
          {label(300, 130, t("guide.topology.trojanAuthFail"), blue)}
        </>
      );
    case "vless-xhttp-reality":
      return (
        <>
          {client(20, 90)}
          {vps(480, 90)}
          {arrow(120, 100, 480, 100, false, emerald)}
          {arrow(480, 130, 120, 130, false, emerald)}
          {label(300, 80, "HTTP/2 " + t("guide.topology.xhttpDuplex"), emerald)}
          {label(300, 165, t("guide.topology.xhttpReality"))}
        </>
      );
    case "vless-xhttp-tls-cdn":
      return (
        <>
          {client(20, 90)}
          {cf(250, 90)}
          {vps(480, 90)}
          {arrow(120, 100, 250, 100)}
          {arrow(250, 130, 120, 130)}
          {arrow(350, 100, 480, 100, true)}
          {arrow(480, 130, 350, 130, true)}
          {label(300, 80, "HTTP/2", emerald)}
          {label(300, 165, t("guide.topology.xhttpCdn"))}
        </>
      );
    case "tuic-v5":
      return (
        <>
          {client(20, 90)}
          {vps(480, 90, "50000")}
          <path d="M 120 108 C 250 80 350 130 480 108" stroke={emerald} strokeWidth="1.5" fill="none" />
          <path d="M 120 118 C 250 145 350 90 480 118" stroke={blue} strokeWidth="1.5" fill="none" />
          <path d="M 120 128 C 250 105 350 150 480 128" stroke={orange} strokeWidth="1.5" fill="none" />
          {label(300, 70, "QUIC · BBR", emerald)}
          {label(300, 165, t("guide.topology.tuicMux"))}
        </>
      );
    case "anytls":
      return (
        <>
          {client(20, 90)}
          {vps(480, 90)}
          <path
            d="M 130 110 q 8 -10 16 0 q 8 10 24 0 q 14 -12 28 5 q 12 8 26 -10 q 16 14 32 -3 q 14 -10 28 8 q 12 14 24 -7 q 16 -8 28 5 q 14 12 30 -8 q 12 9 24 0 q 14 -10 30 6 q 10 8 22 -4"
            stroke={emerald}
            strokeWidth="1.5"
          />
          {label(300, 75, t("guide.topology.anytlsObfs"), emerald)}
          {label(300, 165, t("guide.topology.anytlsAdaptive"))}
        </>
      );
    case "socks5":
      return (
        <>
          {client(20, 90)}
          {vps(480, 90)}
          {arrow(120, 115, 480, 115)}
          {label(300, 100, "SOCKS5 plain", blue)}
          {label(300, 165, t("guide.topology.socksLanOnly"))}
        </>
      );
    case "cdn":
      return (
        <>
          {client(20, 90)}
          {cf(250, 90)}
          {vps(480, 90)}
          {arrow(120, 115, 250, 115)}
          {arrow(350, 115, 480, 115, true)}
          {label(185, 100, "HTTPS")}
          {label(415, 100, t("guide.topology.cdnHidden"))}
          {label(300, 165, t("guide.topology.cdnSummary"))}
        </>
      );
    case "argo":
      return (
        <>
          {vps(20, 90, t("guide.topology.argoLocked"))}
          {cf(250, 90)}
          {client(480, 90)}
          {arrow(130, 115, 250, 115, false, orange)}
          {arrow(350, 115, 480, 115)}
          {label(185, 100, "cloudflared", orange)}
          {label(300, 165, t("guide.topology.argoOutbound"))}
        </>
      );
    case "warp":
      return (
        <>
          {client(20, 30)}
          {vps(20, 130)}
          {cf(250, 130)}
          {box(450, 130, 130, 50, t("guide.topology.warpAi"), blue)}
          {arrow(70, 80, 70, 130)}
          {label(135, 70, t("guide.topology.warpInbound"))}
          {arrow(130, 155, 250, 155)}
          {arrow(350, 155, 450, 155, false, orange)}
          {label(300, 145, "WireGuard", orange)}
          {label(515, 195, t("guide.topology.warpSeen"))}
        </>
      );
    default:
      return null;
  }
}
