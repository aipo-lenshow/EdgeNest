import { useQuery, useMutation } from "@tanstack/react-query";
import { Fragment, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { api, call } from "../api/client";
import { Badge, Button, Card, PageHeader, fmtTime } from "../components/ui";

// Maps a probed target to the WARP preset group that would relay its egress.
// Unlock checks the host's DIRECT egress, so the prescription for any blocked
// target is "route it through WARP" (changes the egress IP). CDN/Argo front the
// inbound instead and don't help an egress-blocked destination.
// WARP egresses via Cloudflare (typically a western/US IP), so it only helps
// services blocked by a western-geo or bot-detection wall (AI + western
// streaming). It would HURT region-pinned Asian services (Bilibili needs HK/TW,
// Abema needs JP), so those deliberately get no "fix with WARP" prescription.
const RELAY_PRESCRIPTION: Record<string, string> = {
  chatgpt: "ai",
  openai_api: "ai",
  gemini: "ai",
  claude: "ai",
  netflix: "streaming",
  disneyplus: "streaming",
  youtube_premium: "streaming",
  primevideo: "streaming",
};

interface Target {
  id: string;
  name: string;
  url: string;
  category?: string; // ai | streaming | music | social
}

// Mirrors the Go unlock.Status JSON shape exactly: state (not "status"),
// http_status (not "http_code"). The probe response carries no url/category —
// they're merged in from the targets catalogue by id for display.
interface ProbeResult {
  id: string;
  name: string;
  state: string; // unlocked | originals_only | restricted | blocked | error | timeout
  http_status: number;
  region?: string; // detected egress country (lower-case)
  detail: string;
  detail_code?: string; // stable translatable reason key
  latency_ms: number;
}

const CATEGORY_ORDER = ["ai", "streaming", "music", "social"];

// Per-category colour: a header chip + a matching left border on every row in
// the group, so a whole category reads as one colour. Theme-safe (light text
// shade on dark, darker shade on light). Static class strings only.
const CAT_STYLE: Record<string, { chip: string; rowBorder: string }> = {
  ai: {
    chip: "border-emerald-500/40 bg-emerald-500/15 text-emerald-700 dark:text-emerald-300",
    rowBorder: "border-l-emerald-500/50",
  },
  streaming: {
    chip: "border-sky-500/40 bg-sky-500/15 text-sky-700 dark:text-sky-300",
    rowBorder: "border-l-sky-500/50",
  },
  music: {
    chip: "border-violet-500/40 bg-violet-500/15 text-violet-700 dark:text-violet-300",
    rowBorder: "border-l-violet-500/50",
  },
  social: {
    chip: "border-pink-500/40 bg-pink-500/15 text-pink-700 dark:text-pink-300",
    rowBorder: "border-l-pink-500/50",
  },
  other: {
    chip: "border-black/20 bg-black/5 text-black/70 dark:border-white/20 dark:bg-white/10 dark:text-white/70",
    rowBorder: "border-l-black/20 dark:border-l-white/20",
  },
};
const catStyle = (c: string) => CAT_STYLE[c] ?? CAT_STYLE.other;

interface ProbeRun {
  targets: ProbeResult[];
  ran_at: number;
}

export default function UnlockPanel() {
  const { t } = useTranslation();
  const { data: targets = [] } = useQuery({
    queryKey: ["unlock-targets"],
    queryFn: () => call<Target[]>(api.get("/unlock/targets")),
  });
  const [last, setLast] = useState<ProbeRun | null>(null);
  const [warpRun, setWarpRun] = useState<ProbeRun | null>(null);
  const [warpErr, setWarpErr] = useState("");

  const probe = useMutation({
    mutationFn: () => call<ProbeRun>(api.post("/unlock/probe")),
    onSuccess: (r) => {
      setLast(r);
      setWarpRun(null); // a fresh direct run invalidates the old comparison
      setWarpErr("");
    },
  });

  const probeWarp = useMutation({
    mutationFn: () => call<ProbeRun>(api.post("/unlock/probe-warp")),
    onSuccess: (r) => {
      setWarpRun(r);
      setWarpErr("");
    },
    onError: (e: any) =>
      setWarpErr(e?.response?.data?.error?.message ?? e.message),
  });

  const warpById = new Map(
    (warpRun?.targets ?? []).map((r) => [r.id, r]),
  );
  const targetById = new Map(targets.map((tg) => [tg.id, tg]));
  // After a run, the probe results carry no url — merge it back from the
  // targets catalogue. Before any run, show the catalogue with a placeholder
  // state so the user sees what will be probed.
  const rows = last?.targets
    ? last.targets.map((r) => ({
        ...r,
        url: targetById.get(r.id)?.url ?? "",
        category: targetById.get(r.id)?.category ?? "",
      }))
    : targets.map((tg) => ({ ...tg, state: "—" }));

  // Group rows by category for sectioned display.
  const known = new Set(CATEGORY_ORDER);
  const groups: { cat: string; items: any[] }[] = CATEGORY_ORDER.map((cat) => ({
    cat,
    items: rows.filter((r: any) => (r.category || "") === cat),
  })).filter((g) => g.items.length > 0);
  const other = rows.filter((r: any) => !known.has(r.category || ""));
  if (other.length) groups.push({ cat: "other", items: other });
  const colCount = warpRun ? 7 : 6;

  return (
    <>
      <PageHeader
        title={t("unlock.title")}
        subtitle={t("unlock.subtitle")}
        action={
          <div className="flex items-center gap-2">
            <Button
              variant="primary"
              disabled={probe.isPending}
              onClick={() => probe.mutate()}
            >
              {probe.isPending ? t("unlock.probing") : t("unlock.runProbe")}
            </Button>
            <Button
              variant="default"
              disabled={probeWarp.isPending}
              onClick={() => probeWarp.mutate()}
            >
              {probeWarp.isPending
                ? t("unlock.probingWarp")
                : t("unlock.runProbeWarp")}
            </Button>
          </div>
        }
      />

      <details
        className="mb-3 rounded-xl border border-white/10 bg-white/[0.03] px-3 py-2 text-xs"
        open
      >
        <summary className="cursor-pointer select-none font-medium text-white/70">
          {t("unlock.legendTitle")}
        </summary>
        <ul className="mt-2 space-y-1.5 text-white/60">
          <li className="flex items-center gap-2">
            <StatusBadge s="unlocked" /> {t("unlock.legendOk")}
          </li>
          <li className="flex items-center gap-2">
            <StatusBadge s="originals_only" /> {t("unlock.legendOriginals")}
          </li>
          <li className="flex items-center gap-2">
            <StatusBadge s="restricted" /> {t("unlock.legendRestricted")}
          </li>
          <li className="flex items-center gap-2">
            <StatusBadge s="blocked" /> {t("unlock.legendBlocked")}
          </li>
          <li className="flex items-center gap-2">
            <span className="rounded border border-black/15 bg-black/5 px-1 text-[10px] dark:border-white/15 dark:bg-white/5">
              {t("unlock.regionPrefix")}
              <span className="font-mono uppercase"> US</span>
            </span>
            {t("unlock.legendRegion")}
          </li>
        </ul>
        <p className="mt-2 border-t border-white/10 pt-2 text-white/55">
          {t("unlock.legendWarp")}
        </p>
      </details>

      {warpErr && (
        <div className="mb-3 rounded-md border border-red-400/30 bg-red-400/10 p-3 text-sm text-red-300">
          <div className="font-medium">{t("unlock.warpProbeFailed")}</div>
          <div className="mt-1 text-xs text-red-300/70 font-mono break-all">
            {warpErr}
          </div>
        </div>
      )}

      <Card
        title={
          last
            ? t("unlock.lastRun", { time: fmtTime(last.ran_at) })
            : t("unlock.targetsTitle")
        }
      >
        <table className="w-full text-sm">
          <thead className="text-white/50 text-xs uppercase">
            <tr>
              <th className="text-left py-1">{t("unlock.service")}</th>
              <th className="text-left py-1 w-28 whitespace-nowrap">
                {warpRun ? t("unlock.statusDirect") : t("unlock.status")}
              </th>
              {warpRun && (
                <th className="text-left py-1 w-28 whitespace-nowrap">
                  {t("unlock.statusViaWarp")}
                </th>
              )}
              <th className="text-left py-1 w-28 whitespace-nowrap">
                {t("unlock.http")}
                {warpRun && (
                  <span className="text-emerald-400/70"> ·{t("unlock.statusViaWarp")}</span>
                )}
              </th>
              <th className="text-left py-1 w-20 whitespace-nowrap">{t("unlock.latency")}</th>
              <th className="text-left py-1">{t("unlock.detail")}</th>
              <th className="text-left py-1 w-32">{t("unlock.action")}</th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => (
              <Fragment key={g.cat}>
                <tr>
                  <td colSpan={colCount} className="pt-4 pb-1">
                    <span
                      className={`inline-flex items-center rounded-md border px-2 py-0.5 text-xs font-semibold uppercase tracking-wide ${catStyle(g.cat).chip}`}
                    >
                      {t(`unlock.cat_${g.cat}`, { defaultValue: g.cat })}
                    </span>
                  </td>
                </tr>
                {g.items.map((r: any) => {
              const warpR = warpById.get(r.id);
              const directBad = r.state !== "unlocked" && r.state !== "—";
              const warpGood = warpR && warpR.state === "unlocked";
              const warpBad = warpR && warpR.state !== "unlocked";
              const needsRelay = directBad && RELAY_PRESCRIPTION[r.id];
              // After a WARP re-probe, HTTP / latency / detail all describe the
              // WARP result (the "after" state the user is evaluating), not the
              // stale direct values — otherwise "经 WARP 正常" sits next to
              // "HTTP 403" / the direct latency and reads as contradictory. The
              // direct verdict is preserved in the 直连 badge for comparison.
              const shown = warpR ?? r;
              const dCode = shown.detail_code;
              const dRaw = shown.detail;
              return (
                <tr
                  key={r.id}
                  className={`border-t border-black/5 dark:border-white/5 border-l-2 ${catStyle(g.cat).rowBorder}`}
                >
                  <td className="py-2 pl-2">
                    <div className="text-white">{r.name}</div>
                    <div className="text-xs text-white/40 font-mono break-all">
                      {r.url}
                    </div>
                  </td>
                  <td className="py-2">
                    <StatusBadge s={r.state} region={r.region} />
                  </td>
                  {warpRun && (
                    <td className="py-2">
                      {warpById.has(r.id) ? (
                        <StatusBadge
                          s={warpById.get(r.id)!.state}
                          region={warpById.get(r.id)!.region}
                          warp
                        />
                      ) : (
                        "—"
                      )}
                    </td>
                  )}
                  <td className="py-2 font-mono">
                    {shown.http_status ? shown.http_status : "—"}
                  </td>
                  <td className="py-2 font-mono">
                    {shown.latency_ms ? `${shown.latency_ms} ms` : "—"}
                  </td>
                  <td className="py-2 text-white/60 text-xs" title={dRaw ?? ""}>
                    {dCode ? t(`unlock.detail_${dCode}`) : (dRaw ?? "")}
                  </td>
                  <td className="py-2">
                    {directBad && warpGood ? (
                      <span className="text-xs text-emerald-400">
                        {t("unlock.warpRecovered")}
                      </span>
                    ) : directBad && warpBad ? (
                      <span className="text-xs text-white/40">
                        {t("unlock.warpNoHelp")}
                      </span>
                    ) : needsRelay ? (
                      <Link
                        to={`/outbound?tab=routes&preset=${RELAY_PRESCRIPTION[r.id]}`}
                        className="text-xs text-emerald-300 hover:text-emerald-200 underline-offset-2 hover:underline"
                      >
                        {t("unlock.fixWithWarp")}
                      </Link>
                    ) : null}
                  </td>
                </tr>
              );
                })}
              </Fragment>
            ))}
          </tbody>
        </table>
      </Card>

      <div className="mt-4 text-xs text-white/40">{t("unlock.hint")}</div>
    </>
  );
}

// State → (label key, semantic hue). The hue keeps green=ok / red=blocked /
// amber=partial regardless of which column it's in.
const STATE_META: Record<string, { key: string; hue: "emerald" | "red" | "amber" }> = {
  unlocked: { key: "statusOk", hue: "emerald" },
  originals_only: { key: "statusOriginalsOnly", hue: "amber" },
  restricted: { key: "statusRestricted", hue: "amber" },
  blocked: { key: "statusBlocked", hue: "red" },
  timeout: { key: "statusTimeout", hue: "amber" },
  error: { key: "statusError", hue: "amber" },
};

// Direct column = filled badge. WARP column = outlined + brighter text, so the
// two adjacent status columns are unmistakable at a glance while still encoding
// ok/blocked by colour. Static class strings (no dynamic Tailwind).
const WARP_OUTLINE: Record<string, string> = {
  emerald: "border-emerald-400 text-emerald-600 dark:text-emerald-300",
  red: "border-red-400 text-red-600 dark:text-red-300",
  amber: "border-amber-400 text-amber-600 dark:text-amber-300",
};
const DIRECT_TONE: Record<string, "success" | "danger" | "warn"> = {
  emerald: "success",
  red: "danger",
  amber: "warn",
};

// State strings come straight from the Go unlock.Status: unlocked |
// originals_only | restricted | blocked | error | timeout (plus "—" placeholder
// before a run). region, when set, is shown as a small chip beside the badge.
function StatusBadge({
  s,
  region,
  warp = false,
}: {
  s: string;
  region?: string;
  warp?: boolean;
}) {
  const { t } = useTranslation();
  const meta = STATE_META[s];
  const badge = !meta ? (
    <Badge tone="neutral">{s}</Badge>
  ) : warp ? (
    <span
      className={`inline-flex items-center rounded-md border bg-transparent px-1.5 py-0.5 text-[11px] font-semibold ${WARP_OUTLINE[meta.hue]}`}
    >
      {t(`unlock.${meta.key}`)}
    </span>
  ) : (
    <Badge tone={DIRECT_TONE[meta.hue]}>{t(`unlock.${meta.key}`)}</Badge>
  );
  return (
    <span className="inline-flex items-center gap-1">
      {badge}
      {region && (
        <span
          title={t("unlock.regionTip")}
          className="rounded border border-black/15 bg-black/5 px-1 text-[10px] text-black/60 dark:border-white/15 dark:bg-white/5 dark:text-white/60"
        >
          {t("unlock.regionPrefix")}
          <span className="font-mono uppercase"> {region}</span>
        </span>
      )}
    </span>
  );
}
