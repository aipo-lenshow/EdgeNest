import { useMutation } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import { Badge, Button, Card, Field, Input, Toggle } from "./ui";

interface SpeedResult {
  ip: string;
  reachable: boolean;
  latency_ms: number;
  speed_mbps: number;
  error?: string;
}

// isValidCdnEntry accepts an IPv4/IPv6 literal or a bare hostname — the pool can
// hold either (a CF anycast IP or an optimization domain that resolves to one).
function isValidCdnEntry(s: string): boolean {
  const v = s.trim();
  if (!v) return false;
  // IPv4
  if (/^(\d{1,3}\.){3}\d{1,3}$/.test(v)) {
    return v.split(".").every((o) => Number(o) >= 0 && Number(o) <= 255);
  }
  // IPv6 (loose — any hex/colon group)
  if (/^[0-9a-fA-F:]+$/.test(v) && v.includes(":")) return true;
  // hostname
  return /^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$/.test(
    v,
  );
}

interface CdnCardProps {
  enabled: boolean;
  onEnabledChange: (v: boolean) => void;
  ips: string[];
  onIpsChange: (v: string[]) => void;
}

// CdnCard renders the CDN preferred-IP section: an opt-in toggle, a chip-based
// IP/domain editor, and a server-side speed test that ranks Cloudflare candidate
// IPs by measured latency (and, with deep test on, download throughput).
//
// Interaction model (deliberately minimal): the result list's checkboxes ARE the
// pool — ticking an IP adds it, unticking removes it, so what's checked always
// mirrors the chips above. One button, "adopt fastest N", ticks the top N for
// the user. The list is pre-sorted best-first by latency (handshake RTT) — a
// highly reproducible signal — so ranking and the recommendation are stable
// run-to-run. Deep test adds a download throughput figure, but it's shown as an
// informational, "varies between runs" reference, not the ranking key.
//
// State for `enabled` + `ips` lives in the parent so the page's single Save
// (PUT /advanced) persists them; this component only edits in place.
export default function CdnCard({
  enabled,
  onEnabledChange,
  ips,
  onIpsChange,
}: CdnCardProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState("");
  const [draftErr, setDraftErr] = useState("");
  const [results, setResults] = useState<SpeedResult[] | null>(null);
  // Deep test additionally downloads a few MB through each reachable edge (one
  // at a time, so the VPS uplink isn't split) and reports throughput (Mbps) as a
  // reference figure — slower than the latency-only sweep. Ranking still follows
  // latency; throughput just hints at which close edge also pulls fast.
  const [deep, setDeep] = useState(false);
  // candidates is the refreshed probe set ("refresh candidate pool"); null means
  // use the server's static curated reps. Held in state only (not persisted) —
  // it's a "try harder" input to the next sweep, not saved config.
  const [candidates, setCandidates] = useState<string[] | null>(null);

  const addEntry = () => {
    const v = draft.trim();
    if (!v) return;
    if (!isValidCdnEntry(v)) {
      setDraftErr(t("advanced.cdnInvalidEntry"));
      return;
    }
    if (ips.includes(v)) {
      setDraft("");
      return;
    }
    onIpsChange([...ips, v]);
    setDraft("");
    setDraftErr("");
  };

  const removeEntry = (ip: string) => onIpsChange(ips.filter((x) => x !== ip));
  const addToPool = (ip: string) =>
    onIpsChange(Array.from(new Set([...ips, ip])));
  const inPool = (ip: string) => ips.includes(ip);
  // Checkbox ⟷ pool membership: tick to add, untick to remove.
  const togglePool = (ip: string) =>
    inPool(ip) ? removeEntry(ip) : addToPool(ip);

  const speedtest = useMutation({
    mutationFn: (probeIps: string[]) =>
      call<{ results: SpeedResult[]; best: string[] }>(
        api.post("/advanced/cdn/speedtest", {
          ips: probeIps,
          download: deep,
        }),
      ),
    onSuccess: (data) => setResults(data.results),
  });

  const refresh = useMutation({
    mutationFn: () =>
      call<{ candidates: string[] }>(
        api.post("/advanced/cdn/candidates/refresh", { n: 48 }),
      ),
    onSuccess: (data) => {
      setCandidates(data.candidates);
      // Re-run the sweep immediately against the fresh set — one click, updated
      // candidates AND re-tested, no second button press.
      speedtest.mutate(data.candidates);
    },
  });

  const runTest = () => speedtest.mutate(candidates ?? []);

  // Backend returns results already sorted by latency (lowest first) with
  // unreachable last.
  const reachable = useMemo(
    () => (results ?? []).filter((r) => r.reachable),
    [results],
  );
  const unreachableCount = useMemo(
    () => (results ?? []).filter((r) => !r.reachable).length,
    [results],
  );

  const clearPool = () => onIpsChange([]);

  const busy = speedtest.isPending || refresh.isPending;

  return (
    <Card title={t("advanced.cardCdn")}>
      <div className="space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Toggle
            checked={enabled}
            onChange={onEnabledChange}
            label={t("advanced.enableCdnLabel")}
          />
          {ips.length > 0 && (
            <Button variant="danger" onClick={clearPool}>
              {t("advanced.cdnClearPool")}
            </Button>
          )}
        </div>

        <Field
          label={t("advanced.fieldPreferredIps")}
          hint={t("advanced.fieldPreferredIpsHint")}
        >
          <div className="space-y-2">
            <div className="rounded-md border border-white/10 bg-white/5 px-3 py-2 text-xs leading-relaxed">
              <div className="font-medium text-white/85">
                {t("advanced.cdnPreferOneLead")}
              </div>
              <div className="mt-1 text-white/50">
                {t("advanced.cdnPreferOneTip")}
              </div>
            </div>
            {ips.length > 0 && (
              <div className="flex flex-wrap gap-2">
                {ips.map((ip) => (
                  <span
                    key={ip}
                    className="inline-flex items-center gap-1.5 rounded-full bg-white/10 px-3 py-1 text-sm text-white/90"
                  >
                    <code>{ip}</code>
                    <button
                      type="button"
                      className="text-white/50 hover:text-white"
                      onClick={() => removeEntry(ip)}
                      aria-label={t("common.delete")}
                    >
                      ×
                    </button>
                  </span>
                ))}
              </div>
            )}
            <div className="flex gap-2">
              <Input
                value={draft}
                onChange={(e) => {
                  setDraft(e.target.value);
                  setDraftErr("");
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    addEntry();
                  }
                }}
                placeholder={t("advanced.cdnAddPlaceholder")}
              />
              <Button variant="ghost" onClick={addEntry}>
                {t("advanced.cdnAddBtn")}
              </Button>
            </div>
            {draftErr && <div className="text-xs text-red-400">{draftErr}</div>}
          </div>
        </Field>

        <div className="flex flex-wrap items-center gap-4">
          <Button variant="primary" disabled={busy} onClick={runTest}>
            {speedtest.isPending
              ? t("advanced.cdnTesting")
              : t("advanced.cdnSpeedtest")}
          </Button>
          <Toggle
            checked={deep}
            onChange={setDeep}
            label={t("advanced.cdnDeepTest")}
          />
          <Button
            variant="primary"
            className="ml-auto"
            disabled={busy}
            onClick={() => refresh.mutate()}
          >
            {refresh.isPending
              ? t("advanced.cdnRefreshing")
              : t("advanced.cdnRefreshCandidates")}
          </Button>
        </div>
        {deep && (
          <div className="text-xs text-white/40">
            {t("advanced.cdnDeepTestHint")}
          </div>
        )}
        <div className="text-xs text-white/40">
          {t("advanced.cdnRefreshTip")}
          {candidates && (
            <span className="ml-1 text-white/30">
              {t("advanced.cdnCandidateCount", { n: candidates.length })}
            </span>
          )}
        </div>

        {results && (
          <div className="rounded-md border border-white/10 bg-white/5 p-3">
            <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
              <span className="text-xs uppercase tracking-wide text-white/50">
                {t("advanced.cdnSpeedtestResults")}
              </span>
            </div>

            <div className="mb-2 text-xs text-white/40">
              {t("advanced.cdnSelectHint")}
            </div>

            {reachable.length === 0 ? (
              <div className="text-sm text-white/60">
                {t("advanced.cdnNoReachable")}
              </div>
            ) : (
              <div className="max-h-44 space-y-1 overflow-y-auto pr-1">
                {reachable.map((r, i) => (
                  <label
                    key={r.ip}
                    className="flex cursor-pointer items-center justify-between gap-2 rounded px-1 py-0.5 text-sm hover:bg-white/5"
                  >
                    <span className="flex items-center gap-2">
                      <input
                        type="checkbox"
                        checked={inPool(r.ip)}
                        onChange={() => togglePool(r.ip)}
                        className="h-3.5 w-3.5 accent-emerald-500"
                      />
                      <code className="text-white/80">{r.ip}</code>
                      {i === 0 && (
                        <Badge tone="success">
                          {t("advanced.cdnRecommended")}
                        </Badge>
                      )}
                    </span>
                    <span className="flex items-center gap-2">
                      {r.speed_mbps > 0 && (
                        <span title={t("advanced.cdnSpeedRefHint")}>
                          <Badge
                            tone={
                              r.speed_mbps >= 50
                                ? "success"
                                : r.speed_mbps >= 10
                                  ? "warn"
                                  : "neutral"
                            }
                          >
                            ≈ {r.speed_mbps.toFixed(1)} Mbps
                          </Badge>
                        </span>
                      )}
                      <Badge
                        tone={
                          r.latency_ms < 100
                            ? "success"
                            : r.latency_ms < 400
                              ? "warn"
                              : "neutral"
                        }
                      >
                        {r.latency_ms} ms
                      </Badge>
                    </span>
                  </label>
                ))}
              </div>
            )}

            {unreachableCount > 0 && (
              <div className="mt-2 text-xs text-white/40">
                {t("advanced.cdnUnreachableHidden", { n: unreachableCount })}
              </div>
            )}
            {reachable.some((r) => r.speed_mbps > 0) && (
              <div className="mt-2 text-xs text-white/40">
                {t("advanced.cdnSpeedRefNote")}
              </div>
            )}
            <div className="mt-2 text-xs text-white/40">
              {t("advanced.cdnSpeedtestHint")}
            </div>
          </div>
        )}
      </div>
    </Card>
  );
}
