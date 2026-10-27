// AntiBlockCard edits the node-wide anti-blocking posture (/api/v1/antiblock):
// the Cloudflare-proxied edge domain in front of "behind reverse proxy"
// inbounds, DNS-only names that replace raw IPs in direct nodes, the public
// subscription origin + signed mirror list, and the low-profile switch that
// takes every directly-exposed inbound off the wire during sensitive periods.

import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Card, ErrorText, Field, Input, Select, TextArea, Toggle } from "./ui";
import { api, call } from "../api/client";
import CopyButton from "./CopyButton";

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
    mode: "off" | "builtin" | "external" | "conflict";
    port?: number;
    backend?: string;
    owner?: { port: number; process: string; tool: string };
    alt_port?: number;
    running: boolean;
    domain?: string;
    cert_expires?: number;
    issuing?: boolean;
    error?: string;
    cf_only: boolean;
    panel_url?: string;
  };
  proxy_configs?: { caddy?: string; nginx?: string };
  port443?: { port: number; process: string; tool: string };
  edge_port: number;
  edge_inbounds: { tag: string; path: string; port: number }[];
  mirror_pubkey: string;
}

// useAntiBlock is shared with the inbound wizard, which needs to know whether
// an edge domain exists before it offers the "behind reverse proxy" option.
export function useAntiBlock() {
  return useQuery({
    queryKey: ["antiblock"],
    queryFn: () => call<AntiBlockData>(api.get("/antiblock")),
    staleTime: 30_000,
  });
}

export default function AntiBlockCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const ab = useAntiBlock();
  const [form, setForm] = useState({
    edge_domain: "",
    direct_domain: "",
    direct_domain_v6: "",
    sub_base_url: "",
    mirrors: "",
  });
  const [msg, setMsg] = useState<{ kind: "ok" | "err"; text: string } | null>(null);

  useEffect(() => {
    if (!ab.data) return;
    setForm({
      edge_domain: ab.data.edge_domain,
      direct_domain: ab.data.direct_domain,
      direct_domain_v6: ab.data.direct_domain_v6,
      sub_base_url: ab.data.sub_base_url,
      mirrors: ab.data.sub_mirrors.join("\n"),
    });
  }, [ab.data]);

  // Every save sends the whole posture: the text fields from the form, the
  // switches from the server's current values unless this save flips one.
  type Switches = {
    stealth_mode?: boolean;
    gateway_mode?: AntiBlockData["gateway_mode"];
    origin_open?: boolean;
    panel_gateway_only?: boolean;
    edge_port?: number;
  };
  const save = useMutation({
    mutationFn: (sw: Switches) =>
      call<AntiBlockData>(
        api.put("/antiblock", {
          edge_domain: form.edge_domain,
          direct_domain: form.direct_domain,
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

  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
    setForm({ ...form, [k]: e.target.value });

  const stealth = ab.data?.stealth_mode ?? false;
  const edgeCount = ab.data?.edge_inbounds.length ?? 0;

  return (
    <div className="grid gap-6">
      {ab.data && <QuickSetupCard data={ab.data} />}

      <Card title={t("antiblock.stealthSection")}>
        <div className="grid gap-3">
          <p className="text-xs text-white/50">{t("antiblock.stealthHint")}</p>
          <Toggle
            checked={stealth}
            onChange={(v) => save.mutate({ stealth_mode: v })}
            disabled={ab.isLoading || save.isPending || (!stealth && edgeCount === 0)}
            pendingLabel={t("common.saving")}
            label={t("antiblock.stealthLabel")}
          />
          {!stealth && edgeCount === 0 && (
            <p className="text-xs text-amber-300">{t("antiblock.stealthNeedsEdge")}</p>
          )}
        </div>
      </Card>

      <Card title={t("antiblock.namesSection")}>
        <div className="grid gap-4">
          <p className="text-xs text-white/50">{t("antiblock.namesIntro")}</p>
          <Field label={t("antiblock.edgeDomain")} hint={t("antiblock.edgeDomainHint")}>
            <Input value={form.edge_domain} onChange={set("edge_domain")} placeholder="cdn.example.com" />
          </Field>
          <Field label={t("antiblock.directDomain")} hint={t("antiblock.directDomainHint")}>
            <Input value={form.direct_domain} onChange={set("direct_domain")} placeholder="n1.example.com" />
          </Field>
          <Field label={t("antiblock.directDomainV6")} hint={t("antiblock.directDomainV6Hint")}>
            <Input value={form.direct_domain_v6} onChange={set("direct_domain_v6")} placeholder="n1v6.example.com" />
          </Field>
          <Field label={t("antiblock.subBaseUrl")} hint={t("antiblock.subBaseUrlHint")}>
            <Input value={form.sub_base_url} onChange={set("sub_base_url")} placeholder="https://cdn.example.com" />
          </Field>
          <Field label={t("antiblock.mirrors")} hint={t("antiblock.mirrorsHint")}>
            <TextArea
              rows={3}
              value={form.mirrors}
              onChange={set("mirrors")}
              placeholder={"https://sub.example.net\nhttps://xxx.workers.dev"}
            />
          </Field>
          <div className="flex items-center gap-3">
            <Button variant="primary" disabled={save.isPending || ab.isLoading} onClick={() => save.mutate({})}>
              {t("common.save")}
            </Button>
            {msg?.kind === "ok" && <span className="text-sm text-emerald-300">{msg.text}</span>}
          </div>
          {msg?.kind === "err" && <ErrorText>{msg.text}</ErrorText>}
        </div>
      </Card>

      {ab.data?.edge_domain && <GatewayCard data={ab.data} busy={save.isPending} onChange={(sw) => save.mutate(sw)} />}

      {ab.data?.proxy_configs && ab.data.gateway.backend && (
        <ProxyConfigCard data={ab.data} />
      )}

      {ab.data?.mirror_pubkey && (ab.data.sub_mirrors.length ?? 0) > 0 && (
        <Card title={t("antiblock.pubkeySection")}>
          <p className="text-xs text-white/50 mb-2">{t("antiblock.pubkeyHint")}</p>
          <code className="text-xs font-mono break-all text-white/70">{ab.data.mirror_pubkey}</code>
        </Card>
      )}
    </div>
  );
}

// GatewayCard shows who terminates TLS for the edge domain — the built-in
// gateway (no web server needed) or the operator's own reverse proxy — and
// the gateway's certificate and origin-protection state.
function GatewayCard({
  data,
  busy,
  onChange,
}: {
  data: AntiBlockData;
  busy: boolean;
  onChange: (sw: {
    gateway_mode?: AntiBlockData["gateway_mode"];
    origin_open?: boolean;
    panel_gateway_only?: boolean;
    edge_port?: number;
  }) => void;
}) {
  const { t } = useTranslation();
  const g = data.gateway;
  const status =
    g.mode === "builtin" && g.running
      ? g.port && g.port !== 443
        ? t("antiblock.gatewayRunningPort", { port: g.port })
        : t("antiblock.gatewayRunning")
      : g.mode === "external"
        ? t("antiblock.gatewayExternal")
        : g.mode === "conflict"
          ? t("antiblock.gatewayConflict", { process: g.owner?.process || "?" })
          : t("antiblock.gatewayStopped");
  return (
    <Card title={t("antiblock.gatewaySection")}>
      <div className="grid gap-3">
        <p className="text-xs text-white/50">{t("antiblock.gatewayHint")}</p>
        <div className="text-sm">
          <span className={g.mode === "builtin" && g.running ? "text-emerald-300" : "text-amber-300"}>● {status}</span>
          {g.cert_expires ? (
            <span className="text-white/50">
              {"　"}
              {t("antiblock.gatewayCertUntil", { date: new Date(g.cert_expires * 1000).toLocaleDateString() })}
            </span>
          ) : g.issuing ? (
            <span className="text-white/50">{"　"}{t("antiblock.gatewayIssuing")}</span>
          ) : null}
        </div>
        {g.error && <p className="text-xs text-amber-300">{g.error}</p>}
        {g.mode === "conflict" && (
          <div className="flex flex-wrap gap-2">
            <Button variant="default" disabled={busy} onClick={() => onChange({ gateway_mode: "external" })}>
              {t("antiblock.quickExternal", { process: g.owner?.process || "?" })}
            </Button>
            {g.alt_port ? (
              <Button
                variant="primary"
                disabled={busy}
                onClick={() => onChange({ gateway_mode: "builtin", edge_port: g.alt_port })}
              >
                {t("antiblock.useAltPort", { port: g.alt_port })}
              </Button>
            ) : null}
          </div>
        )}
        <Field label={t("antiblock.gatewayMode")} hint={t("antiblock.gatewayModeHint")}>
          <Select
            value={data.gateway_mode}
            disabled={busy}
            onChange={(e) => onChange({ gateway_mode: e.target.value as AntiBlockData["gateway_mode"] })}
          >
            <option value="">{t("antiblock.gatewayModeAuto")}</option>
            <option value="builtin">{t("antiblock.gatewayModeBuiltin")}</option>
            <option value="external">{t("antiblock.gatewayModeExternal")}</option>
          </Select>
        </Field>
        {g.panel_url && (
          <Field label={t("antiblock.panelUrl")} hint={t("antiblock.panelUrlHint")}>
            <div className="flex items-center gap-2">
              <code className="text-xs font-mono break-all text-white/80">{g.panel_url}</code>
              <CopyButton text={g.panel_url} />
            </div>
          </Field>
        )}
        {g.mode === "builtin" && (
          <>
            <p className="text-xs text-white/50">{t("antiblock.panelOnlyHint")}</p>
            <Toggle
              checked={data.panel_gateway_only}
              onChange={(v) => onChange({ panel_gateway_only: v })}
              disabled={busy || (!data.panel_gateway_only && !(g.running && g.cert_expires))}
              label={t("antiblock.panelOnlyLabel")}
            />
            {!data.panel_gateway_only && !(g.running && g.cert_expires) && (
              <p className="text-xs text-amber-300">{t("antiblock.panelOnlyNeedsGateway")}</p>
            )}
            <p className="text-xs text-white/50">{t("antiblock.cfOnlyHint")}</p>
            <Toggle
              checked={!data.origin_open}
              onChange={(v) => onChange({ origin_open: !v })}
              disabled={busy}
              label={t("antiblock.cfOnlyLabel")}
            />
          </>
        )}
      </div>
    </Card>
  );
}

// QuickSetupCard is the one-click path: detect who owns port 443, take the
// two names, and either let EdgeNest serve the edge domain itself (on 443, or
// a free Cloudflare port when 443 is taken) or keep the server's own reverse
// proxy and show the config to add to it.
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
  return (
    <Card title={t("antiblock.quickSection")}>
      <div className="grid gap-3">
        <p className="text-xs text-white/50">{t("antiblock.quickHint")}</p>
        <p className={`text-sm ${owner ? "text-amber-300" : "text-emerald-300"}`}>
          ●{" "}
          {owner
            ? t("antiblock.port443Taken", { process: owner.process || "?" })
            : t("antiblock.port443Free")}
        </p>
        <Field label={t("antiblock.edgeDomain")} hint={t("antiblock.quickEdgeHint")}>
          <Input value={edge} onChange={(e) => setEdge(e.target.value)} placeholder="cdn.example.com" />
        </Field>
        <Field label={t("antiblock.directDomain")} hint={t("antiblock.quickDirectHint")}>
          <Input value={direct} onChange={(e) => setDirect(e.target.value)} placeholder="n1.example.com" />
        </Field>
        <div className="flex flex-wrap gap-2">
          <Button variant="primary" disabled={setup.isPending || !edge.trim()} onClick={() => setup.mutate("builtin")}>
            {owner ? t("antiblock.quickBuiltinAlt") : t("antiblock.quickBuiltin")}
          </Button>
          {owner && (
            <Button variant="default" disabled={setup.isPending || !edge.trim()} onClick={() => setup.mutate("external")}>
              {t("antiblock.quickExternal", { process: owner.process || "?" })}
            </Button>
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
