import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import Layout from "../components/Layout";
import {
  Badge,
  Button,
  PageHeader,
  fmtBytes,
  fmtTime,
} from "../components/ui";

interface ProtoTag {
  type: string;
  engine: string;
  network: string;
  port: number;
  cdn: boolean;
  argo: boolean;
}

interface UserStat {
  email: string;
  enabled: boolean;
  traffic_up: number;
  traffic_down: number;
  quota_bytes: number;
  quota_used_pct: number; // -1 = unlimited
  expiry_at: number;
  over_quota: boolean;
  expired: boolean;
  protocols: ProtoTag[];
}

interface StatsSummary {
  users: UserStat[];
  total_up: number;
  total_down: number;
  enabled_users: number;
  over_quota: number;
  expired: number;
}

// Compact protocol labels for the per-inbound chips. The inbound `type` already
// distinguishes WS/XHTTP variants, so a small map is enough.
const PROTO_SHORT: Record<string, string> = {
  vless: "VLESS-Reality",
  "vless-ws": "VLESS-WS",
  "vless-xhttp": "VLESS-XHTTP",
  vmess: "VMess",
  "vmess-ws": "VMess-WS",
  hysteria2: "Hysteria2",
  trojan: "Trojan",
  shadowsocks: "Shadowsocks",
  tuic: "TUIC",
  anytls: "AnyTLS",
  socks: "SOCKS5",
};

function protoShort(type: string): string {
  return PROTO_SHORT[type] ?? type;
}

function protoBadgeClass(type: string): string {
  switch (type) {
    case "vless":
      return "bg-violet-500/15 text-violet-300 border-violet-500/30";
    case "vless-ws":
      return "bg-sky-500/15 text-sky-300 border-sky-500/30";
    case "vless-xhttp":
      return "bg-indigo-500/15 text-indigo-300 border-indigo-500/30";
    case "vmess-ws":
    case "vmess":
      return "bg-fuchsia-500/15 text-fuchsia-300 border-fuchsia-500/30";
    case "hysteria2":
      return "bg-emerald-500/15 text-emerald-300 border-emerald-500/30";
    case "trojan":
      return "bg-pink-500/15 text-pink-300 border-pink-500/30";
    case "shadowsocks":
      return "bg-teal-500/15 text-teal-300 border-teal-500/30";
    case "tuic":
      return "bg-cyan-500/15 text-cyan-300 border-cyan-500/30";
    case "anytls":
      return "bg-orange-500/15 text-orange-300 border-orange-500/30";
    default:
      return "bg-white/5 text-white/70 border-white/15";
  }
}

function isUdp(p: ProtoTag): boolean {
  return (
    p.network === "udp" ||
    p.network === "both" ||
    p.type === "hysteria2" ||
    p.type === "tuic"
  );
}

function ProtoChips({ protos }: { protos: ProtoTag[] }) {
  if (!protos || protos.length === 0) return <span className="text-white/30">—</span>;
  return (
    <div className="flex flex-wrap gap-1">
      {protos.map((p, i) => (
        <span
          key={i}
          title={`:${p.port} · ${p.engine}`}
          className={`inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-[11px] font-medium ${protoBadgeClass(
            p.type,
          )}`}
        >
          {protoShort(p.type)}
          {isUdp(p) && <Mark>UDP</Mark>}
          {p.cdn && <Mark>CDN</Mark>}
          {p.argo && <Mark>Argo</Mark>}
        </span>
      ))}
    </div>
  );
}

function Mark({ children }: { children: React.ReactNode }) {
  return (
    <span className="rounded bg-black/30 px-1 text-[9px] uppercase tracking-wide text-white/70">
      {children}
    </span>
  );
}

// StatsPage is the per-user traffic + quota overview. Aggregated by user to match
// how quota/expiry are actually enforced. Renders standalone or embedded inside
// the Monitor page's "traffic" tab.
export default function StatsPage({ embedded = false }: { embedded?: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ["stats-summary"],
    queryFn: () => call<StatsSummary>(api.get("/stats/summary")),
    // Traffic counters update from the engine poller every ~15s; refetch on the
    // same cadence so the table reflects live usage without a manual reload.
    refetchInterval: 15000,
  });

  const enforce = useMutation({
    mutationFn: () => call(api.post("/quota/enforce")),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["stats-summary"] }),
  });

  const rows = data?.users ?? [];

  const enforceBtn = (
    <Button
      variant="primary"
      disabled={enforce.isPending}
      onClick={() => enforce.mutate()}
    >
      {enforce.isPending ? t("stats.enforcing") : t("stats.runEnforcement")}
    </Button>
  );

  const content = (
    <>
      {embedded && (
        <div className="mb-4 flex items-center justify-between gap-3">
          <p className="text-xs text-white/50 whitespace-nowrap overflow-hidden text-ellipsis">
            {t("stats.runEnforcementHint")}
          </p>
          <div className="shrink-0">{enforceBtn}</div>
        </div>
      )}

      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-4 mb-5">
        <Tile k={t("stats.tileActive")} v={String(data?.enabled_users ?? 0)} />
        <Tile k={t("stats.tileOverQuota")} v={String(data?.over_quota ?? 0)} tone="warn" />
        <Tile k={t("stats.tileExpired")} v={String(data?.expired ?? 0)} tone="warn" />
        <Tile k={t("stats.tileTotalUp")} v={fmtBytes(data?.total_up ?? 0)} />
        <Tile k={t("stats.tileTotalDown")} v={fmtBytes(data?.total_down ?? 0)} />
      </div>

      <div className="rounded-2xl border border-white/10 bg-white/[0.03] overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-white/5 text-white/50 text-xs uppercase">
            <tr>
              <th className="px-3 py-2 text-left w-40">{t("stats.thClient")}</th>
              <th className="px-3 py-2 text-left">{t("stats.thProtocols")}</th>
              <th className="px-3 py-2 text-left w-24">{t("stats.thState")}</th>
              <th className="px-3 py-2 text-right w-28">{t("stats.thUp")}</th>
              <th className="px-3 py-2 text-right w-28">{t("stats.thDown")}</th>
              <th className="px-3 py-2 text-right w-44">
                {t("stats.thQuotaUsage")}
              </th>
              <th className="px-3 py-2 text-left w-44">
                {t("stats.thExpires")}
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && (
              <tr>
                <td
                  colSpan={7}
                  className="px-3 py-8 text-center text-white/40 text-sm"
                >
                  {t("stats.noClients")}
                </td>
              </tr>
            )}
            {rows.map((r) => (
              <tr key={r.email} className="border-t border-white/5 align-top">
                <td className="px-3 py-2 font-mono">{r.email}</td>
                <td className="px-3 py-2">
                  <ProtoChips protos={r.protocols} />
                </td>
                <td className="px-3 py-2">
                  {r.over_quota ? (
                    <Badge tone="danger">{t("stats.badgeOverQuota")}</Badge>
                  ) : r.expired ? (
                    <Badge tone="warn">{t("stats.badgeExpired")}</Badge>
                  ) : !r.enabled ? (
                    <Badge tone="danger">{t("stats.badgeDisabled")}</Badge>
                  ) : (
                    <Badge tone="success">{t("stats.badgeActive")}</Badge>
                  )}
                </td>
                <td className="px-3 py-2 text-right font-mono">
                  {fmtBytes(r.traffic_up)}
                </td>
                <td className="px-3 py-2 text-right font-mono">
                  {fmtBytes(r.traffic_down)}
                </td>
                <td className="px-3 py-2">
                  <QuotaBar pct={r.quota_used_pct} quota={r.quota_bytes} />
                </td>
                <td className="px-3 py-2 text-white/60">
                  {r.expiry_at ? fmtTime(r.expiry_at) : t("stats.expiryNever")}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );

  if (embedded) return content;

  return (
    <Layout>
      <PageHeader
        title={t("stats.title")}
        subtitle={t("stats.subtitle")}
        action={enforceBtn}
      />
      {content}
    </Layout>
  );
}

function Tile({
  k,
  v,
  tone,
}: {
  k: string;
  v: string;
  tone?: "warn";
}) {
  return (
    <div className="rounded-2xl border border-white/10 bg-white/5 p-5">
      <div className="text-xs uppercase tracking-wide text-white/40">{k}</div>
      <div
        className={`text-2xl font-semibold mt-1 ${
          tone === "warn" && v !== "0" ? "text-amber-300" : ""
        }`}
      >
        {v}
      </div>
    </div>
  );
}

function QuotaBar({ pct, quota }: { pct: number; quota: number }) {
  const { t } = useTranslation();
  if (quota === 0)
    return <span className="text-white/40 text-xs">{t("stats.unlimited")}</span>;
  const clamped = Math.max(0, Math.min(100, pct));
  const color =
    clamped >= 100
      ? "bg-red-500"
      : clamped >= 80
      ? "bg-amber-500"
      : "bg-emerald-500";
  return (
    <div>
      <div className="text-xs text-white/60 text-right mb-0.5">
        {t("stats.quotaUsage", {
          pct: clamped.toFixed(0),
          quota: fmtBytes(quota),
        })}
      </div>
      <div className="h-1.5 rounded-full bg-white/10 overflow-hidden">
        <div
          className={`h-full ${color}`}
          style={{ width: `${clamped}%` }}
        />
      </div>
    </div>
  );
}
