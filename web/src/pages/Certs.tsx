import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import { fmtDate } from "../lib/datetime";
import Layout from "../components/Layout";
import {
  Badge,
  Button,
  ConfirmDialog,
  ErrorText,
  Field,
  Input,
  Modal,
  PageHeader,
  Select,
  Toggle,
} from "../components/ui";

interface Certificate {
  id: number;
  domain: string;
  mode: string;
  dns_provider: string;
  cert_path: string;
  key_path: string;
  issued_at: number;
  expires_at: number;
  auto_renew: boolean;
  last_error: string;
}

interface DNSField {
  key: string;
  env: string;
  secret: boolean;
  multi: boolean;
}
interface DNSProviderSpec {
  name: string;
  fields: DNSField[];
}

interface IssuePayload {
  domain: string;
  email?: string;
  mode: string;
  dns_provider?: string;
  dns_config?: Record<string, string>;
  http_port: number;
}

const DAY = 86400;
// Mirror cert.Manager.renewSoon (30 days): the scheduler renews any cert with
// this much or less left, so the projected auto-renew date is expiry − 30d.
const RENEW_BEFORE = 30 * DAY;

// fmtDate (date-only) / fmtTime (full) both honour the operator's chosen
// display timezone — see lib/datetime.

// Pull a human message out of an axios/call error (same shape the IssueModal
// reads). Empty string when there's no error so callers can `||`-chain.
function mutErr(e: unknown): string {
  if (!e) return "";
  const ax = e as { response?: { data?: { error?: { message?: string } } }; message?: string };
  return ax?.response?.data?.error?.message ?? ax?.message ?? "";
}

type TFn = (key: string, opts?: Record<string, unknown>) => string;

// Format the "retry after 2026-06-15 05:17:48 UTC" timestamp lego embeds in a
// rate-limit error into the viewer's local time. Empty string if unparseable.
function fmtRetryAfter(s: string): string {
  const iso = s.trim().replace(" ", "T") + "Z";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? "" : d.toLocaleString();
}

// Turn a raw lego/ACME error (long English strings with URLs) into a short,
// localized, action-oriented line. We never surface the raw text to users — it
// stays in the element's title attribute for operators who want the detail.
function humanizeCertError(raw: string, t: TFn): string {
  if (!raw) return "";
  const lower = raw.toLowerCase();
  if (
    lower.includes("ratelimited") ||
    lower.includes("too many certificates") ||
    lower.includes("429")
  ) {
    const m = raw.match(/retry after\s+(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2})/i);
    const when = m ? fmtRetryAfter(m[1]) : "";
    return when
      ? t("certs.errRateLimitedUntil", { time: when })
      : t("certs.errRateLimited");
  }
  if (lower.includes("email required")) return t("certs.errEmail");
  if (lower.includes("timeout") || lower.includes("deadline"))
    return t("certs.errTimeout");
  if (lower.includes("dns")) return t("certs.errDns");
  return t("certs.errGeneric");
}

export default function CertsPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { data = [] } = useQuery({
    queryKey: ["certs"],
    queryFn: () => call<Certificate[]>(api.get("/certs")),
  });
  const [issuing, setIssuing] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<Certificate | null>(null);
  const [pendingRenew, setPendingRenew] = useState<Certificate | null>(null);

  const renew = useMutation({
    mutationFn: (id: number) => call(api.post(`/certs/${id}/renew`)),
    // Refetch on both success and failure: a failed renew records last_error on
    // the row, and we want the (humanised) status to update without a reload.
    onSettled: () => qc.invalidateQueries({ queryKey: ["certs"] }),
  });
  const del = useMutation({
    mutationFn: (id: number) => call(api.delete(`/certs/${id}`)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["certs"] }),
  });
  const setAuto = useMutation({
    mutationFn: (v: { id: number; auto_renew: boolean }) =>
      call(api.patch(`/certs/${v.id}/auto-renew`, { auto_renew: v.auto_renew })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["certs"] }),
  });
  // Issuance lives in the parent (not the modal) so the modal can close the
  // instant the operator hits "issue" — the ~10s ACME round-trip then shows as
  // an inline "issuing…" banner instead of a frozen dialog.
  const issue = useMutation({
    mutationFn: (payload: IssuePayload) => call(api.post("/certs", payload)),
    onSettled: () => qc.invalidateQueries({ queryKey: ["certs"] }),
  });

  const now = Math.floor(Date.now() / 1000);

  return (
    <Layout>
      <PageHeader
        title={t("certs.title")}
        subtitle={t("certs.subtitle")}
        action={
          <Button variant="primary" onClick={() => setIssuing(true)}>
            {t("certs.issue")}
          </Button>
        }
      />

      <div className="rounded-2xl border border-white/10 bg-white/[0.03] overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-white/5 text-white/50 text-xs uppercase">
            <tr>
              <th className="px-3 py-2 text-left">{t("certs.domain")}</th>
              <th className="px-3 py-2 text-left w-20">{t("certs.mode")}</th>
              <th className="px-3 py-2 text-left w-48">{t("certs.expires")}</th>
              <th className="px-3 py-2 text-left w-36">{t("certs.auto")}</th>
              <th className="px-3 py-2 text-right w-44">{t("certs.actions")}</th>
            </tr>
          </thead>
          <tbody>
            {data.length === 0 && (
              <tr>
                <td
                  colSpan={5}
                  className="px-3 py-8 text-center text-white/40 text-sm"
                >
                  {t("certs.empty")}
                </td>
              </tr>
            )}
            {data.map((c) => {
              const expired = c.expires_at > 0 && c.expires_at < now;
              const daysLeft =
                c.expires_at > 0
                  ? Math.round((c.expires_at - now) / DAY)
                  : null;
              const soon = daysLeft !== null && !expired && daysLeft <= 30;
              const renewAt =
                c.auto_renew && c.expires_at > 0
                  ? c.expires_at - RENEW_BEFORE
                  : 0;
              const healthTone =
                expired || c.last_error
                  ? "danger"
                  : soon
                    ? "warn"
                    : "success";
              // ACME renewal is a ~10s http-01 round-trip with no streaming
              // feedback, so the button must lock for this row while in flight —
              // otherwise the operator re-clicks and burns ACME rate limits.
              const renewing = renew.isPending && renew.variables === c.id;
              return (
                <tr key={c.id} className="border-t border-white/5">
                  <td className="px-3 py-2 font-mono">
                    {c.domain}
                    {c.last_error && (
                      <div
                        className="text-xs text-red-400 mt-0.5"
                        title={c.last_error}
                      >
                        {humanizeCertError(c.last_error, t)}
                      </div>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <Badge tone="neutral">{c.mode}</Badge>
                  </td>
                  <td className="px-3 py-2">
                    {c.expires_at ? (
                      <div className="space-y-0.5">
                        <div className="flex items-center gap-2">
                          <span>{fmtDate(c.expires_at)}</span>
                          {daysLeft !== null && (
                            <Badge tone={healthTone}>
                              {expired
                                ? t("certs.expiredDays", { days: -daysLeft })
                                : t("certs.daysLeft", { days: daysLeft })}
                            </Badge>
                          )}
                        </div>
                        {c.issued_at > 0 && (
                          <div className="text-xs text-white/40 whitespace-nowrap">
                            {t("certs.issuedOn", {
                              date: fmtDate(c.issued_at),
                            })}
                          </div>
                        )}
                      </div>
                    ) : (
                      "—"
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <div className="space-y-1">
                      <Toggle
                        checked={c.auto_renew}
                        disabled={
                          setAuto.isPending && setAuto.variables?.id === c.id
                        }
                        pendingLabel={t("certs.saving")}
                        label={
                          c.auto_renew ? t("certs.autoOn") : t("certs.autoOff")
                        }
                        onChange={(v) =>
                          setAuto.mutate({ id: c.id, auto_renew: v })
                        }
                      />
                      {c.auto_renew &&
                        (renewAt > now ? (
                          <div className="text-xs text-white/40 whitespace-nowrap">
                            {t("certs.renewOn", { date: fmtDate(renewAt) })}
                          </div>
                        ) : (
                          !expired && (
                            <div className="text-xs text-amber-300/80 whitespace-nowrap">
                              {t("certs.renewSoon")}
                            </div>
                          )
                        ))}
                    </div>
                  </td>
                  <td className="px-3 py-2 text-right">
                    <div className="inline-flex gap-1">
                      <Button
                        disabled={renewing}
                        onClick={() => setPendingRenew(c)}
                      >
                        {renewing ? t("certs.renewing") : t("certs.renew")}
                      </Button>
                      <Button
                        variant="danger"
                        disabled={renewing}
                        onClick={() => setPendingDelete(c)}
                      >
                        {t("certs.del")}
                      </Button>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {/* Issuance progress / errors show here (the modal closes on submit). */}
      {issue.isPending && (
        <div className="mt-3 flex items-center gap-2 text-sm text-white/60">
          <span className="inline-block h-3 w-3 rounded-full border-2 border-white/30 border-t-white/80 animate-spin" />
          {t("certs.issuingFor", { domain: issue.variables?.domain ?? "" })}
        </div>
      )}
      {issue.isError && (
        <div className="mt-3">
          <ErrorText>{humanizeCertError(mutErr(issue.error), t)}</ErrorText>
        </div>
      )}
      {/* Renew failures surface inline on the row (last_error, humanised + auto-
          refetched). The toggle has no row slot, so its errors show here. */}
      {setAuto.isError && (
        <div className="mt-3">
          <ErrorText>{humanizeCertError(mutErr(setAuto.error), t)}</ErrorText>
        </div>
      )}

      <IssueModal
        open={issuing}
        onClose={() => setIssuing(false)}
        onSubmit={(payload) => {
          issue.mutate(payload);
          setIssuing(false);
        }}
      />

      <ConfirmDialog
        open={pendingRenew !== null}
        title={t("certs.renewConfirmTitle")}
        body={
          pendingRenew
            ? t("certs.renewConfirmBody", { domain: pendingRenew.domain })
            : ""
        }
        confirmLabel={t("certs.renew")}
        cancelLabel={t("common.cancel")}
        onCancel={() => setPendingRenew(null)}
        onConfirm={() => {
          if (!pendingRenew) return;
          // Close the dialog immediately; the row's "renewing…" button is the
          // in-flight indicator. Don't hold the dialog open for the ACME call.
          renew.mutate(pendingRenew.id);
          setPendingRenew(null);
        }}
      />

      <ConfirmDialog
        open={pendingDelete !== null}
        title={t("certs.del")}
        body={
          pendingDelete
            ? t("certs.deleteConfirm", { domain: pendingDelete.domain })
            : ""
        }
        confirmLabel={t("common.confirm")}
        cancelLabel={t("common.cancel")}
        variant="danger"
        busy={del.isPending}
        onCancel={() => setPendingDelete(null)}
        onConfirm={() => {
          if (!pendingDelete) return;
          del.mutate(pendingDelete.id, {
            onSettled: () => setPendingDelete(null),
          });
        }}
      />
    </Layout>
  );
}

function IssueModal({
  open,
  onClose,
  onSubmit,
}: {
  open: boolean;
  onClose: () => void;
  onSubmit: (payload: IssuePayload) => void;
}) {
  const { t } = useTranslation();
  const [domain, setDomain] = useState("");
  const [email, setEmail] = useState("");
  const [mode, setMode] = useState("http-01");
  const [dnsProvider, setDnsProvider] = useState("cloudflare");
  const [dnsCreds, setDnsCreds] = useState<Record<string, string>>({});
  const [httpPort, setHttpPort] = useState(80);
  const [err, setErr] = useState("");
  const [tutorialFor, setTutorialFor] = useState<string | null>(null);

  const { data: providers = [] } = useQuery({
    queryKey: ["dns-providers"],
    queryFn: () => call<DNSProviderSpec[]>(api.get("/certs/dns-providers")),
    enabled: open,
  });
  const selectedSpec = providers.find((p) => p.name === dnsProvider);

  function submit() {
    setErr("");
    if (!domain.trim()) {
      setErr(t("certs.errDomainRequired"));
      return;
    }
    onSubmit({
      domain: domain.trim(),
      email: email || undefined,
      mode,
      dns_provider: mode === "dns-01" ? dnsProvider || undefined : undefined,
      dns_config: mode === "dns-01" ? dnsCreds : undefined,
      http_port: httpPort || 80,
    });
  }

  return (
    <>
    <Modal
      open={open}
      onClose={onClose}
      title={t("certs.issueModalTitle")}
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("certs.cancel")}
          </Button>
          <Button variant="primary" onClick={submit}>
            {t("certs.issueAction")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label={t("certs.fieldDomain")}>
          <Input value={domain} onChange={(e) => setDomain(e.target.value)} />
        </Field>
        <Field
          label={t("certs.fieldEmail")}
          hint={t("certs.fieldEmailHint")}
        >
          <Input value={email} onChange={(e) => setEmail(e.target.value)} />
        </Field>
        <Field label={t("certs.fieldChallengeMode")}>
          <Select value={mode} onChange={(e) => setMode(e.target.value)}>
            <option value="http-01">{t("certs.modeHttp01")}</option>
            <option value="dns-01">{t("certs.modeDns01")}</option>
          </Select>
        </Field>
        {mode === "http-01" && (
          <Field label={t("certs.fieldHttpPort")} hint={t("certs.fieldHttpPortHint")}>
            <Input
              type="number"
              value={httpPort}
              onChange={(e) => setHttpPort(Number(e.target.value))}
            />
          </Field>
        )}
        {mode === "dns-01" && (
          <>
            <Field
              label={t("certs.fieldDnsProvider")}
              hint={t("certs.fieldDnsProviderHint")}
            >
              <Select
                value={dnsProvider}
                onChange={(e) => {
                  setDnsProvider(e.target.value);
                  setDnsCreds({});
                }}
              >
                {providers.map((p) => (
                  <option key={p.name} value={p.name}>
                    {t(`certs.dnsProv.${p.name}`)}
                  </option>
                ))}
              </Select>
            </Field>
            {selectedSpec && (
              <>
                <div className="flex items-start gap-2 -mt-2">
                  <p className="text-xs text-white/40 flex-1">
                    {t(`certs.dnsHint.${selectedSpec.name}`)}
                  </p>
                  <button
                    type="button"
                    className="text-xs text-emerald-300 underline shrink-0"
                    onClick={() => setTutorialFor(selectedSpec.name)}
                  >
                    {t("certs.dnsTutorialLink")}
                  </button>
                </div>
                {selectedSpec.fields.map((f) => (
                  <Field key={f.key} label={t(`certs.dnsF.${f.key}`)}>
                    {f.multi ? (
                      <textarea
                        className="w-full rounded-lg bg-black/30 border border-white/10 px-3 py-2 text-sm font-mono outline-none focus:border-white/30"
                        rows={4}
                        autoComplete="off"
                        value={dnsCreds[f.key] ?? ""}
                        onChange={(e) =>
                          setDnsCreds((s) => ({ ...s, [f.key]: e.target.value }))
                        }
                      />
                    ) : (
                      <Input
                        type={f.secret ? "password" : "text"}
                        autoComplete="off"
                        value={dnsCreds[f.key] ?? ""}
                        onChange={(e) =>
                          setDnsCreds((s) => ({ ...s, [f.key]: e.target.value }))
                        }
                      />
                    )}
                  </Field>
                ))}
              </>
            )}
          </>
        )}
        <ErrorText>{err}</ErrorText>
      </div>
    </Modal>

    <Modal
      open={tutorialFor !== null}
      onClose={() => setTutorialFor(null)}
      title={t("certs.dnsTutorialTitle", {
        provider: tutorialFor ? t(`certs.dnsProv.${tutorialFor}`) : "",
      })}
      size="lg"
    >
      <div className="text-sm leading-relaxed whitespace-pre-line text-white/80">
        {tutorialFor ? t(`certs.dnsTut.${tutorialFor}`) : ""}
      </div>
    </Modal>
    </>
  );
}
