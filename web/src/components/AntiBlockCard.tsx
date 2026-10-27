// AntiBlockCard edits the node-wide anti-blocking posture (/api/v1/antiblock).
// Laid out for a first-time operator: what is in effect now (who serves
// HTTPS, the certificate, the subscription and panel addresses) on top, then
// the one-click setup, the low-profile switch, the reverse-proxy rule when
// the operator keeps their own web server, and everything else folded away
// under "more settings".

import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Card, ErrorText, Field, Input, Select, TextArea, Toggle } from "./ui";
import { api, call } from "../api/client";
import CopyButton from "./CopyButton";

type Owner = { port: number; process: string; tool: string };

export interface AntiBlockData {
  edge_domain: string;
  direct_domain: string;
  direct_domain_v6: string;
  sub_base_url: string;
  sub_mirrors: string[];
  stealth_mode: boolean;
  gateway_mode: "" | "builtin" | "external";
  origin_open: boolean;
  panel_gateway_only: boolean;
  gateway: {
    mode: "off" | "builtin" | "external" | "pending";
    need_cf_token?: boolean;
    port?: number;
    backend?: string;
    owner?: Owner;
    alt_port?: number;
    running: boolean;
    domain?: string;
    cert_expires?: number;
    cert_state?: "ok" | "issuing" | "no_dns" | "failed" | "need_token";
    cert_error?: string;
    next_retry?: number;
    issuing?: boolean;
    error?: string;
    cf_only: boolean;
    panel_url?: string;
  };
  proxy_configs?: { caddy?: string; nginx?: string };
  port443?: Owner;
  edge_port: number;
  edge_inbounds: { tag: string; path: string; port: number }[];
  mirror_pubkey: string;
  sub_origin: string;
  panel_url?: string;
  has_cf_token: boolean;
}

// useAntiBlock is shared with the inbound wizard, which needs to know whether
// an edge domain exists before it offers the "behind reverse proxy" option.
export function useAntiBlock() {
  return useQuery({
    queryKey: ["antiblock"],
    queryFn: () => call<AntiBlockData>(api.get("/antiblock")),
    staleTime: 30_000,
    // Poll while a certificate is being requested so the result shows up
    // without a manual refresh.
    refetchInterval: (q) => (q.state.data?.gateway.cert_state === "issuing" ? 5_000 : false),
  });
}

type Switches = {
  stealth_mode?: boolean;
  gateway_mode?: AntiBlockData["gateway_mode"];
  origin_open?: boolean;
  panel_gateway_only?: boolean;
  edge_port?: number;
};

export default function AntiBlockCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const ab = useAntiBlock();
  const [form, setForm] = useState({ direct_domain_v6: "", sub_base_url: "", mirrors: "" });
  const [msg, setMsg] = useState<{ kind: "ok" | "err"; text: string } | null>(null);

  useEffect(() => {
    if (!ab.data) return;
    setForm({
      direct_domain_v6: ab.data.direct_domain_v6,
      sub_base_url: ab.data.sub_base_url,
      mirrors: ab.data.sub_mirrors.join("\n"),
    });
  }, [ab.data]);

  // Every save sends the whole posture: the names from the server (they are
  // edited in the one-click setup), the advanced text fields from the form,
  // the switches from the server unless this save flips one.
  const save = useMutation({
    mutationFn: (sw: Switches) =>
      call<AntiBlockData>(
        api.put("/antiblock", {
          edge_domain: ab.data?.edge_domain ?? "",
          direct_domain: ab.data?.direct_domain ?? "",
          direct_domain_v6: form.direct_domain_v6,
          sub_base_url: form.sub_base_url,
          sub_mirrors: form.mirrors
            .split("\n")
            .map((s) => s.trim())
            .filter(Boolean),
          stealth_mode: sw.stealth_mode ?? ab.data?.stealth_mode ?? false,
          gateway_mode: sw.gateway_mode ?? ab.data?.gateway_mode ?? "",
          origin_open: sw.origin_open ?? ab.data?.origin_open ?? false,
          panel_gateway_only: sw.panel_gateway_only ?? ab.data?.panel_gateway_only ?? false,
          edge_port: sw.edge_port ?? ab.data?.edge_port ?? 0,
        }),
      ),
    onSuccess: (_d, sw) => {
      setMsg({
        kind: "ok",
        text:
          sw.stealth_mode !== undefined && sw.stealth_mode !== (ab.data?.stealth_mode ?? false)
            ? sw.stealth_mode
              ? t("antiblock.stealthOnSaved")
              : t("antiblock.stealthOffSaved")
            : t("antiblock.saved"),
      });
      qc.invalidateQueries({ queryKey: ["antiblock"] });
      qc.invalidateQueries({ queryKey: ["subscriptions"] });
    },
    onError: (e: Error) => setMsg({ kind: "err", text: e.message }),
  });

  if (!ab.data) return null;
  const data = ab.data;
  const stealth = data.stealth_mode;
  const edgeCount = data.edge_inbounds.length;
  const feedback = (
    <>
      {msg?.kind === "ok" && <span className="text-sm text-emerald-300">{msg.text}</span>}
      {msg?.kind === "err" && <ErrorText>{msg.text}</ErrorText>}
    </>
  );

  return (
    <div className="grid gap-6">
      <StatusCard data={data} busy={save.isPending} onChange={(sw) => save.mutate(sw)} />

      <QuickSetupCard data={data} />

      <Card title={t("antiblock.stealthSection")}>
        <div className="grid gap-3">
          <p className="text-xs text-white/50">{t("antiblock.stealthHint")}</p>
          <Toggle
            checked={stealth}
            onChange={(v) => save.mutate({ stealth_mode: v })}
            disabled={save.isPending || (!stealth && edgeCount === 0)}
            pendingLabel={save.isPending ? t("common.saving") : undefined}
            label={t("antiblock.stealthLabel")}
          />
          {!stealth && edgeCount === 0 && <p className="text-xs text-amber-300">{t("antiblock.stealthNeedsEdge")}</p>}
        </div>
      </Card>

      {data.proxy_configs && data.gateway.backend && data.gateway.mode === "external" && (
        <ProxyConfigCard data={data} />
      )}

      <details className="group rounded-xl border border-white/10 bg-white/[0.02]">
        <summary className="cursor-pointer select-none px-5 py-4 text-sm font-medium text-white/80">
          {t("antiblock.moreSection")}
        </summary>
        <div className="grid gap-5 px-5 pb-5">
          <Field label={t("antiblock.gatewayMode")} hint={t("antiblock.gatewayModeHint")}>
            <Select
              value={data.gateway_mode}
              disabled={save.isPending}
              onChange={(e) => save.mutate({ gateway_mode: e.target.value as AntiBlockData["gateway_mode"] })}
            >
              <option value="">{t("antiblock.gatewayModeAuto")}</option>
              <option value="builtin">{t("antiblock.gatewayModeBuiltin")}</option>
              <option value="external">{t("antiblock.gatewayModeExternal")}</option>
            </Select>
          </Field>

          {data.gateway.mode === "builtin" && (
            <>
              <div className="grid gap-1">
                <Toggle
                  checked={!data.origin_open}
                  onChange={(v) => save.mutate({ origin_open: !v })}
                  disabled={save.isPending}
                  label={t("antiblock.cfOnlyLabel")}
                />
                <p className="text-xs text-white/50">{t("antiblock.cfOnlyHint")}</p>
              </div>
              <div className="grid gap-1">
                <Toggle
                  checked={data.panel_gateway_only}
                  onChange={(v) => save.mutate({ panel_gateway_only: v })}
                  disabled={
                    save.isPending ||
                    (!data.panel_gateway_only && !(data.gateway.running && data.gateway.cert_state === "ok"))
                  }
                  label={t("antiblock.panelOnlyLabel")}
                />
                <p className="text-xs text-white/50">{t("antiblock.panelOnlyHint")}</p>
                {!data.panel_gateway_only && !(data.gateway.running && data.gateway.cert_state === "ok") && (
                  <p className="text-xs text-amber-300">{t("antiblock.panelOnlyNeedsGateway")}</p>
                )}
              </div>
            </>
          )}

          <Field label={t("antiblock.subBaseUrl")} hint={t("antiblock.subBaseUrlHint")}>
            <Input
              value={form.sub_base_url}
              onChange={(e) => setForm({ ...form, sub_base_url: e.target.value })}
              placeholder={t("antiblock.subBaseUrlPlaceholder")}
            />
          </Field>
          <Field label={t("antiblock.directDomainV6")} hint={t("antiblock.directDomainV6Hint")}>
            <Input
              value={form.direct_domain_v6}
              onChange={(e) => setForm({ ...form, direct_domain_v6: e.target.value })}
              placeholder="n1v6.example.com"
            />
          </Field>
          <Field label={t("antiblock.mirrors")} hint={t("antiblock.mirrorsHint")}>
            <TextArea
              rows={3}
              value={form.mirrors}
              onChange={(e) => setForm({ ...form, mirrors: e.target.value })}
              placeholder={"https://sub.example.net\nhttps://xxx.workers.dev"}
            />
          </Field>
          {data.mirror_pubkey && data.sub_mirrors.length > 0 && (
            <Field label={t("antiblock.pubkeySection")} hint={t("antiblock.pubkeyHint")}>
              <code className="text-xs font-mono break-all text-white/70">{data.mirror_pubkey}</code>
            </Field>
          )}
          <div className="flex items-center gap-3">
            <Button variant="primary" disabled={save.isPending} onClick={() => save.mutate({})}>
              {t("common.save")}
            </Button>
            {feedback}
          </div>
        </div>
      </details>
    </div>
  );
}

// Row is one "label: value" line of the status card.
function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1 sm:grid-cols-[8rem_1fr] sm:gap-3 py-2 border-b border-white/5 last:border-0">
      <div className="text-xs text-white/50 sm:pt-0.5">{label}</div>
      <div className="text-sm min-w-0">{children}</div>
    </div>
  );
}

const ok = "text-emerald-300";
const warn = "text-amber-300";

// StatusCard answers "what is in effect right now" in plain words, and puts
// the one action each problem needs right next to it.
function StatusCard({ data, busy, onChange }: { data: AntiBlockData; busy: boolean; onChange: (sw: Switches) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const g = data.gateway;
  const retry = useMutation({
    mutationFn: () => call(api.post("/antiblock/cert/retry", {})),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["antiblock"] }),
  });

  let proxy: React.ReactNode;
  if (g.mode === "builtin") {
    proxy = g.running ? (
      <span className={ok}>● {t("antiblock.proxyBuiltin", { port: g.port || 443 })}</span>
    ) : (
      <span className={warn}>● {t("antiblock.proxyBuiltinDown", { error: g.error ?? "" })}</span>
    );
  } else if (g.mode === "external") {
    proxy = (
      <span className={ok}>
        ● {g.owner?.process ? t("antiblock.proxyExternal", { process: g.owner.process }) : t("antiblock.proxyExternalUnknown")}
      </span>
    );
  } else if (g.mode === "pending" && g.owner) {
    proxy = (
      <div className="grid gap-2">
        <span className={warn}>● {t("antiblock.proxyConflict", { process: g.owner.process || "?" })}</span>
        <div className="flex flex-wrap gap-2">
          <Button variant="default" disabled={busy} onClick={() => onChange({ gateway_mode: "external" })}>
            {t("antiblock.quickExternal", { process: g.owner.process || "?" })}
          </Button>
          {g.alt_port ? (
            <Button variant="primary" disabled={busy} onClick={() => onChange({ gateway_mode: "builtin", edge_port: g.alt_port })}>
              {t("antiblock.useAltPort", { port: g.alt_port })}
            </Button>
          ) : null}
        </div>
      </div>
    );
  } else {
    proxy = <span className={warn}>● {t("antiblock.proxyNone")}</span>;
  }

  let certRow: React.ReactNode = null;
  if (g.mode === "builtin" && g.running) {
    const st = g.cert_state;
    certRow = (
      <Row label={t("antiblock.rowCert")}>
        {st === "ok" && g.cert_expires ? (
          <span className={ok}>● {t("antiblock.certOk", { date: new Date(g.cert_expires * 1000).toLocaleDateString() })}</span>
        ) : st === "no_dns" ? (
          <span className={warn}>● {t("antiblock.certNoDns", { domain: data.edge_domain })}</span>
        ) : st === "failed" ? (
          <div className="grid gap-1">
            <span className={warn}>
              ●{" "}
              {t("antiblock.certFailed", {
                time: g.next_retry ? new Date(g.next_retry * 1000).toLocaleTimeString() : "",
              })}
            </span>
            {g.cert_error && <code className="text-xs font-mono break-all text-white/60">{g.cert_error}</code>}
            <p className="text-xs text-white/50">{t("antiblock.certFailedHint")}</p>
          </div>
        ) : st === "need_token" ? (
          <div className="grid gap-2">
            <span className={warn}>● {t("antiblock.certNeedToken", { process: g.cert_error || "?" })}</span>
            <CloudflareTokenForm />
          </div>
        ) : (
          <span className="text-white/70">● {t("antiblock.certIssuing")}</span>
        )}
        {(st === "no_dns" || st === "failed") && (
          <div className="mt-2">
            <Button variant="default" disabled={retry.isPending} onClick={() => retry.mutate()}>
              {t("antiblock.certRetry")}
            </Button>
          </div>
        )}
      </Row>
    );
  } else if (g.mode === "external") {
    certRow = (
      <Row label={t("antiblock.rowCert")}>
        <span className="text-white/70">{t("antiblock.certExternal")}</span>
      </Row>
    );
  }

  return (
    <Card title={t("antiblock.statusSection")}>
      <div className="grid">
        <Row label={t("antiblock.rowProxy")}>{proxy}</Row>
        {data.edge_domain && (
          <Row label={t("antiblock.rowDomain")}>
            <span className="font-mono text-white/80">{data.edge_domain}</span>
            {data.direct_domain && (
              <span className="text-white/50">
                {"　"}
                {t("antiblock.directShort", { domain: data.direct_domain })}
              </span>
            )}
          </Row>
        )}
        {certRow}
        <Row label={t("antiblock.rowSub")}>
          {data.sub_origin ? (
            <span className="font-mono text-white/80 break-all">{data.sub_origin}/sub/…</span>
          ) : (
            <span className="text-white/60">{t("antiblock.subNone")}</span>
          )}
        </Row>
        {data.panel_url && (
          <Row label={t("antiblock.rowPanel")}>
            <div className="flex items-center gap-2">
              <code className="text-xs font-mono break-all text-white/80">{data.panel_url}</code>
              <CopyButton text={data.panel_url} />
            </div>
            <p className="text-xs text-white/50 mt-1">{t("antiblock.panelNote")}</p>
          </Row>
        )}
        <Row label={t("antiblock.rowStealth")}>
          <span className={data.stealth_mode ? warn : "text-white/70"}>
            {data.stealth_mode ? t("antiblock.stealthIsOn") : t("antiblock.stealthIsOff")}
          </span>
        </Row>
      </div>
    </Card>
  );
}

// QuickSetupCard is the one-click path: detect who owns port 443, take the
// two names, and either let EdgeNest serve the edge domain itself (on 443, or
// a free Cloudflare port when 443 is taken) or keep the server's own reverse
// proxy and show the rule to add to it.
function QuickSetupCard({ data }: { data: AntiBlockData }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [edge, setEdge] = useState(data.edge_domain);
  const [direct, setDirect] = useState(data.direct_domain);
  const [msg, setMsg] = useState<{ kind: "ok" | "err"; text: string } | null>(null);
  useEffect(() => {
    setEdge(data.edge_domain);
    setDirect(data.direct_domain);
  }, [data.edge_domain, data.direct_domain]);
  const setup = useMutation({
    mutationFn: (use: "builtin" | "external") =>
      call<AntiBlockData & { created_inbound: boolean; users_added: number }>(
        api.post("/antiblock/setup", { edge_domain: edge, direct_domain: direct, use }),
      ),
    onSuccess: (r, use) => {
      setMsg({
        kind: "ok",
        text:
          (r.created_inbound ? t("antiblock.quickDoneCreated", { users: r.users_added }) : t("antiblock.quickDone")) +
          (use === "external" ? " " + t("antiblock.quickDoneExternal") : ""),
      });
      qc.invalidateQueries({ queryKey: ["antiblock"] });
      qc.invalidateQueries({ queryKey: ["subscriptions"] });
      qc.invalidateQueries({ queryKey: ["inbounds"] });
    },
    onError: (e: Error) => setMsg({ kind: "err", text: e.message }),
  });
  const owner = data.port443;
  const disabled = setup.isPending || !edge.trim();
  const choice = (label: string, desc: string, onClick: () => void, primary: boolean) => (
    <div className="grid gap-1 rounded-lg border border-white/10 p-3">
      <div>
        <Button variant={primary ? "primary" : "default"} disabled={disabled} onClick={onClick}>
          {label}
        </Button>
      </div>
      <p className="text-xs text-white/50">{desc}</p>
    </div>
  );
  return (
    <Card title={t("antiblock.quickSection")}>
      <div className="grid gap-3">
        <p className="text-xs text-white/50">{t("antiblock.quickHint")}</p>
        <Field label={t("antiblock.edgeDomain")} hint={t("antiblock.quickEdgeHint")}>
          <Input value={edge} onChange={(e) => setEdge(e.target.value)} placeholder="cdn.example.com" />
        </Field>
        <Field label={t("antiblock.directDomain")} hint={t("antiblock.quickDirectHint")}>
          <Input value={direct} onChange={(e) => setDirect(e.target.value)} placeholder="n1.example.com" />
        </Field>
        <p className={`text-sm ${owner ? warn : ok}`}>
          ● {owner ? t("antiblock.port443Taken", { process: owner.process || "?" }) : t("antiblock.port443Free")}
        </p>
        <div className="grid gap-2 sm:grid-cols-2">
          {owner
            ? choice(
                t("antiblock.quickExternal", { process: owner.process || "?" }),
                t("antiblock.quickExternalDesc", { process: owner.process || "?" }),
                () => setup.mutate("external"),
                false,
              )
            : null}
          {choice(
            owner ? t("antiblock.quickBuiltinAlt") : t("antiblock.quickBuiltin"),
            owner ? t("antiblock.quickBuiltinAltDesc", { process: owner.process || "?" }) : t("antiblock.quickBuiltinDesc"),
            () => setup.mutate("builtin"),
            !owner,
          )}
        </div>
        {setup.isPending && <span className="text-sm text-white/60">{t("common.saving")}</span>}
        {msg?.kind === "ok" && <span className="text-sm text-emerald-300">{msg.text}</span>}
        {msg?.kind === "err" && <ErrorText>{msg.text}</ErrorText>}
      </div>
    </Card>
  );
}

// ProxyConfigCard shows what to add to the server's own reverse proxy: one
// rule sending the whole edge domain to EdgeNest's loopback entry, as a
// Caddyfile block, an Nginx server block, or steps for GUI tools like Lucky.
function ProxyConfigCard({ data }: { data: AntiBlockData }) {
  const { t } = useTranslation();
  const detected = data.gateway.owner?.tool ?? data.port443?.tool ?? "";
  const initial = detected === "nginx" ? "nginx" : detected === "caddy" ? "caddy" : detected ? "other" : "caddy";
  const [tab, setTab] = useState<"caddy" | "nginx" | "other">(initial);
  const cfgs = data.proxy_configs ?? {};
  const text = tab === "caddy" ? cfgs.caddy : tab === "nginx" ? cfgs.nginx : undefined;
  const tabs: { key: "caddy" | "nginx" | "other"; label: string }[] = [
    { key: "caddy", label: "Caddy" },
    { key: "nginx", label: "Nginx" },
    { key: "other", label: t("antiblock.proxyTabOther") },
  ];
  return (
    <Card title={t("antiblock.proxySection")}>
      <div className="grid gap-3">
        <p className="text-xs text-white/50">{t("antiblock.proxyHint", { backend: data.gateway.backend })}</p>
        <div className="flex flex-wrap gap-1">
          {tabs.map((x) => (
            <Button key={x.key} variant={tab === x.key ? "primary" : "default"} onClick={() => setTab(x.key)}>
              {x.label}
            </Button>
          ))}
        </div>
        {text ? (
          <>
            <pre className="rounded-lg bg-black/30 border border-white/10 p-3 text-xs font-mono overflow-x-auto whitespace-pre">
              {text}
            </pre>
            <div>
              <CopyButton text={text} />
            </div>
            <p className="text-xs text-white/50">
              {tab === "caddy" ? t("antiblock.proxyCaddyNote") : t("antiblock.proxyNginxNote")}
            </p>
          </>
        ) : (
          <p className="text-sm text-white/70 whitespace-pre-line">
            {t("antiblock.proxyOtherSteps", { domain: data.edge_domain, backend: data.gateway.backend })}
          </p>
        )}
      </div>
    </Card>
  );
}

// CloudflareTokenForm takes a Cloudflare API token when port 80 belongs to
// another program, so the edge domain's certificate is validated over DNS.
function CloudflareTokenForm() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [token, setToken] = useState("");
  const [err, setErr] = useState("");
  const save = useMutation({
    mutationFn: () => call(api.put("/antiblock/cf-token", { token })),
    onSuccess: () => {
      setToken("");
      setErr("");
      qc.invalidateQueries({ queryKey: ["antiblock"] });
    },
    onError: (e: Error) => setErr(e.message),
  });
  return (
    <div className="grid gap-2 rounded-lg border border-amber-400/30 bg-amber-400/5 p-3">
      <p className="text-xs text-white/70 whitespace-pre-line">{t("antiblock.cfTokenSteps")}</p>
      <div className="flex gap-2">
        <Input
          type="password"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder={t("antiblock.cfTokenPlaceholder")}
        />
        <Button variant="primary" disabled={save.isPending || !token.trim()} onClick={() => save.mutate()}>
          {t("common.save")}
        </Button>
      </div>
      {err && <ErrorText>{err}</ErrorText>}
    </div>
  );
}
