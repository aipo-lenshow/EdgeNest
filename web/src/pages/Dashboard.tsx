import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import Layout from "../components/Layout";
import { PageHeader, fmtBytes } from "../components/ui";
import SystemInfoPage from "./SystemInfo";

interface DashboardData {
  engine: { running: boolean; version: string; detail?: string };
  health: { singbox_running: boolean; bbr: string };
  nodes: number;
}

interface StatsSummary {
  total_up: number;
  total_down: number;
  enabled_users: number;
  over_quota: number;
}

export default function Dashboard() {
  const { t } = useTranslation();

  const { data: dash } = useQuery({
    queryKey: ["dashboard"],
    queryFn: () => call<DashboardData>(api.get("/dashboard")),
  });
  const { data: stats } = useQuery({
    queryKey: ["stats-summary"],
    queryFn: () => call<StatsSummary>(api.get("/stats/summary")),
  });
  // Global WARP egress state — a node-wide outbound (not per-inbound).
  const { data: warp } = useQuery({
    queryKey: ["warp"],
    queryFn: () => call<{ enabled: boolean }>(api.get("/warp")),
    retry: false,
  });

  return (
    <Layout>
      <PageHeader
        title={t("dashboard.title")}
        subtitle={t("dashboard.subtitle")}
      />

      <div className="grid gap-6">
        {/* System info + Xray cards, merged in from the former /system page. */}
        <SystemInfoPage embedded />

        {/* At-a-glance status tiles below the system/Xray cards. */}
        <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-4">
          <Stat label={t("dashboard.statNodes")} value={String(dash?.nodes ?? "—")} />
          <Stat
            label={t("dashboard.statEngine")}
            value={
              dash?.engine.running
                ? t("dashboard.engineRunning")
                : t("dashboard.engineStopped")
            }
            sub={dash?.engine.version}
            tone={
              dash?.engine.running === undefined
                ? undefined
                : dash.engine.running
                ? "ok"
                : "warn"
            }
          />
          <Stat
            label={t("dashboard.statBbr")}
            value={dash?.health.bbr ?? "—"}
            tone={dash?.health.bbr?.startsWith("bbr") ? "ok" : undefined}
          />
          <Link to="/outbound?tab=warp" className="block">
            <Stat
              label={t("dashboard.statWarp")}
              value={
                warp?.enabled
                  ? t("dashboard.warpOn")
                  : t("dashboard.warpOff")
              }
              sub={warp?.enabled ? t("dashboard.warpOnSub") : undefined}
              tone={warp?.enabled ? "ok" : undefined}
            />
          </Link>
          <Link to="/stats" className="block">
            <Stat
              label={t("dashboard.statClients")}
              value={String(stats?.enabled_users ?? 0)}
              sub={
                stats?.over_quota
                  ? t("dashboard.overQuotaCount", { count: stats.over_quota })
                  : t("dashboard.allUnderQuota")
              }
            />
          </Link>
          <Link to="/stats" className="block">
            <TrafficTile
              up={stats?.total_up ?? 0}
              down={stats?.total_down ?? 0}
            />
          </Link>
        </div>
      </div>
    </Layout>
  );
}

function Stat({
  label,
  value,
  sub,
  tone,
}: {
  label: string;
  value: string;
  sub?: string;
  // tone colours the value: "ok" (green) for a running/healthy state,
  // "warn" (amber) for a stopped/degraded one. Undefined = neutral white.
  tone?: "ok" | "warn";
}) {
  const valueColor =
    tone === "ok"
      ? "text-emerald-300"
      : tone === "warn"
      ? "text-amber-300"
      : "";
  return (
    <div className="rounded-2xl border border-white/10 bg-white/5 p-5 h-full">
      <div className="text-xs uppercase tracking-wide text-white/40">
        {label}
      </div>
      <div className={`text-2xl font-semibold mt-1 ${valueColor}`}>{value}</div>
      {sub && <div className="text-xs text-white/40 mt-1">{sub}</div>}
    </div>
  );
}

// TrafficTile packs both directions into one compact tile (the former full-width
// traffic card, shrunk to sit in the status row). Links to the per-user view.
function TrafficTile({ up, down }: { up: number; down: number }) {
  const { t } = useTranslation();
  return (
    <div className="rounded-2xl border border-white/10 bg-white/5 p-5 h-full">
      <div className="text-xs uppercase tracking-wide text-white/40">
        {t("dashboard.trafficTitle")}
      </div>
      <div className="mt-1.5 grid gap-1 text-sm">
        <div className="flex items-baseline justify-between gap-2">
          <span className="text-white/40 text-xs">{t("dashboard.upload")}</span>
          <span className="font-semibold">{fmtBytes(up)}</span>
        </div>
        <div className="flex items-baseline justify-between gap-2">
          <span className="text-white/40 text-xs">{t("dashboard.download")}</span>
          <span className="font-semibold">{fmtBytes(down)}</span>
        </div>
      </div>
    </div>
  );
}
