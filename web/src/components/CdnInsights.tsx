import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import { Badge, Button, Card, ConfirmDialog, ErrorText, Field, Input } from "./ui";

interface AddrStat {
  ip: string;
  samples: number;
  ok_rate: number;
  avg_ms: number;
  ranked: boolean;
}

interface Network {
  key: string;
  label: string;
  samples: number;
  updated_at: number;
  cdn_down: boolean;
  addrs: AddrStat[];
}

interface Candidate {
  ip: string;
  source: "pool" | "builtin";
  good_nets: number;
}

interface NetworksResp {
  networks: Network[] | null;
  candidates: Candidate[] | null;
  window_days: number;
  retention_days: number;
}

interface Policy {
  interval_sec: number;
  same_rounds: number;
  cooldown_sec: number;
}

const shownPerNetwork = 5;

// CdnInsights shows what EdgeNest clients pooled from their own networks (the
// numbers that matter for users, unlike the server-side speed test), the
// candidate list clients are handed, and the manual re-optimise limits. The
// limits save on their own: they only shape the subscription response, so
// they never go through the page's engine-reapplying Save.
export default function CdnInsights() {
  const { t, i18n } = useTranslation();
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  // What the confirm dialog will delete: a whole network, or one address on it.
  const [pendingDel, setPendingDel] = useState<{ net: string; label: string; ip?: string } | null>(null);
  const qc = useQueryClient();
  const del = useMutation({
    mutationFn: (d: { net: string; ip?: string }) =>
      call(api.delete("/advanced/cdn/networks", { params: { net: d.net, ip: d.ip ?? "" } })),
    onSuccess: () => {
      setPendingDel(null);
      qc.invalidateQueries({ queryKey: ["cdn-networks"] });
    },
  });
  const nets = useQuery({
    queryKey: ["cdn-networks"],
    queryFn: () => call<NetworksResp>(api.get("/advanced/cdn/networks")),
    refetchInterval: 60_000,
  });

  const fmtTime = (unix: number) =>
    new Date(unix * 1000).toLocaleString(i18n.language, {
      month: "numeric",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });

  const networks = nets.data?.networks ?? [];
  const candidates = nets.data?.candidates ?? [];

  return (
    <>
      <Card title={t("advanced.cardCdnNetworks")}>
        <div className="space-y-3">
          <div className="text-xs leading-relaxed text-white/50">
            {t("advanced.cdnNetworksIntro", {
              days: nets.data?.window_days ?? 7,
              retention: nets.data?.retention_days ?? 30,
            })}
          </div>
          {nets.error && (
            <ErrorText>
              {t("advanced.cdnNetworksLoadFailed", {
                msg: (nets.error as Error).message,
              })}
            </ErrorText>
          )}
          {nets.data && networks.length === 0 && (
            <div className="text-sm text-white/60">
              {t("advanced.cdnNetworksEmpty")}
            </div>
          )}
          {networks.map((n) => (
            <div
              key={n.key}
              className="rounded-md border border-white/10 bg-white/5 p-3"
            >
              <div className="mb-2 flex flex-wrap items-center gap-2">
                <span className="font-medium text-white/90">
                  {n.label || n.key}
                </span>
                <code className="text-xs text-white/40">{n.key}</code>
                <button
                  type="button"
                  className="text-xs text-red-300/80 hover:text-red-300"
                  onClick={() => setPendingDel({ net: n.key, label: n.label || n.key })}
                >
                  {t("advanced.cdnNetClear")}
                </button>
                <span className="ml-auto text-xs text-white/40">
                  {t("advanced.cdnNetSummary", {
                    addrs: n.addrs.length,
                    n: n.samples,
                  })}{" "}
                  ·{" "}
                  {t("advanced.cdnNetUpdated", { t: fmtTime(n.updated_at) })}
                </span>
              </div>
              {n.cdn_down && (
                <div className="mb-2 text-xs text-amber-300">
                  {t("advanced.cdnNetDown")}
                </div>
              )}
              <div className="space-y-1">
                {(expanded[n.key]
                  ? n.addrs
                  : n.addrs.slice(0, shownPerNetwork)
                ).map((a, i) => (
                  <div
                    key={a.ip}
                    className="flex flex-wrap items-center justify-between gap-2 text-sm"
                  >
                    <span className="flex items-center gap-2">
                      <code className="text-white/80">{a.ip}</code>
                      {a.ranked && i === 0 && (
                        <Badge tone="success">
                          {t("advanced.cdnRecommended")}
                        </Badge>
                      )}
                    </span>
                    <span className="flex items-center gap-2 text-xs text-white/50">
                      <span>
                        {t("advanced.cdnOkRate", {
                          p: Math.round(a.ok_rate * 100),
                        })}
                      </span>
                      <span>{t("advanced.cdnAddrTries", { n: a.samples })}</span>
                      {a.avg_ms > 0 && (
                        <Badge
                          tone={
                            a.avg_ms < 150
                              ? "success"
                              : a.avg_ms < 400
                                ? "warn"
                                : "neutral"
                          }
                        >
                          {a.avg_ms} ms
                        </Badge>
                      )}
                      <button
                        type="button"
                        title={t("advanced.cdnAddrDelete")}
                        className="text-white/30 hover:text-red-300"
                        onClick={() => setPendingDel({ net: n.key, label: n.label || n.key, ip: a.ip })}
                      >
                        ×
                      </button>
                    </span>
                  </div>
                ))}
              </div>
              {n.addrs.length > shownPerNetwork && (
                <button
                  type="button"
                  className="mt-2 text-xs text-emerald-300 hover:text-emerald-200"
                  onClick={() =>
                    setExpanded({ ...expanded, [n.key]: !expanded[n.key] })
                  }
                >
                  {expanded[n.key]
                    ? t("advanced.cdnShowLess")
                    : t("advanced.cdnShowAll", { n: n.addrs.length })}
                </button>
              )}
              {!n.addrs.some((a) => a.ranked) && !n.cdn_down && (
                <div className="mt-2 text-xs text-white/40">
                  {t("advanced.cdnNetNotRanked", {
                    min: 3,
                    rate: 80,
                    max: Math.max(...n.addrs.map((a) => a.samples)),
                  })}
                </div>
              )}
            </div>
          ))}
        </div>
      </Card>

      {candidates.length > 0 && (
        <Card title={t("advanced.cardCdnCandidates")}>
          <div className="mb-3 text-xs leading-relaxed text-white/50">
            {t("advanced.cdnCandidatesIntro")}
          </div>
          <div className="flex flex-wrap gap-2">
            {candidates.map((c) => (
              <span
                key={c.ip}
                className="inline-flex items-center gap-1.5 rounded-full bg-white/10 px-3 py-1 text-sm text-white/90"
              >
                <code>{c.ip}</code>
                <Badge tone={c.source === "pool" ? "success" : "neutral"}>
                  {c.source === "pool"
                    ? t("advanced.cdnSrcPool")
                    : t("advanced.cdnSrcBuiltin")}
                </Badge>
                {c.good_nets > 0 && (
                  <span className="text-xs text-emerald-300">
                    {t("advanced.cdnGoodNets", { n: c.good_nets })}
                  </span>
                )}
              </span>
            ))}
          </div>
        </Card>
      )}

      <CdnPolicyCard />

      <ConfirmDialog
        open={pendingDel !== null}
        title={pendingDel?.ip ? t("advanced.cdnAddrDelete") : t("advanced.cdnNetClear")}
        body={
          <div className="space-y-2 text-sm">
            <p>
              {pendingDel?.ip
                ? t("advanced.cdnAddrDeleteBody", { ip: pendingDel.ip, net: pendingDel.label })
                : t("advanced.cdnNetClearBody", { net: pendingDel?.label ?? "" })}
            </p>
            {del.error && <ErrorText>{(del.error as Error).message}</ErrorText>}
          </div>
        }
        confirmLabel={t("common.delete")}
        cancelLabel={t("common.cancel")}
        variant="danger"
        busy={del.isPending}
        onConfirm={() => pendingDel && del.mutate({ net: pendingDel.net, ip: pendingDel.ip })}
        onCancel={() => setPendingDel(null)}
      />
    </>
  );
}

function CdnPolicyCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["cdn-policy"],
    queryFn: () => call<{ policy: Policy }>(api.get("/advanced/cdn/policy")),
  });
  const [form, setForm] = useState({ interval: "", same: "", cooldown: "" });
  const [err, setErr] = useState("");
  const [ok, setOk] = useState(false);

  useEffect(() => {
    const p = q.data?.policy;
    if (p) {
      setForm({
        interval: String(p.interval_sec),
        same: String(p.same_rounds),
        cooldown: String(p.cooldown_sec),
      });
    }
  }, [q.data]);

  const save = useMutation({
    mutationFn: (p: Policy) => call(api.put("/advanced/cdn/policy", p)),
    onSuccess: () => {
      setOk(true);
      qc.invalidateQueries({ queryKey: ["cdn-policy"] });
    },
    onError: (e: any) => setErr(e?.response?.data?.error?.message ?? e.message),
  });

  const submit = () => {
    setErr("");
    setOk(false);
    const p: Policy = {
      interval_sec: Number(form.interval),
      same_rounds: Number(form.same),
      cooldown_sec: Number(form.cooldown),
    };
    const valid =
      Number.isInteger(p.interval_sec) &&
      p.interval_sec >= 30 &&
      p.interval_sec <= 86400 &&
      Number.isInteger(p.same_rounds) &&
      p.same_rounds >= 1 &&
      p.same_rounds <= 20 &&
      Number.isInteger(p.cooldown_sec) &&
      p.cooldown_sec >= 0 &&
      p.cooldown_sec <= 604800;
    if (!valid) {
      setErr(t("advanced.cdnPolicyInvalid"));
      return;
    }
    save.mutate(p);
  };

  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm({ ...form, [k]: e.target.value });

  return (
    <Card title={t("advanced.cardCdnPolicy")}>
      <div className="space-y-3">
        <div className="text-xs leading-relaxed text-white/50">
          {t("advanced.cdnPolicyIntro")}
        </div>
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label={t("advanced.cdnPolicyInterval")}>
            <Input inputMode="numeric" value={form.interval} onChange={set("interval")} />
          </Field>
          <Field label={t("advanced.cdnPolicySame")}>
            <Input inputMode="numeric" value={form.same} onChange={set("same")} />
          </Field>
          <Field
            label={t("advanced.cdnPolicyCooldown")}
            hint={t("advanced.cdnPolicyCooldownHint")}
          >
            <Input inputMode="numeric" value={form.cooldown} onChange={set("cooldown")} />
          </Field>
        </div>
        <ErrorText>{err}</ErrorText>
        <div className="flex items-center gap-3">
          <Button variant="primary" disabled={save.isPending} onClick={submit}>
            {t("advanced.cdnPolicySave")}
          </Button>
          {ok && (
            <span className="text-sm text-emerald-400">
              {t("advanced.cdnPolicySaved")}
            </span>
          )}
        </div>
      </div>
    </Card>
  );
}
