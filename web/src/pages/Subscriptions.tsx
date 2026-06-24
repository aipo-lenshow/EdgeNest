import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { QRCodeSVG } from "qrcode.react";
import { api, call } from "../api/client";
import Layout from "../components/Layout";
import { SUB_FORMATS, subUrlForFmt } from "../lib/subscription";
import { copyText } from "../lib/clipboard";
import { subscriptionBulkDelete } from "../lib/bulkDeleteStore";
import {
  Badge,
  Button,
  ConfirmDialog,
  Modal,
  PageHeader,
  fmtTime,
} from "../components/ui";

interface SettingsResp {
  host: string;
}

interface Subscription {
  id: number;
  name: string;
  token?: string;
  client_id: number;
  allowed_nodes: string;
  allowed_inbounds: string;
  allowed_inbound_tags?: string[];
  // subscription_host is the IP literal the wizard's Step1 picked for this
  // batch (v4 or v6). All inbounds in this subscription share a single
  // host per the design ("one wizard batch = one IP"), so the
  // backend returns the family-correct host here. The list row uses it to
  // tag the subscription with an IPv4 / IPv6 chip so the operator can
  // tell two same-named subscriptions apart at a glance.
  subscription_host?: string;
  // orphaned is set by the backend when every inbound this subscription
  // referenced has been deleted. The row renders a "—" host chip + a rebuild
  // hint instead of silently borrowing the node-default host (whose family
  // may be wrong — the BUG-1 v6-shows-v4 class).
  orphaned?: boolean;
  // absolute_url is the family-correct full URL the backend constructed
  // (http://<host>:<panel_port>/sub/<token>, v6 host bracketed per
  // RFC 3986). When non-empty it overrides the window.location.origin
  // prefix — the panel UI is reached via whatever IP the operator typed
  // into the browser, which on a dual-stack node is almost never the
  // family the wizard bound this inbound to.
  absolute_url?: string;
  expires_at: number;
  revoked: boolean;
  created_at: number;
  // The bound user's enabled state. When false the resolver serves /sub 404
  // (the user was disabled by quota/expiry enforcement), so the link is dead
  // even if revoked/expires_at look fine.
  client_enabled?: boolean;
}

interface Inbound {
  id: number;
  tag: string;
  // settings carries the per-inbound cdn_mode / argo_bound flags (JSON string,
  // same shape the Inbounds page parses) so a subscription row can mirror the
  // CDN / Argo badges instead of being silent about acceleration.
  settings?: string;
  clients?: { id: number; email: string }[];
}

// inboundFeatures parses an inbound's settings JSON into the acceleration flags
// the row badges care about. Tolerant of missing / malformed settings.
function inboundFeatures(settings: string | undefined): {
  cdn: boolean;
  argo: boolean;
} {
  try {
    const s = JSON.parse(settings || "{}");
    return {
      cdn: s.cdn_mode === true || s.cdn_mode === "true",
      argo: s.argo_bound === true || s.argo_bound === "true",
    };
  } catch {
    return { cdn: false, argo: false };
  }
}

// buildFullUrl prefers the backend's absolute_url (family-correct, built
// from the inbound's SubscriptionHost + the panel port — owns IPv6 [ ]
// bracketing) and falls back to window.location.origin only for legacy
// rows where the backend didn't supply one (legacy migrations, or a
// subscription whose inbounds were all deleted).
function buildFullUrl(
  absoluteUrl: string | undefined,
  path: string | undefined,
): string {
  if (absoluteUrl) return absoluteUrl;
  if (!path) return "";
  if (typeof window === "undefined") return path;
  return `${window.location.origin}${path}`;
}

// familyOf returns "v4" / "v6" / "" for a host literal — used to render
// the host chip on each subscription row so the operator can tell two
// same-named subscriptions apart at a glance. DNS names return "" (no chip).
function familyOf(host: string | undefined): "v4" | "v6" | "" {
  if (!host) return "";
  if (host.includes(":")) return "v6";
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(host)) return "v4";
  return "";
}

// shortenTag turns the wizard-minted "EdgeNest-VLESS-Reality-v4-8443" into a
// chip-friendly "VLESS-Reality" so the subscription row doesn't blow up the
// column. Also accepts the legacy "EdgeNest-VLESS-Reality-8443" legacy
// shape so old migrated rows still render readable chips.
function shortenTag(tag: string): string {
  let s = tag;
  if (s.startsWith("EdgeNest-")) s = s.slice("EdgeNest-".length);
  // new: <protocol>-<family>-<port>; legacy: <protocol>-<port>
  const newShape = s.match(/^(.+)-v[46]-(\d{2,5})$/);
  if (newShape) return newShape[1];
  const legacy = s.match(/^(.+)-(\d{2,5})$/);
  if (legacy) return legacy[1];
  return s;
}

// resolveAllowedTags returns the chip list to render. Prefers the enriched
// allowed_inbound_tags field (string array of inbound tags, populated by the
// backend's subscriptionView). Falls back to parsing the raw allowed_inbounds
// JSON for legacy shapes — but only if it parses as tag strings, not the
// post-migration []uint of IDs. Returns null when "include every inbound".
function resolveAllowedTags(
  enriched: string[] | undefined,
  raw: string,
): string[] | null {
  if (Array.isArray(enriched) && enriched.length > 0) return enriched;
  if (!raw || raw.trim() === "") return null;
  try {
    const arr = JSON.parse(raw);
    if (!Array.isArray(arr) || arr.length === 0) return null;
    const tags = arr.filter((v) => typeof v === "string") as string[];
    return tags.length > 0 ? tags : null;
  } catch {
    return null;
  }
}

function SubFormatRow({ label, url }: { label: string; url: string }) {
  const { t } = useTranslation();
  const [showQr, setShowQr] = useState(false);
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await copyText(url);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard blocked
    }
  };
  return (
    <div className="rounded-lg border border-white/10 bg-black/20 p-3">
      <div className="flex items-start justify-between gap-3 mb-2">
        <div className="text-xs font-medium text-white/80 flex-1 min-w-0">{label}</div>
        <div className="flex gap-1 shrink-0">
          <Button onClick={copy}>
            {copied ? t("inbounds.subFormatCopied") : t("inbounds.subFormatCopy")}
          </Button>
          <Button onClick={() => setShowQr((v) => !v)}>
            {t("inbounds.subFormatQR")}
          </Button>
        </div>
      </div>
      <div className="font-mono text-emerald-300 break-all bg-black/40 rounded p-2 text-xs">
        {url}
      </div>
      {showQr && (
        <div className="mt-3 flex justify-center">
          <div className="bg-white rounded-lg p-3">
            <QRCodeSVG value={url} size={160} level="M" />
          </div>
        </div>
      )}
    </div>
  );
}

export default function SubscriptionsPage({ embedded = false }: { embedded?: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { data: subs = [] } = useQuery({
    queryKey: ["subscriptions"],
    queryFn: () => call<Subscription[]>(api.get("/subscriptions")),
  });
  const { data: inbounds = [] } = useQuery({
    queryKey: ["inbounds"],
    queryFn: () => call<Inbound[]>(api.get("/inbounds")),
  });
  const { data: settings } = useQuery({
    queryKey: ["settings"],
    queryFn: () => call<SettingsResp>(api.get("/settings")),
  });
  const hostMissing = !!settings && !settings.host;

  // Global CDN switch + live Argo tunnel state — same signals the Inbounds page
  // uses, so the subscription-row CDN / Argo badges tell the same truth (CDN off
  // globally → fronting inactive; tunnel down → Argo not yet ready).
  const { data: advanced } = useQuery({
    queryKey: ["advanced"],
    queryFn: () => call<{ cdn_enabled?: boolean }>(api.get("/advanced")),
  });
  const cdnGloballyOff = advanced?.cdn_enabled === false;
  const { data: argoStatus } = useQuery({
    queryKey: ["argo-status"],
    queryFn: () => call<{ state: string }>(api.get("/argo/status")),
    refetchInterval: 3000,
  });
  const argoRunning = argoStatus?.state === "running";

  const clientEmailById = new Map<number, string>();
  // featByTag maps each inbound tag → its acceleration flags, so a subscription
  // (a bundle of inbounds, all sharing one host) can aggregate "does any included
  // inbound use CDN / Argo" for its row badges.
  const featByTag = new Map<string, { cdn: boolean; argo: boolean }>();
  for (const ib of inbounds) {
    featByTag.set(ib.tag, inboundFeatures(ib.settings));
    for (const c of ib.clients ?? []) {
      if (!clientEmailById.has(c.id)) clientEmailById.set(c.id, c.email);
    }
  }
  // subFeatures aggregates acceleration flags across the inbounds a subscription
  // includes. tags === null means "every inbound", so we scan them all.
  function subFeatures(tags: string[] | null): { cdn: boolean; argo: boolean } {
    const names = tags ?? inbounds.map((i) => i.tag);
    let cdn = false;
    let argo = false;
    for (const tag of names) {
      const f = featByTag.get(tag);
      if (f?.cdn) cdn = true;
      if (f?.argo) argo = true;
    }
    return { cdn, argo };
  }

  const [viewingId, setViewingId] = useState<number | null>(null);
  const [pending, setPending] = useState<
    { kind: "revoke" | "delete"; id: number } | null
  >(null);
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set());
  const [pendingBulkDelete, setPendingBulkDelete] = useState(false);
  // Bulk-delete progress lives in a module-level store so it survives leaving
  // and re-coming to this page mid-delete (mirrors Inbounds, BUGLOG 0-13/0-17).
  const bulk = useSyncExternalStore(
    subscriptionBulkDelete.subscribe,
    subscriptionBulkDelete.getState,
  );
  const bulkBusy = bulk.busy;
  const bulkProgress = bulk.busy ? { done: bulk.done, total: bulk.total } : null;
  const bulkErr =
    !bulk.busy && bulk.failed > 0
      ? t("subscriptions.bulkDeletePartial", {
          failed: bulk.failed,
          first: bulk.firstErr,
        })
      : null;

  const revoke = useMutation({
    mutationFn: (id: number) => call(api.post(`/subscriptions/${id}/revoke`)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["subscriptions"] }),
  });
  const del = useMutation({
    mutationFn: (id: number) => call(api.delete(`/subscriptions/${id}`)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["subscriptions"] }),
  });

  const allIds = subs.map((s) => s.id);
  const allSelected = allIds.length > 0 && selectedIds.size === allIds.length;
  const someSelected = selectedIds.size > 0;

  function toggleSelectOne(id: number) {
    const next = new Set(selectedIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setSelectedIds(next);
  }

  function toggleSelectAll() {
    if (allSelected) setSelectedIds(new Set());
    else setSelectedIds(new Set(allIds));
  }

  function runBulkDelete() {
    const ids = Array.from(selectedIds);
    // Close the confirm dialog immediately and clear the selection; the loop +
    // its progress live in the module store (survives leaving this page) and it
    // invalidates the subscriptions query itself as it goes.
    setPendingBulkDelete(false);
    setSelectedIds(new Set());
    void subscriptionBulkDelete.run(ids, qc);
  }

  const now = Math.floor(Date.now() / 1000);

  const content = (
    <>
      {!embedded && (
        <PageHeader
          title={t("subscriptions.pageTitle")}
          subtitle={t("subscriptions.pageSubtitle")}
        />
      )}

      {hostMissing && (
        <div className="mb-4 rounded-xl border border-amber-400/40 bg-amber-400/10 px-4 py-3 text-sm text-amber-200">
          <div className="font-medium mb-1">{t("subscriptions.hostMissingTitle")}</div>
          <div className="text-amber-100/90">
            {t("subscriptions.hostMissingBody")}{" "}
            <Link to="/settings" className="underline text-amber-50 hover:text-white">
              {t("subscriptions.hostMissingCta")}
            </Link>
          </div>
        </div>
      )}

      <details className="mb-4 rounded-xl border border-white/10 bg-white/[0.03]">
        <summary className="cursor-pointer select-none px-4 py-3 text-sm font-medium text-white/80">
          {t("subscriptions.helpTitle")}
        </summary>
        <div className="px-4 pb-4 text-sm text-white/70 space-y-3 border-t border-white/5 pt-3">
          <p>{t("subscriptions.helpIntro")}</p>
          <div>
            <div className="font-medium text-white/85 mb-1">{t("subscriptions.helpStepsTitle")}</div>
            <ol className="list-decimal pl-5 space-y-1 text-white/65">
              <li>{t("subscriptions.helpStep1")}</li>
              <li>{t("subscriptions.helpStep2")}</li>
            </ol>
          </div>
          <div>
            <div className="font-medium text-white/85 mb-1">{t("subscriptions.helpLifecycleTitle")}</div>
            <ul className="list-disc pl-5 space-y-1 text-white/65">
              <li>{t("subscriptions.helpLifecycleView")}</li>
              <li>{t("subscriptions.helpLifecycleRotate")}</li>
              <li>{t("subscriptions.helpLifecycleRevoke")}</li>
              <li>{t("subscriptions.helpLifecycleDelete")}</li>
            </ul>
          </div>
        </div>
      </details>

      {subs.length > 0 && (
        <div className="flex items-center gap-3 mb-3 px-1 text-sm">
          <label className="inline-flex items-center gap-2 cursor-pointer">
            <input
              type="checkbox"
              checked={allSelected}
              ref={(el) => {
                if (el) el.indeterminate = someSelected && !allSelected;
              }}
              onChange={toggleSelectAll}
            />
            <span className="text-white/70">
              {allSelected
                ? t("subscriptions.selectNone")
                : t("subscriptions.selectAll")}
            </span>
          </label>
          {someSelected && (
            <>
              <span className="text-white/40">
                {t("subscriptions.selectedCount", { n: selectedIds.size })}
              </span>
              <Button
                variant="danger"
                onClick={() => setPendingBulkDelete(true)}
                disabled={bulkBusy}
              >
                {bulkBusy
                  ? t("subscriptions.bulkDeleting")
                  : t("subscriptions.bulkDelete")}
              </Button>
            </>
          )}
          {bulkErr && (
            <span className="text-rose-300 text-xs">{bulkErr}</span>
          )}
        </div>
      )}

      {bulkProgress && (
        <div className="flex items-center gap-2 mb-3 px-1 text-sm text-white/70">
          <span className="inline-block h-3.5 w-3.5 shrink-0 animate-spin rounded-full border-2 border-white/20 border-t-white/70" />
          <span>
            {t("subscriptions.bulkDeleteProgress", {
              done: bulkProgress.done,
              total: bulkProgress.total,
            })}
          </span>
        </div>
      )}

      <div className="rounded-2xl border border-white/10 bg-white/[0.03] overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-white/5 text-white/50 text-xs uppercase">
            <tr>
              <th className="px-3 py-2 text-left w-10"></th>
              <th className="px-3 py-2 text-left">{t("subscriptions.tableName")}</th>
              <th className="px-3 py-2 text-left w-24">{t("subscriptions.tableClient")}</th>
              <th className="px-3 py-2 text-left w-44">{t("subscriptions.tableExpires")}</th>
              <th className="px-3 py-2 text-left w-20">{t("subscriptions.tableState")}</th>
              <th className="px-3 py-2 text-right w-72">{t("subscriptions.tableActions")}</th>
            </tr>
          </thead>
          <tbody>
            {subs.length === 0 && (
              <tr>
                <td
                  colSpan={6}
                  className="px-3 py-8 text-center text-white/40 text-sm"
                >
                  {t("subscriptions.emptyState")}
                </td>
              </tr>
            )}
            {subs.map((s) => {
              const expired = s.expires_at > 0 && s.expires_at < now;
              const tags = resolveAllowedTags(s.allowed_inbound_tags, s.allowed_inbounds);
              return (
                <tr key={s.id} className="border-t border-white/5">
                  <td className="px-3 py-2 align-top pt-3">
                    <input
                      type="checkbox"
                      checked={selectedIds.has(s.id)}
                      onChange={() => toggleSelectOne(s.id)}
                      aria-label={`select ${s.name || s.id}`}
                    />
                  </td>
                  <td className="px-3 py-2">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span>{s.name || t("subscriptions.defaultName", { id: s.id })}</span>
                      {(() => {
                        const fam = familyOf(s.subscription_host);
                        if (fam === "v4") {
                          return (
                            <span
                              title={s.subscription_host}
                              className="inline-flex rounded-md border border-sky-500/30 bg-sky-500/10 px-1.5 py-0.5 text-[10px] font-mono text-sky-300"
                            >
                              IPv4
                            </span>
                          );
                        }
                        if (fam === "v6") {
                          return (
                            <span
                              title={s.subscription_host}
                              className="inline-flex rounded-md border border-fuchsia-500/30 bg-fuchsia-500/10 px-1.5 py-0.5 text-[10px] font-mono text-fuchsia-300"
                            >
                              IPv6
                            </span>
                          );
                        }
                        if (s.orphaned) {
                          return (
                            <span
                              title={t("subscriptions.orphanedHint")}
                              className="inline-flex rounded-md border border-white/20 bg-white/5 px-1.5 py-0.5 text-[10px] font-mono text-white/40"
                            >
                              —
                            </span>
                          );
                        }
                        return null;
                      })()}
                      {(() => {
                        const feat = subFeatures(tags);
                        const out = [];
                        if (feat.cdn) {
                          out.push(
                            cdnGloballyOff ? (
                              <span
                                key="cdn"
                                title={t("subscriptions.cdnBadgeOffHint")}
                                className="inline-flex rounded-md border border-amber-500/40 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-medium text-amber-300"
                              >
                                {t("inbounds.cdnBadgeOff")}
                              </span>
                            ) : (
                              <span
                                key="cdn"
                                title={t("subscriptions.cdnBadgeHint")}
                                className="inline-flex rounded-md border border-sky-500/40 bg-sky-500/10 px-1.5 py-0.5 text-[10px] font-medium text-sky-300"
                              >
                                {t("inbounds.cdnBadge")}
                              </span>
                            ),
                          );
                        }
                        if (feat.argo) {
                          out.push(
                            <span
                              key="argo"
                              title={
                                argoRunning
                                  ? t("subscriptions.argoReadyHint")
                                  : t("subscriptions.argoPendingHint")
                              }
                              className={`inline-flex rounded-md border px-1.5 py-0.5 text-[10px] font-medium ${
                                argoRunning
                                  ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-300"
                                  : "border-amber-500/40 bg-amber-500/10 text-amber-300"
                              }`}
                            >
                              {argoRunning
                                ? t("inbounds.argoReady")
                                : t("inbounds.argoPending")}
                            </span>,
                          );
                        }
                        return out;
                      })()}
                    </div>
                    {s.subscription_host && (
                      <div className="text-[11px] text-white/35 font-mono mt-0.5 truncate max-w-[16rem]" title={s.subscription_host}>
                        {s.subscription_host}
                      </div>
                    )}
                    {s.orphaned && (
                      <div className="text-[11px] text-amber-500/80 mt-0.5">
                        {t("subscriptions.orphanedHint")}
                      </div>
                    )}
                    <div className="text-xs text-white/40">
                      {t("subscriptions.createdAt", { time: fmtTime(s.created_at) })}
                    </div>
                  </td>
                  <td className="px-3 py-2 text-white/70 truncate max-w-[12rem]" title={clientEmailById.get(s.client_id) ?? `#${s.client_id}`}>
                    {clientEmailById.get(s.client_id) ?? `#${s.client_id}`}
                  </td>
                  <td className="px-3 py-2">
                    {s.expires_at ? fmtTime(s.expires_at) : t("subscriptions.expiresNever")}
                  </td>
                  <td className="px-3 py-2">
                    {s.revoked ? (
                      <Badge tone="danger">{t("subscriptions.stateRevoked")}</Badge>
                    ) : s.client_enabled === false ? (
                      <Badge tone="warn">{t("subscriptions.stateUserDisabled")}</Badge>
                    ) : expired ? (
                      <Badge tone="warn">{t("subscriptions.stateExpired")}</Badge>
                    ) : (
                      <Badge tone="success">{t("subscriptions.stateActive")}</Badge>
                    )}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <div className="inline-flex gap-1">
                      <Button
                        variant="primary"
                        onClick={() => setViewingId(s.id)}
                      >
                        {t("subscriptions.btnView")}
                      </Button>
                      <Button
                        disabled={s.revoked}
                        onClick={() => setPending({ kind: "revoke", id: s.id })}
                      >
                        {t("subscriptions.btnRevoke")}
                      </Button>
                      <Button
                        variant="danger"
                        onClick={() => setPending({ kind: "delete", id: s.id })}
                      >
                        {t("subscriptions.btnDelete")}
                      </Button>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <DetailModal
        id={viewingId}
        onClose={() => setViewingId(null)}
      />
      <ConfirmDialog
        open={pending !== null}
        title={
          pending?.kind === "delete"
            ? t("subscriptions.btnDelete")
            : t("subscriptions.btnRevoke")
        }
        body={
          pending?.kind === "delete"
            ? t("subscriptions.confirmDelete", { id: pending.id })
            : pending?.kind === "revoke"
              ? t("subscriptions.confirmRevoke")
              : ""
        }
        confirmLabel={t("common.confirm")}
        cancelLabel={t("common.cancel")}
        variant="danger"
        busy={revoke.isPending || del.isPending}
        onCancel={() => setPending(null)}
        onConfirm={() => {
          if (!pending) return;
          const action = pending.kind === "delete" ? del : revoke;
          action.mutate(pending.id, {
            onSettled: () => setPending(null),
          });
        }}
      />
      <ConfirmDialog
        open={pendingBulkDelete}
        title={t("subscriptions.bulkDelete")}
        body={t("subscriptions.confirmBulkDelete", { n: selectedIds.size })}
        confirmLabel={t("common.confirm")}
        cancelLabel={t("common.cancel")}
        variant="danger"
        onCancel={() => setPendingBulkDelete(false)}
        onConfirm={runBulkDelete}
      />
    </>
  );
  return embedded ? content : <Layout>{content}</Layout>;
}

interface SubscriptionDetail {
  id: number;
  name: string;
  token: string;
  client_id: number;
  allowed_inbounds: string;
  allowed_inbound_tags?: string[];
  // Same family-correct host the list row shows. Useful in the detail
  // modal because the title alone doesn't tell the operator which family
  // they're about to copy / scan.
  subscription_host?: string;
  // absolute_url is the backend-built family-correct subscription URL.
  // See the comment on Subscription.absolute_url.
  absolute_url?: string;
  expires_at: number;
  revoked: boolean;
  created_at: number;
  url: string;
  client_enabled?: boolean;
}

function DetailModal({
  id,
  onClose,
}: {
  id: number | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [confirmRotate, setConfirmRotate] = useState(false);
  const { data: detail, isLoading } = useQuery({
    queryKey: ["subscription-detail", id],
    queryFn: () => call<SubscriptionDetail>(api.get(`/subscriptions/${id}`)),
    enabled: id !== null,
  });

  const rotate = useMutation({
    mutationFn: () => call<{ id: number; token: string; url: string }>(
      api.post(`/subscriptions/${id}/rotate`),
    ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["subscriptions"] });
      qc.invalidateQueries({ queryKey: ["subscription-detail", id] });
    },
  });

  const fullUrl = detail ? buildFullUrl(detail.absolute_url, detail.url) : "";

  const copy = async (text: string) => {
    try {
      await copyText(text);
    } catch {
      // clipboard blocked — fall back to selection
    }
  };

  return (
    <Modal
      open={id !== null}
      onClose={onClose}
      title={t("subscriptions.detailTitle")}
      size="lg"
      footer={
        <>
          <Button
            disabled={rotate.isPending}
            onClick={() => setConfirmRotate(true)}
          >
            {rotate.isPending ? t("subscriptions.btnRotating") : t("subscriptions.btnRotate")}
          </Button>
          <Button variant="primary" onClick={onClose}>
            {t("subscriptions.btnClose")}
          </Button>
        </>
      }
    >
      <ConfirmDialog
        open={confirmRotate}
        title={t("subscriptions.btnRotate")}
        body={t("subscriptions.confirmRotate")}
        confirmLabel={t("common.confirm")}
        cancelLabel={t("common.cancel")}
        variant="danger"
        busy={rotate.isPending}
        onCancel={() => setConfirmRotate(false)}
        onConfirm={() => {
          rotate.mutate(undefined, {
            onSettled: () => setConfirmRotate(false),
          });
        }}
      />
      {isLoading && <div className="text-white/50 text-sm">{t("common.loading")}</div>}
      {detail && (
        <div className="space-y-4 text-sm">
          <div className="rounded-lg border border-sky-400/30 bg-sky-400/10 px-3 py-2 text-sky-100 text-xs">
            {t("subscriptions.detailIAHint")}
          </div>
          {(() => {
            const tags = resolveAllowedTags(detail.allowed_inbound_tags, detail.allowed_inbounds);
            return (
              <div className="rounded-lg border border-white/10 bg-white/[0.02] px-3 py-2">
                <div className="text-[11px] uppercase tracking-wide text-white/40 mb-1">
                  {t("subscriptions.detailInboundsLabel")}
                </div>
                {tags === null ? (
                  <Badge tone="neutral">{t("subscriptions.inboundsAll")}</Badge>
                ) : (
                  <div className="flex flex-wrap gap-1">
                    {tags.map((tag) => (
                      <span
                        key={tag}
                        title={tag}
                        className="inline-flex rounded-md border border-emerald-500/30 bg-emerald-500/10 px-1.5 py-0.5 text-[11px] font-mono text-emerald-300"
                      >
                        {shortenTag(tag)}
                      </span>
                    ))}
                  </div>
                )}
              </div>
            );
          })()}
          {detail.revoked && (
            <div className="rounded-lg border border-rose-400/40 bg-rose-400/10 px-3 py-2 text-rose-200">
              {t("subscriptions.detailRevokedNote")}
            </div>
          )}
          {!detail.revoked && detail.client_enabled === false && (
            <div className="rounded-lg border border-amber-400/40 bg-amber-400/10 px-3 py-2 text-amber-200">
              {t("subscriptions.detailUserDisabledNote")}
            </div>
          )}
          {!fullUrl && !detail.revoked && (
            <div className="rounded-lg border border-amber-400/40 bg-amber-400/10 px-3 py-2 text-amber-200">
              {t("subscriptions.detailNoTokenStored")}
            </div>
          )}
          {fullUrl && !detail.revoked && (
            <div className="space-y-3">
              <div className="rounded-lg border border-amber-400/40 bg-amber-400/10 px-3 py-2 text-amber-200 text-xs">
                {t("createInbound.subInsecureHint")}
              </div>
              {SUB_FORMATS.map((f) => (
                <SubFormatRow
                  key={f.fmt}
                  label={t(`inbounds.subFormat.${f.i18nKey}` as any)}
                  url={subUrlForFmt(fullUrl, f.fmt)}
                />
              ))}
              <div className="pt-2 border-t border-white/5">
                <div className="text-xs text-white/40 mb-1">{t("subscriptions.tokenLabel")}</div>
                <div className="flex gap-2 items-start">
                  <div className="font-mono break-all bg-black/30 rounded p-2 flex-1 text-xs">
                    {detail.token}
                  </div>
                  <Button onClick={() => copy(detail.token)}>{t("subscriptions.btnCopy")}</Button>
                </div>
              </div>
            </div>
          )}
        </div>
      )}
    </Modal>
  );
}
