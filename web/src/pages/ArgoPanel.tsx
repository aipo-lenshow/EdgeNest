import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import {
  Badge,
  Button,
  Card,
  ErrorText,
  Field,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  TextArea,
  Toggle,
} from "../components/ui";

interface AdvancedArgo {
  argo_enabled: boolean;
  argo_mode: string;
  argo_domain: string;
  argo_has_token: boolean;
}

interface ArgoStatus {
  state: "idle" | "starting" | "running" | "failed";
  mode?: "temp" | "named";
  hostname?: string;
  error?: string;
  since?: string;
}

// useArgoErrMsg maps EVERY error code the Argo tab's endpoints (save / start /
// Cloudflare probe+provision) can return to localised, jargon-free text. Without
// it the operator sees raw Go/Cloudflare strings ("argo_token is required for
// fixed Argo tunnels", "cloudflare: 10000 Authentication error", …) that leak
// internals and ignore the UI language. PURE codes have a fixed self-evident
// meaning; DETAIL codes wrap an upstream (cloudflared / Cloudflare API) message
// worth surfacing, so they get a localised prefix + the raw detail.
const ARGO_ERR_PURE: Record<string, string> = {
  MISSING_ARGO_TOKEN: "advanced.argoErr.missingToken",
  ARGO_MISSING_TOKEN: "advanced.argoErr.missingToken",
  ARGO_MISSING_DOMAIN: "advanced.argoErr.missingDomain",
  BAD_ARGO_MODE: "advanced.argoErr.badMode",
  ARGO_NO_INBOUND: "advanced.argoErr.noInbound",
  ARGO_NOT_CONFIGURED: "advanced.argoErr.notConfigured",
  ARGO_INVALID_TOKEN: "advanced.argoErr.invalidToken",
  ARGO_BINARY_FAILED: "advanced.argoErr.binaryFailed",
  BAD_LOCAL_PORT: "advanced.argoErr.badPort",
  NO_CF_TOKEN: "advanced.argoErr.noCfToken",
  NO_DOMAIN: "advanced.argoErr.noDomain",
  CF_TOKEN_SCOPE: "advanced.argoErr.cfTokenScope",
  DB_ERROR: "advanced.argoErr.dbError",
  APPLY_FAILED: "advanced.argoErr.applyFailed",
  BAD_BODY: "advanced.argoErr.badBody",
};
const ARGO_ERR_DETAIL: Record<string, string> = {
  ARGO_START_FAILED: "advanced.argoErr.startFailed",
  BAD_CF_TOKEN: "advanced.argoErr.badCfToken",
  NO_ZONE: "advanced.argoErr.noZone",
  CF_ERROR: "advanced.argoErr.cfError",
  CF_TOKEN_FAILED: "advanced.argoErr.cfError",
  CF_CREATE_FAILED: "advanced.argoErr.cfError",
  CF_INGRESS_FAILED: "advanced.argoErr.cfError",
  CF_DNS_FAILED: "advanced.argoErr.cfError",
};
function useArgoErrMsg() {
  const { t } = useTranslation();
  return (e: any): string => {
    const code: string | undefined = e?.response?.data?.error?.code;
    const raw: string = e?.response?.data?.error?.message ?? e?.message ?? "";
    if (code && ARGO_ERR_PURE[code]) return t(ARGO_ERR_PURE[code]);
    if (code && ARGO_ERR_DETAIL[code]) return t(ARGO_ERR_DETAIL[code], { detail: raw });
    return raw;
  };
}

type FixedMethod = "tunnel_token" | "cf_api";
type TutorialKind = "tunnel" | "api" | null;

// copyText copies to the clipboard, falling back to execCommand when the panel
// is served over plain HTTP (navigator.clipboard needs a secure context, which
// a self-hosted node on http://ip:port is not).
async function copyText(s: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(s);
      return true;
    }
  } catch {
    /* fall through to legacy path */
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = s;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}

// CopyField shows a value with a one-click copy button — used in the tutorial so
// the operator pastes the exact Service URL into Cloudflare instead of having to
// substitute a port into a placeholder.
function CopyField({ value }: { value: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex items-center gap-2">
      <code className="flex-1 break-all rounded bg-black/40 px-2 py-1.5 text-sm text-emerald-300">
        {value}
      </code>
      <Button
        variant="default"
        onClick={async () => {
          if (await copyText(value)) {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }
        }}
      >
        {copied ? t("advanced.argoTT.copied") : t("advanced.argoTT.copy")}
      </Button>
    </div>
  );
}

// ArgoPanel is the Argo-tunnel tab of the client-inbound page. It owns the Argo
// slice of AdvancedConfig and saves through PUT /advanced/argo (write-only
// token: empty means "keep the stored one"). For a fixed tunnel the operator
// first chooses HOW they supply credentials — paste a tunnel token themselves,
// or let us drive the Cloudflare API — because the two paths need entirely
// different inputs and tutorials.
export default function ArgoPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMsg = useArgoErrMsg();
  const { data } = useQuery({
    queryKey: ["advanced"],
    queryFn: () => call<AdvancedArgo>(api.get("/advanced")),
  });

  const [argoEnabled, setArgoEnabled] = useState(false);
  const [argoMode, setArgoMode] = useState("temp");
  const [argoDomain, setArgoDomain] = useState("");
  const [argoToken, setArgoToken] = useState("");
  const [fixedMethod, setFixedMethod] = useState<FixedMethod>("tunnel_token");
  // CF-API path credentials live here (not inside ArgoCFApi) so the API-token
  // tutorial can paste-and-fill them back into the panel.
  const [cfToken, setCfToken] = useState("");
  const [cfReuseCert, setCfReuseCert] = useState(true);
  const [err, setErr] = useState("");
  const [okMsg, setOkMsg] = useState("");
  const [tutorial, setTutorial] = useState<TutorialKind>(null);

  useEffect(() => {
    if (data) {
      setArgoEnabled(!!data.argo_enabled);
      setArgoMode(data.argo_mode || "temp");
      setArgoDomain(data.argo_domain ?? "");
    }
  }, [data]);

  const save = useMutation({
    mutationFn: () => {
      const body: any = {
        argo_enabled: argoEnabled,
        argo_mode: argoMode,
        argo_domain: argoDomain,
      };
      if (argoToken.trim() !== "") body.argo_token = argoToken.trim();
      return call(api.put("/advanced/argo", body));
    },
    onSuccess: () => {
      setOkMsg(t("advanced.savedMsg"));
      setArgoToken("");
      qc.invalidateQueries({ queryKey: ["advanced"] });
    },
    onError: (e: any) => setErr(errMsg(e)),
  });

  // Argo tunnel status polled every 3s while the operator is on this tab.
  const tunnel = useQuery({
    queryKey: ["argo-status"],
    queryFn: () => call<ArgoStatus>(api.get("/argo/status")),
    refetchInterval: 3000,
  });

  const [argoLocalPort, setArgoLocalPort] = useState(8443);

  // The single Argo inbound (at most one per node). Its loopback port is what
  // the tunnel must point at, so we bind the local port to it rather than let
  // the operator type a number. No argo inbound → the start control is replaced
  // by a "create one in the wizard" notice.
  const { data: inbounds } = useQuery({
    queryKey: ["inbounds"],
    queryFn: () =>
      call<{ id: number; tag: string; port: number; settings: string }[]>(
        api.get("/inbounds"),
      ),
  });
  const argoInbound = (inbounds ?? []).find((ib) => {
    try {
      const s = JSON.parse(ib.settings || "{}");
      return s.argo_bound === true || s.argo_bound === "true";
    } catch {
      return false;
    }
  });
  useEffect(() => {
    if (argoInbound) setArgoLocalPort(argoInbound.port);
  }, [argoInbound?.port]);

  const startTunnel = useMutation({
    mutationFn: () =>
      call<ArgoStatus>(api.post("/argo/start", { local_port: argoLocalPort })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["argo-status"] }),
    onError: (e: any) => setErr(errMsg(e)),
  });

  // Start = persist the on-screen mode/domain/token FIRST, then launch — the
  // server starts from the SAVED config, so without this a mode the operator
  // just picked (e.g. switched fixed → temp) but didn't save is ignored and the
  // tunnel starts in the stale mode. Saving first makes start what-you-see.
  const saveThenStart = async () => {
    setErr("");
    setOkMsg("");
    try {
      await save.mutateAsync();
    } catch {
      return; // save.onError already surfaced the reason
    }
    startTunnel.mutate();
  };
  const stopTunnel = useMutation({
    mutationFn: () => call<ArgoStatus>(api.post("/argo/stop")),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["argo-status"] }),
  });

  const running =
    tunnel.data?.state === "running" || tunnel.data?.state === "starting";

  return (
    <>
      <PageHeader
        title={t("inbound.argoTitle")}
        subtitle={t("inbound.argoSubtitle")}
        action={
          <Badge
            tone={tunnel.data?.state === "running" ? "success" : "neutral"}
            dot
            solid={tunnel.data?.state === "running"}
            size="lg"
          >
            {t("advanced.badgeArgo")}
          </Badge>
        }
      />

      <Card title={t("advanced.cardArgo")}>
        <div className="space-y-4">
          <Field label={t("advanced.fieldMode")}>
            <Select value={argoMode} onChange={(e) => setArgoMode(e.target.value)}>
              <option value="temp">{t("advanced.modeTemp")}</option>
              <option value="fixed">{t("advanced.modeFixed")}</option>
            </Select>
          </Field>

          {argoMode === "temp" && (
            <div className="rounded-md border border-amber-400/30 bg-amber-400/10 p-3 text-xs text-amber-200/90">
              {t("advanced.argoTempHint")}
            </div>
          )}

          {argoMode === "fixed" && (
            <>
              {/* Step 1: how does the operator supply credentials? */}
              <Field label={t("advanced.fixedMethodLabel")}>
                <div className="grid grid-cols-2 gap-2">
                  <MethodButton
                    active={fixedMethod === "tunnel_token"}
                    title={t("advanced.fixedMethodTunnel")}
                    hint={t("advanced.fixedMethodTunnelHint")}
                    onClick={() => setFixedMethod("tunnel_token")}
                  />
                  <MethodButton
                    active={fixedMethod === "cf_api"}
                    title={t("advanced.fixedMethodApi")}
                    hint={t("advanced.fixedMethodApiHint")}
                    onClick={() => setFixedMethod("cf_api")}
                  />
                </div>
              </Field>

              {fixedMethod === "tunnel_token" ? (
                <div className="space-y-3">
                  <button
                    type="button"
                    className="text-sm text-emerald-300 hover:text-emerald-200 underline-offset-2 hover:underline"
                    onClick={() => setTutorial("tunnel")}
                  >
                    {t("advanced.argoTunnelTokenTutorialLink")}
                  </button>
                  {/* Order matches Cloudflare's flow: the token is shown first
                      (in the install command), the public hostname configured
                      after — so token on top, domain below. */}
                  <Field
                    label={t("advanced.fieldArgoToken")}
                    hint={t("advanced.fieldArgoTokenHint")}
                  >
                    <Input
                      type="password"
                      value={argoToken}
                      onChange={(e) => setArgoToken(e.target.value)}
                      placeholder={
                        data?.argo_has_token
                          ? t("advanced.argoTokenSaved")
                          : t("advanced.argoTokenEmpty")
                      }
                    />
                  </Field>
                  <Field
                    label={t("advanced.fieldArgoDomain")}
                    hint={t("advanced.argoTunnelDomainHint")}
                  >
                    <Input
                      value={argoDomain}
                      onChange={(e) => setArgoDomain(e.target.value)}
                      placeholder="tunnel.example.com"
                    />
                  </Field>
                </div>
              ) : (
                <ArgoCFApi
                  token={cfToken}
                  reuseCert={cfReuseCert}
                  onToken={setCfToken}
                  onReuseCert={setCfReuseCert}
                  onTutorial={() => setTutorial("api")}
                  onProvisioned={(d) => {
                    setArgoDomain(d);
                    setArgoToken("");
                    setOkMsg(t("advanced.cfProvisioned", { domain: d }));
                    qc.invalidateQueries({ queryKey: ["advanced"] });
                    qc.invalidateQueries({ queryKey: ["argo-status"] });
                  }}
                />
              )}
            </>
          )}

          <div className="rounded-md border border-white/10 bg-white/5 p-3">
            <div className="mb-2 text-xs uppercase tracking-wide text-white/50">
              {t("advanced.tunnelStateTitle")}
            </div>

            {!argoInbound ? (
              <div className="rounded-md border border-amber-400/30 bg-amber-400/10 p-3 text-xs text-amber-200/90">
                {t("advanced.argoNoInbound")}
              </div>
            ) : (
              <>
                <div className="flex flex-wrap items-center gap-3 text-sm">
                  <Badge
                    tone={
                      tunnel.data?.state === "running"
                        ? "success"
                        : tunnel.data?.state === "starting"
                          ? "warn"
                          : tunnel.data?.state === "failed"
                            ? "danger"
                            : "neutral"
                    }
                  >
                    {tunnel.data?.state === "starting" && <Spinner className="mr-1.5" />}
                    {t(
                      `accel.tunnelState.${
                        ["idle", "starting", "running", "failed"].includes(
                          tunnel.data?.state ?? "idle",
                        )
                          ? (tunnel.data?.state ?? "idle")
                          : "unknown"
                      }`,
                    )}
                  </Badge>
                  {tunnel.data?.hostname ? (
                    <code className="rounded bg-black/40 px-2 py-1 text-emerald-300">
                      https://{tunnel.data.hostname}
                    </code>
                  ) : (
                    <span className="text-white/40">{t("advanced.tunnelNoHostname")}</span>
                  )}
                  {tunnel.data?.error && (
                    <span className="text-red-400">{tunnel.data.error}</span>
                  )}
                </div>
                <div className="mt-3 flex flex-wrap items-end gap-3">
                  <Field
                    label={t("advanced.tunnelLocalPort")}
                    hint={t("advanced.tunnelLocalPortBound", { tag: argoInbound.tag })}
                  >
                    <Input type="number" value={argoLocalPort} readOnly disabled />
                  </Field>
                </div>
                <div className="mt-2 text-xs text-white/40">
                  {t("advanced.tunnelHint")}
                </div>
              </>
            )}
          </div>
        </div>
      </Card>

      <ErrorText>{err}</ErrorText>
      {okMsg && <div className="mt-3 text-sm text-emerald-400">{okMsg}</div>}

      {/* One adaptive action button (save + start are the same intent — starting
          the tunnel persists the on-screen config first). Stops when running;
          with no Argo inbound yet it just saves the config for later. */}
      <div className="mt-5">
        {running ? (
          <Button
            variant="danger"
            disabled={stopTunnel.isPending}
            onClick={() => stopTunnel.mutate()}
          >
            {t("advanced.tunnelStop")}
          </Button>
        ) : argoInbound ? (
          <Button
            variant="primary"
            disabled={save.isPending || startTunnel.isPending}
            onClick={saveThenStart}
          >
            {save.isPending || startTunnel.isPending ? (
              <span className="inline-flex items-center gap-2">
                <Spinner />{" "}
                {save.isPending ? t("advanced.btnSaving") : t("advanced.tunnelStarting")}
              </span>
            ) : (
              t("advanced.saveAndStart")
            )}
          </Button>
        ) : (
          <Button
            variant="primary"
            disabled={save.isPending}
            onClick={() => {
              setErr("");
              setOkMsg("");
              save.mutate();
            }}
          >
            {save.isPending ? (
              <span className="inline-flex items-center gap-2">
                <Spinner /> {t("advanced.btnSaving")}
              </span>
            ) : (
              t("advanced.btnSave")
            )}
          </Button>
        )}
      </div>

      <Modal
        open={tutorial !== null}
        onClose={() => setTutorial(null)}
        title={
          tutorial === "api"
            ? t("advanced.argoApiTokenTutorialTitle")
            : t("advanced.argoTunnelTokenTutorialTitle")
        }
        size="lg"
      >
        {tutorial === "api" ? (
          <ApiTokenTutorial
            token={cfToken}
            reuseCert={cfReuseCert}
            onToken={(tk) => {
              setCfToken(tk);
              if (tk.trim()) setCfReuseCert(false);
            }}
            onSwitchMethod={() => setFixedMethod("cf_api")}
          />
        ) : (
          <TunnelTokenTutorial
            localPort={argoLocalPort}
            domain={argoDomain}
            onToken={(tk) => {
              setArgoToken(tk);
              setFixedMethod("tunnel_token");
            }}
            onDomain={setArgoDomain}
          />
        )}
      </Modal>
    </>
  );
}

// MethodButton is a segmented-control tile for the fixed-tunnel credential
// choice (paste a tunnel token vs. drive the Cloudflare API).
function MethodButton({
  active,
  title,
  hint,
  onClick,
}: {
  active: boolean;
  title: string;
  hint: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "rounded-md border p-3 text-left transition " +
        (active
          ? "border-emerald-400/60 bg-emerald-400/10"
          : "border-white/10 bg-white/5 hover:border-white/20")
      }
    >
      <div className={"text-sm font-medium " + (active ? "text-emerald-200" : "text-white/80")}>
        {title}
      </div>
      <div className="mt-0.5 text-xs text-white/45">{hint}</div>
    </button>
  );
}

// TunnelTokenTutorial is the interactive "get a tunnel token" guide. Rather than
// only telling the operator what to copy, it lets them paste Cloudflare's whole
// install command (we extract the token), copies out the exact Service URL with
// the real local port filled in, and takes the public hostname inline — all of
// which flow straight back into the panel's config fields.
function TunnelTokenTutorial({
  localPort,
  domain,
  onToken,
  onDomain,
}: {
  localPort: number;
  domain: string;
  onToken: (v: string) => void;
  onDomain: (v: string) => void;
}) {
  const { t } = useTranslation();
  const [pasted, setPasted] = useState("");
  const [tokenOk, setTokenOk] = useState(false);

  const handlePaste = (v: string) => {
    setPasted(v);
    // The tunnel run token is a long base64 string starting with eyJ. Pull it
    // out of the whole install command so the operator doesn't have to hunt.
    const m = v.match(/eyJ[A-Za-z0-9+/=_-]{20,}/);
    const tk = m ? m[0] : v.trim();
    onToken(tk);
    setTokenOk(tk.length > 0);
  };

  const P = ({ k }: { k: string }) => (
    <p className="whitespace-pre-line">{t(k)}</p>
  );

  return (
    <div className="space-y-4 text-sm leading-relaxed text-white/80">
      <P k="advanced.argoTT.lead" />
      <P k="advanced.argoTT.s1" />
      <P k="advanced.argoTT.s2" />
      <P k="advanced.argoTT.s3" />
      <P k="advanced.argoTT.s4" />
      <P k="advanced.argoTT.s5" />

      <div className="space-y-2">
        <P k="advanced.argoTT.s6" />
        <Field label={t("advanced.argoTT.pasteLabel")} hint={t("advanced.argoTT.pasteHint")}>
          <TextArea
            rows={3}
            value={pasted}
            onChange={(e) => handlePaste(e.target.value)}
            placeholder="cloudflared service install eyJhIjoi…"
          />
        </Field>
        {tokenOk && (
          <div className="text-xs text-emerald-400">{t("advanced.argoTT.tokenOk")}</div>
        )}
      </div>

      <div className="space-y-2">
        <P k="advanced.argoTT.s7" />
        <Field label={t("advanced.argoTT.copyUrlLabel")}>
          <CopyField value={`localhost:${localPort}`} />
        </Field>
        <P k="advanced.argoTT.s7b" />
      </div>

      <div className="space-y-2">
        <P k="advanced.argoTT.s8" />
        <Field label={t("advanced.argoTT.domainLabel")}>
          <Input
            value={domain}
            onChange={(e) => onDomain(e.target.value)}
            placeholder="tunnel.example.com"
          />
        </Field>
      </div>

      <P k="advanced.argoTT.s9" />
      <P k="advanced.argoTT.s10" />
    </div>
  );
}

// ApiTokenTutorial walks through creating a DNS+Tunnel API token, then lets the
// operator paste it inline so it lands in the panel's CF-API token field.
function ApiTokenTutorial({
  token,
  reuseCert,
  onToken,
  onSwitchMethod,
}: {
  token: string;
  reuseCert: boolean;
  onToken: (v: string) => void;
  onSwitchMethod: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="space-y-4 text-sm leading-relaxed text-white/80">
      <p className="whitespace-pre-line">{t("advanced.argoApiTokenTutorialBody")}</p>
      <Field
        label={t("advanced.argoTT.apiPasteLabel")}
        hint={reuseCert ? t("advanced.argoTT.apiPasteReuseHint") : undefined}
      >
        <Input
          type="password"
          value={token}
          onChange={(e) => {
            onToken(e.target.value);
            onSwitchMethod();
          }}
          placeholder="cloudflare API token"
        />
      </Field>
      <p className="text-xs text-white/50">{t("advanced.argoTT.apiPasteAfter")}</p>
    </div>
  );
}

interface CFTunnel {
  id: string;
  name: string;
  status: string;
}

interface CFProbe {
  verified: boolean;
  has_tunnel_perm: boolean;
  account_name: string;
  zones: string[];
  tunnels: CFTunnel[];
}

// ArgoCFApi is the Cloudflare-API fixed-tunnel path. It first PROBES the token
// (verify + list readable zones + check tunnel permission). Without tunnel
// permission it shows actionable guidance instead of failing later at create
// time. With permission it lets the operator pick a zone + subdomain prefix
// (we build the hostname) and reuse or create a tunnel, then provisions
// everything (tunnel + ingress + DNS CNAME) and saves the fixed-mode config —
// after which the bottom "save & start" launches it.
function ArgoCFApi({
  token,
  reuseCert,
  onToken,
  onReuseCert,
  onProvisioned,
  onTutorial,
}: {
  token: string;
  reuseCert: boolean;
  onToken: (v: string) => void;
  onReuseCert: (v: boolean) => void;
  onProvisioned: (domain: string) => void;
  onTutorial: () => void;
}) {
  const { t } = useTranslation();
  const errMsg = useArgoErrMsg();

  const [probe, setProbe] = useState<CFProbe | null>(null);
  const [zone, setZone] = useState("");
  const [prefix, setPrefix] = useState("tunnel");
  const [tunnelId, setTunnelId] = useState(""); // "" = create new
  const [tunnelName, setTunnelName] = useState("");
  const [err, setErr] = useState("");
  const [okMsg, setOkMsg] = useState("");

  const hostname = useMemo(() => {
    const z = zone.trim().toLowerCase();
    const p = prefix.trim().toLowerCase().replace(/\.+$/, "");
    if (!z) return "";
    return p ? `${p}.${z}` : z;
  }, [zone, prefix]);

  const tokenBody = () => ({
    token: token.trim() || undefined,
    reuse_cert_token: reuseCert,
  });

  const probeMut = useMutation({
    mutationFn: () => call<CFProbe>(api.post("/argo/cf/probe", tokenBody())),
    onSuccess: (d) => {
      setErr("");
      setProbe(d);
      // Default the zone selection to the first readable zone.
      if (d.zones.length > 0 && !zone) setZone(d.zones[0]);
    },
    onError: (e: any) => {
      setProbe(null);
      setErr(errMsg(e));
    },
  });

  const provision = useMutation({
    mutationFn: () =>
      call<{ domain: string; tunnel_name?: string }>(
        api.post("/argo/cf/provision", {
          ...tokenBody(),
          domain: hostname,
          tunnel_id: tunnelId || undefined,
          tunnel_name: tunnelName.trim() || undefined,
        }),
      ),
    onSuccess: (d) => {
      setErr("");
      // Show which tunnel took effect. The backend returns the created tunnel's
      // name; when an existing tunnel was reused it may be blank, so fall back to
      // the name we already know from the probe list (or the typed name).
      const reused = tunnelId
        ? probe?.tunnels.find((tn) => tn.id === tunnelId)?.name
        : tunnelName.trim();
      const tname = d.tunnel_name || reused || tunnelName.trim() || "—";
      setOkMsg(t("advanced.cfApplied", { tunnel: tname, domain: d.domain }));
      onProvisioned(d.domain);
    },
    onError: (e: any) => {
      setOkMsg("");
      setErr(errMsg(e));
    },
  });

  return (
    <div className="space-y-3 rounded-md border border-sky-500/30 bg-sky-500/[0.06] p-3">
      <p className="text-xs text-white/50">{t("advanced.cfApiIntro")}</p>
      <button
        type="button"
        className="self-start text-sm text-emerald-300 hover:text-emerald-200 underline-offset-2 hover:underline"
        onClick={onTutorial}
      >
        {t("advanced.argoApiTokenTutorialLink")}
      </button>

      <Toggle
        checked={reuseCert}
        onChange={(v) => {
          onReuseCert(v);
          setProbe(null);
        }}
        label={t("advanced.cfReuseCertToken")}
      />
      {!reuseCert && (
        <Field label={t("advanced.cfTokenLabel")} hint={t("advanced.cfTokenHint")}>
          <Input
            type="password"
            value={token}
            onChange={(e) => {
              onToken(e.target.value);
              setProbe(null);
            }}
            placeholder="cloudflare API token"
          />
        </Field>
      )}

      <Button
        variant="default"
        disabled={probeMut.isPending}
        onClick={() => {
          setErr("");
          setOkMsg("");
          probeMut.mutate();
        }}
      >
        {probeMut.isPending ? (
          <span className="inline-flex items-center gap-2">
            <Spinner /> {t("advanced.cfProbing")}
          </span>
        ) : (
          t("advanced.cfProbeBtn")
        )}
      </Button>

      {/* Case B — token verified but lacks tunnel permission: actionable guidance. */}
      {probe && !probe.has_tunnel_perm && (
        <div className="rounded-md border border-amber-400/40 bg-amber-400/10 p-3 text-xs leading-relaxed text-amber-100/90">
          {t("advanced.argoErr.cfTokenScope")}
        </div>
      )}

      {/* Case A — token has tunnel permission: pick domain + tunnel, then apply. */}
      {probe && probe.has_tunnel_perm && (
        <div className="space-y-3 border-t border-white/10 pt-3">
          {probe.account_name && (
            <div className="text-xs text-white/40">
              {t("advanced.cfAccountFound", { account: probe.account_name })}
            </div>
          )}
          <div className="grid grid-cols-2 gap-3">
            <Field label={t("advanced.cfPickZone")}>
              <Select value={zone} onChange={(e) => setZone(e.target.value)}>
                {probe.zones.map((z) => (
                  <option key={z} value={z}>
                    {z}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label={t("advanced.cfSubdomainPrefix")}>
              <Input value={prefix} onChange={(e) => setPrefix(e.target.value)} placeholder="tunnel" />
              <p className="mt-1.5 text-xs text-amber-300 bg-amber-400/10 border border-amber-400/20 rounded-lg px-3 py-2">
                {t("advanced.cfSubdomainPrefixHint")}
              </p>
            </Field>
          </div>
          {hostname && (
            <div className="text-xs text-white/55">
              {t("advanced.cfHostnamePreview")}{" "}
              <code className="rounded bg-black/40 px-1.5 py-0.5 text-emerald-300">{hostname}</code>
            </div>
          )}

          <Field label={t("advanced.cfPickTunnel")}>
            <Select value={tunnelId} onChange={(e) => setTunnelId(e.target.value)}>
              <option value="">{t("advanced.cfCreateNew")}</option>
              {probe.tunnels.map((tn) => (
                <option key={tn.id} value={tn.id}>
                  {tn.name} ({tn.status})
                </option>
              ))}
            </Select>
          </Field>
          {!tunnelId && (
            <Field label={t("advanced.cfTunnelName")} hint={t("advanced.cfTunnelNameHint")}>
              <Input
                value={tunnelName}
                onChange={(e) => setTunnelName(e.target.value)}
                placeholder="edgenest-tunnel"
              />
            </Field>
          )}

          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="primary"
              disabled={provision.isPending || !hostname}
              onClick={() => {
                setErr("");
                setOkMsg("");
                provision.mutate();
              }}
            >
              {provision.isPending ? (
                <span className="inline-flex items-center gap-2">
                  <Spinner /> {t("advanced.cfApplying")}
                </span>
              ) : (
                t("advanced.cfApplyBtn")
              )}
            </Button>
            <span className="text-xs text-white/40">{t("advanced.cfProvisionNeedInbound")}</span>
          </div>
        </div>
      )}

      {err && <ErrorText>{err}</ErrorText>}
      {okMsg && <div className="text-sm text-emerald-400">{okMsg}</div>}
    </div>
  );
}
