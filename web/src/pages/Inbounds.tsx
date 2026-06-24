import { useMemo, useState, useSyncExternalStore } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import Layout from "../components/Layout";
import AccelStatusNote from "../components/AccelStatusNote";
import { QRCodeSVG } from "qrcode.react";
import { copyText } from "../lib/clipboard";
import { EditUserModal, type UserRow } from "./MultiUser";
import {
  PROTO_ADVANCED_DEFAULTS as SHARED_ADVANCED_DEFAULTS,
  PROTO_DEFAULT_PORTS as SHARED_DEFAULT_PORTS,
  randomPortFor as sharedRandomPortFor,
} from "../lib/protoDefaults";
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  ErrorText,
  Field,
  Input,
  Modal,
  PageHeader,
  Select,
  TextArea,
  Toggle,
  fmtBytes,
  fmtTime,
} from "../components/ui";
import { inboundBulkDelete } from "../lib/bulkDeleteStore";

// ReadOnlyValue renders a tag / protocol-type value in edit mode as a flat
// grey field with no border or dropdown chevron, signalling "this looks like
// a field but it's not editable". A small hint right under it explains why
// (engine identifier / rename via remark). Used instead of `<Input disabled>`
// which still draws the input chrome and visually invites users to try.
function ReadOnlyValue({
  children,
  mono = false,
}: {
  children: React.ReactNode;
  mono?: boolean;
}) {
  return (
    <div
      className={`w-full rounded-lg bg-white/[0.04] border border-white/5 px-3 py-2 text-sm text-white/70 select-text ${
        mono ? "font-mono" : ""
      }`}
    >
      {children}
    </div>
  );
}

interface Inbound {
  id: number;
  node_id: number;
  tag: string;
  type: string;
  engine: string;
  listen: string;
  port: number;
  network: string;
  remark: string;
  enabled: boolean;
  settings: string;
  // subscription_host is the IP literal the wizard's Step1 picked for this
  // inbound — the IP that ends up in the URI's `server` field. Distinct
  // from listen: on a NAT'd Oracle VPS listen is "::" wildcard while
  // subscription_host is the public IP clients dial. On a v4-only node
  // listen is "0.0.0.0" but subscription_host is the public v4. The list
  // row shows it (with an IPv4 / IPv6 chip) so the operator can tell
  // multi-IP / dual-stack inbounds apart at a glance.
  subscription_host?: string;
  created_at?: number;
  clients?: Client[];
}

// familyOf returns "v4" / "v6" / "" for a host literal — drives the
// IPv4 / IPv6 chip on the inbound row. DNS names return "" (no chip).
function familyOf(host: string | undefined): "v4" | "v6" | "" {
  if (!host) return "";
  if (host.includes(":")) return "v6";
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(host)) return "v4";
  return "";
}

// Per-protocol badge tint for the inbound list — makes the row scannable.
// Keep the palette wide enough that adjacent rows visually distinct, but stay
// inside the panel's muted-on-dark vibe (each tint at 15-30% opacity).
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
    case "socks":
      return "bg-slate-500/20 text-slate-300 border-slate-500/30";
    default:
      return "bg-white/5 text-white/70 border-white/15";
  }
}

function protoLabel(type: string): string {
  const hit = PROTOCOLS.find((p) => p.value === type);
  return hit?.label ?? type;
}

interface Client {
  id: number;
  inbound_id: number;
  email: string;
  uuid: string;
  password: string;
  flow: string;
  quota_bytes: number;
  expiry_at: number;
  enabled: boolean;
  traffic_up: number;
  traffic_down: number;
}

const PROTOCOLS: { value: string; label: string; engine: string }[] = [
  { value: "vless", label: "VLESS-Reality-Vision", engine: "sing-box" },
  { value: "hysteria2", label: "Hysteria2", engine: "sing-box" },
  { value: "trojan", label: "Trojan (TLS)", engine: "sing-box" },
  { value: "shadowsocks", label: "Shadowsocks-2022", engine: "sing-box" },
  { value: "tuic", label: "TUIC v5", engine: "sing-box" },
  { value: "vmess", label: "VMess-WS", engine: "sing-box" },
  { value: "vless-ws", label: "VLESS-WS", engine: "sing-box" },
  { value: "socks", label: "SOCKS5", engine: "sing-box" },
  { value: "anytls", label: "AnyTLS", engine: "sing-box" },
  { value: "vless-xhttp", label: "VLESS-XHTTP (Reality/TLS/CDN)", engine: "xray" },
];

// SNI candidates: well-known low-noise TLS endpoints suitable for Reality /
// Hy2 / Trojan SNI / domain-fronting. Curated to be neutral globally — no
// region-specific brands, no domains that 451 in major markets.
const SNI_CANDIDATES = [
  "www.microsoft.com",
  "www.cloudflare.com",
  "www.apple.com",
  "www.amazon.com",
  "www.bing.com",
  "www.icloud.com",
  "aws.amazon.com",
  "addons.mozilla.org",
  "www.wikipedia.org",
  "www.tesla.com",
];

// Per-protocol defaults are imported from `lib/protoDefaults.ts` so that
// the wizard / 一键全套 / 高级模式 three entry points always agree on the
// same port baseline and the same `advanced` starter payload. Aliases below
// keep the in-file references readable.
const ADVANCED_DEFAULTS = SHARED_ADVANCED_DEFAULTS;
const DEFAULT_PORTS = SHARED_DEFAULT_PORTS;
const randomPortFor = sharedRandomPortFor;

export default function InboundsPage({ embedded = false }: { embedded?: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  // "+ 新建入站" routes to /create-inbound (single entry point with its own
  // 快速 / 场景 / 完整 chooser). InboundFormModal only renders for edit now.
  const [editingID, setEditingID] = useState<number | null>(null);
  const [clientsFor, setClientsFor] = useState<Inbound | null>(null);
  const [rowErr, setRowErr] = useState<{ id: number; msg: string } | null>(null);
  const [pendingDeleteInbound, setPendingDeleteInbound] = useState<Inbound | null>(null);
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set());
  const [pendingBulkDelete, setPendingBulkDelete] = useState(false);
  // Bulk-delete progress lives in a module-level store so it survives leaving
  // and re-coming to this page mid-delete (0-17). Subscribe via the external
  // store; the loop itself runs outside the component.
  const bulk = useSyncExternalStore(
    inboundBulkDelete.subscribe,
    inboundBulkDelete.getState,
  );
  const bulkBusy = bulk.busy;
  const bulkProgress = bulk.busy ? { done: bulk.done, total: bulk.total } : null;
  const bulkErr =
    !bulk.busy && bulk.failed > 0
      ? t("inbounds.bulkDeletePartial", { failed: bulk.failed, first: bulk.firstErr })
      : null;

  const { data: inbounds, isLoading } = useQuery({
    queryKey: ["inbounds"],
    queryFn: () => call<Inbound[]>(api.get("/inbounds")),
  });

  // Live Argo tunnel state — drives the per-row Argo badge so an argo_bound
  // inbound reads "tunnel down, not in subscription" vs "ready" without a
  // manual refresh. Polled while the page is open.
  const { data: argoStatus } = useQuery({
    queryKey: ["argo-status"],
    queryFn: () => call<{ state: string }>(api.get("/argo/status")),
    refetchInterval: 3000,
  });
  const argoRunning = argoStatus?.state === "running";

  // Global CDN switch — a cdn_mode inbound only actually fronts through CF when
  // the operator has CDN turned on; with it off the subscription host falls back
  // to the VPS IP (see cdnPoolForLocalNode gate). Load it so the CDN badge tells
  // the truth instead of implying CF is active when it is globally disabled.
  const { data: advanced } = useQuery({
    queryKey: ["advanced"],
    queryFn: () => call<{ cdn_enabled?: boolean }>(api.get("/advanced")),
  });
  const cdnGloballyOff = advanced?.cdn_enabled === false;

  const del = useMutation({
    mutationFn: (id: number) => call(api.delete(`/inbounds/${id}`)),
    onSuccess: () => {
      setRowErr(null);
      qc.invalidateQueries({ queryKey: ["inbounds"] });
    },
    onError: (e: any, id) =>
      setRowErr({ id, msg: e?.message ?? "delete failed" }),
  });

  const allIds = useMemo(() => inbounds?.map((i) => i.id) ?? [], [inbounds]);
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
    // its progress live in the module store (survives leaving this page), and
    // it invalidates the inbounds query itself as it goes.
    setPendingBulkDelete(false);
    setSelectedIds(new Set());
    void inboundBulkDelete.run(ids, qc);
  }

  const toggle = useMutation({
    mutationFn: (ib: Inbound) =>
      call(api.put(`/inbounds/${ib.id}`, { enabled: !ib.enabled })),
    onSuccess: () => {
      setRowErr(null);
      qc.invalidateQueries({ queryKey: ["inbounds"] });
    },
    onError: (e: any, ib) =>
      setRowErr({ id: ib.id, msg: e?.message ?? "toggle failed" }),
  });

  const createBtn = (
    <Button variant="primary" onClick={() => navigate("/create-inbound")}>
      {t("inbounds.newInboundWizard")}
    </Button>
  );
  const content = (
    <>
      {embedded ? (
        <div className="flex justify-end mb-3">{createBtn}</div>
      ) : (
        <PageHeader
          title={t("inbounds.pageTitle")}
          subtitle={t("inbounds.pageSubtitle")}
          action={createBtn}
        />
      )}

      {isLoading && <p className="text-white/50">{t("inbounds.loading")}</p>}
      {inbounds && inbounds.length === 0 && (
        <Card>
          <p className="text-white/60">{t("inbounds.wizardEmpty")}</p>
        </Card>
      )}

      {inbounds && inbounds.length > 0 && (
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
                ? t("inbounds.selectNone")
                : t("inbounds.selectAll")}
            </span>
          </label>
          {someSelected && (
            <>
              <span className="text-white/40">
                {t("inbounds.selectedCount", { n: selectedIds.size })}
              </span>
              <Button
                variant="danger"
                onClick={() => setPendingBulkDelete(true)}
                disabled={bulkBusy}
              >
                {bulkBusy
                  ? t("inbounds.bulkDeleting")
                  : t("inbounds.bulkDelete")}
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
            {t("inbounds.bulkDeleteProgress", {
              done: bulkProgress.done,
              total: bulkProgress.total,
            })}
          </span>
        </div>
      )}

      <div className="space-y-3">
        {inbounds?.map((ib) => (
          <div
            key={ib.id}
            className="rounded-2xl border border-white/10 bg-white/[0.03] p-4"
          >
            <div className="flex items-center justify-between gap-4 flex-wrap">
              <div className="flex items-center gap-3">
                <input
                  type="checkbox"
                  checked={selectedIds.has(ib.id)}
                  onChange={() => toggleSelectOne(ib.id)}
                  className="shrink-0"
                  aria-label={`select ${ib.tag}`}
                />
                <Toggle
                  checked={ib.enabled}
                  onChange={() => toggle.mutate(ib)}
                  disabled={toggle.isPending && toggle.variables?.id === ib.id}
                  pendingLabel={t("inbounds.toggleSwitching")}
                  label={
                    ib.enabled
                      ? t("inbounds.toggleEnabled")
                      : t("inbounds.toggleDisabled")
                  }
                />
                {(ib.clients?.length ?? 0) === 0 && (
                  <button
                    type="button"
                    onClick={() => setClientsFor(ib)}
                    className="inline-flex items-center gap-1 rounded-md border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[11px] font-medium text-amber-200 hover:bg-amber-500/20 transition"
                    title={t("inbounds.badgeNoClientHint")}
                  >
                    ⚠ {t("inbounds.badgeNoClient")}
                  </button>
                )}
                <div>
                  <div className="font-medium">{ib.tag}</div>
                  <div className="mt-1 flex items-center gap-1.5 flex-wrap">
                    <span
                      className={`inline-flex items-center rounded-md border px-1.5 py-0.5 text-[11px] font-medium ${protoBadgeClass(
                        ib.type,
                      )}`}
                    >
                      {protoLabel(ib.type)}
                    </span>
                    {(() => {
                      const fam = familyOf(ib.subscription_host);
                      if (fam === "v4") {
                        return (
                          <span
                            title={ib.subscription_host}
                            className="inline-flex items-center rounded-md border border-sky-500/30 bg-sky-500/10 px-1.5 py-0.5 text-[11px] font-mono text-sky-300"
                          >
                            IPv4
                          </span>
                        );
                      }
                      if (fam === "v6") {
                        return (
                          <span
                            title={ib.subscription_host}
                            className="inline-flex items-center rounded-md border border-fuchsia-500/30 bg-fuchsia-500/10 px-1.5 py-0.5 text-[11px] font-mono text-fuchsia-300"
                          >
                            IPv6
                          </span>
                        );
                      }
                      return null;
                    })()}
                    <span className="inline-flex items-center rounded-md border border-white/15 bg-white/5 px-1.5 py-0.5 text-[11px] font-mono text-white/80">
                      :{ib.port}
                    </span>
                    {(() => {
                      let cdnMode = false;
                      try {
                        const s = JSON.parse(ib.settings || "{}");
                        cdnMode = s.cdn_mode === true || s.cdn_mode === "true";
                      } catch {
                        cdnMode = false;
                      }
                      if (!cdnMode) return null;
                      if (cdnGloballyOff) {
                        // CDN is globally off → this inbound falls back to the
                        // VPS host. Say so (amber) instead of implying it fronts
                        // through CF. Mirrors the silent-fallback notice rule.
                        return (
                          <span
                            title={t("inbounds.cdnBadgeOffHint")}
                            className="inline-flex items-center rounded-md border border-amber-500/40 bg-amber-500/10 px-1.5 py-0.5 text-[11px] font-medium text-amber-300"
                          >
                            {t("inbounds.cdnBadgeOff")}
                          </span>
                        );
                      }
                      return (
                        <span
                          title={t("inbounds.cdnBadgeHint")}
                          className="inline-flex items-center rounded-md border border-sky-500/40 bg-sky-500/10 px-1.5 py-0.5 text-[11px] font-medium text-sky-300"
                        >
                          {t("inbounds.cdnBadge")}
                        </span>
                      );
                    })()}
                    {(() => {
                      let argoBound = false;
                      try {
                        const s = JSON.parse(ib.settings || "{}");
                        argoBound = s.argo_bound === true || s.argo_bound === "true";
                      } catch {
                        argoBound = false;
                      }
                      if (!argoBound) return null;
                      return (
                        <span
                          title={
                            argoRunning
                              ? t("inbounds.argoReadyHint")
                              : t("inbounds.argoPendingHint")
                          }
                          className={`inline-flex items-center rounded-md border px-1.5 py-0.5 text-[11px] font-medium ${
                            argoRunning
                              ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-300"
                              : "border-amber-500/40 bg-amber-500/10 text-amber-300"
                          }`}
                        >
                          {argoRunning
                            ? t("inbounds.argoReady")
                            : t("inbounds.argoPending")}
                        </span>
                      );
                    })()}
                    <span className="text-[11px] text-white/40">{ib.engine}</span>
                    {ib.created_at ? (
                      <span className="text-[11px] text-white/40">
                        · {t("inbounds.createdAt", { time: fmtTime(ib.created_at) })}
                      </span>
                    ) : null}
                  </div>
                  {ib.subscription_host && (
                    <div className="mt-0.5 text-[11px] text-white/35 font-mono truncate max-w-[20rem]" title={ib.subscription_host}>
                      {ib.subscription_host}
                    </div>
                  )}
                </div>
              </div>
              <div className="flex items-center gap-2">
                <Button
                  variant="ghost"
                  onClick={() => setClientsFor(ib)}
                >
                  {t("inbounds.clientsBtn", {
                    count: ib.clients?.length ?? 0,
                  })}
                </Button>
                <Button
                  variant="ghost"
                  onClick={() => setEditingID(ib.id)}
                >
                  {t("inbounds.btnEdit")}
                </Button>
                <Button
                  variant="danger"
                  onClick={() => setPendingDeleteInbound(ib)}
                >
                  {t("inbounds.btnDelete")}
                </Button>
              </div>
            </div>
            {ib.remark && (
              <div className="mt-2 text-xs text-white/40">{ib.remark}</div>
            )}
            {rowErr && rowErr.id === ib.id && (
              <div className="mt-2 text-xs text-rose-300">
                {t("inbounds.rowError", { msg: rowErr.msg })}
              </div>
            )}
          </div>
        ))}
      </div>

      {editingID !== null && (
        <InboundFormModal
          mode="edit"
          id={editingID}
          onClose={() => setEditingID(null)}
          onSaved={() => {
            setEditingID(null);
            qc.invalidateQueries({ queryKey: ["inbounds"] });
          }}
        />
      )}
      {clientsFor && (
        <ClientsModal
          inbound={clientsFor}
          onClose={() => setClientsFor(null)}
        />
      )}
      <ConfirmDialog
        open={pendingDeleteInbound !== null}
        title={t("inbounds.btnDelete")}
        body={
          pendingDeleteInbound
            ? t("inbounds.confirmDeleteInbound", {
                tag: pendingDeleteInbound.tag,
              }) +
              ((() => {
                try {
                  const s = JSON.parse(pendingDeleteInbound.settings || "{}");
                  return s.argo_bound === true || s.argo_bound === "true";
                } catch {
                  return false;
                }
              })()
                ? "\n\n" + t("inbounds.confirmDeleteArgo")
                : "")
            : ""
        }
        confirmLabel={t("common.confirm")}
        cancelLabel={t("common.cancel")}
        variant="danger"
        busy={del.isPending}
        onCancel={() => setPendingDeleteInbound(null)}
        onConfirm={() => {
          if (!pendingDeleteInbound) return;
          del.mutate(pendingDeleteInbound.id, {
            onSettled: () => setPendingDeleteInbound(null),
          });
        }}
      />
      <ConfirmDialog
        open={pendingBulkDelete}
        title={t("inbounds.bulkDelete")}
        body={t("inbounds.confirmBulkDelete", { n: selectedIds.size })}
        confirmLabel={t("common.confirm")}
        cancelLabel={t("common.cancel")}
        variant="danger"
        busy={bulkBusy}
        onCancel={() => setPendingBulkDelete(false)}
        onConfirm={() => {
          runBulkDelete();
        }}
      />
    </>
  );
  return embedded ? content : <Layout>{content}</Layout>;
}

// InboundDetail mirrors the new GET /inbounds/:id response: the existing
// Inbound row + a server-side reverse-parsed `advanced` map ready to drop
// into the structured form. `advanced_error` is set when the stored settings
// JSON couldn't be parsed — in that case we fall back to raw textarea so the
// operator can fix it by hand.
interface InboundDetail extends Inbound {
  advanced?: Record<string, any>;
  advanced_error?: string;
}

function InboundFormModal({
  mode,
  id,
  onClose,
  onSaved,
}: {
  mode: "create" | "edit";
  id?: number;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const existing = useQuery({
    queryKey: ["inbound", id],
    queryFn: () => call<InboundDetail>(api.get(`/inbounds/${id}`)),
    enabled: mode === "edit" && !!id,
  });
  const ib = existing.data;

  const [tag, setTag] = useState("");
  const [type, setType] = useState("vless");
  // Initial port is randomized on first render — same logic as switching
  // protocols inside the create dialog. We use a lazy initializer so the
  // randomization happens once per mount.
  const [port, setPort] = useState(() => randomPortFor("vless"));
  const [listen, setListen] = useState("::");
  const [network, setNetwork] = useState("");
  const [remark, setRemark] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [advanced, setAdvanced] = useState<Record<string, any>>(
    ADVANCED_DEFAULTS.vless,
  );
  // Raw-mode fallback: only shown when the stored settings JSON is unparseable
  // (advanced_error set on the GET response). Lets operator recover by hand.
  const [rawFallback, setRawFallback] = useState<string | null>(null);
  const [err, setErr] = useState("");
  const initialised = !!ib || mode === "create";

  useMemo(() => {
    if (mode === "edit" && ib) {
      setTag(ib.tag);
      setType(ib.type);
      setPort(ib.port);
      setListen(ib.listen);
      setNetwork(ib.network);
      setRemark(ib.remark);
      setEnabled(ib.enabled);
      if (ib.advanced_error) {
        // Settings JSON unparseable — surface raw textarea + warning.
        try {
          setRawFallback(JSON.stringify(JSON.parse(ib.settings || "{}"), null, 2));
        } catch {
          setRawFallback(ib.settings || "{}");
        }
      } else {
        setAdvanced({
          ...ADVANCED_DEFAULTS[ib.type],
          ...(ib.advanced ?? {}),
        });
        setRawFallback(null);
      }
    }
  }, [ib, mode]);

  function handleTypeChange(next: string) {
    setType(next);
    if (mode === "create") {
      setPort(randomPortFor(next));
      setNetwork(next === "hysteria2" || next === "tuic" ? "udp" : "");
      setAdvanced(ADVANCED_DEFAULTS[next] ?? {});
    }
  }

  const setAdvField = (key: string, value: any) =>
    setAdvanced((cur) => ({ ...cur, [key]: value }));

  const save = useMutation({
    mutationFn: async () => {
      const baseFields = {
        listen,
        port: Number(port),
        network,
        remark,
        enabled,
      };
      let createdInbound: Inbound | null = null;
      if (rawFallback !== null) {
        // Raw fallback: ship the JSON directly via legacy `settings` field;
        // server still runs autofill so secrets get re-minted if dropped.
        let parsed: any = {};
        try {
          parsed = JSON.parse(rawFallback || "{}");
        } catch (e: any) {
          throw new Error(t("inbounds.errSettingsJson", { msg: e.message }));
        }
        if (mode === "create") {
          createdInbound = await call<Inbound>(
            api.post("/inbounds", {
              tag: tag.trim(),
              type,
              ...baseFields,
              settings: parsed,
            }),
          );
        } else {
          return await call<Inbound>(
            api.put(`/inbounds/${id}`, { ...baseFields, settings: parsed }),
          );
        }
      } else if (mode === "create") {
        // Normal path: structured `advanced` payload, server fills secrets.
        createdInbound = await call<Inbound>(
          api.post("/inbounds", {
            tag: tag.trim(),
            type,
            ...baseFields,
            advanced,
          }),
        );
      } else {
        return await call<Inbound>(
          api.put(`/inbounds/${id}`, { ...baseFields, advanced }),
        );
      }
      // Create-mode: every protocol's renderClientsAsUsers requires ≥1 client
      // (Invariant I1: users[].name == Client.Email). Without a client the
      // inbound stays in the "未生效·加客户端" limbo. Auto-seed a default
      // client so newly-created inbounds are immediately usable. The wizard
      // also POSTs a client; here we mirror that for the advanced modal.
      if (createdInbound) {
        try {
          await call(
            api.post(`/inbounds/${createdInbound.id}/clients`, {
              email: `default-${createdInbound.tag}@edgenest.local`,
              uuid: randomUUID(),
              password: randomHex(16),
              flow: type === "vless" ? "xtls-rprx-vision" : "",
              enabled: true,
            }),
          );
        } catch {
          // Don't block the inbound create on client seeding — user can still
          // add a client manually from the row's ⚠ badge.
        }
      }
      return createdInbound!;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["inbounds"] });
      onSaved();
    },
    onError: (e: any) =>
      setErr(e?.response?.data?.error?.message ?? e.message),
  });

  const protoSubtitle = t(`inbounds.protoSubtitles.${type}`, {
    defaultValue: "",
  });

  // Port is immutable for CDN / Argo inbounds: a CDN inbound must stay on a
  // Cloudflare-proxyable HTTPS port (cdnPortGate), and an Argo inbound's port
  // is an internal loopback port cloudflared dials — both break if moved here.
  const cdnLocked =
    mode === "edit" &&
    (advanced.cdn_mode === true || advanced.cdn_mode === "true");
  const argoLocked =
    mode === "edit" &&
    (advanced.argo_bound === true || advanced.argo_bound === "true");
  const portLocked = rawFallback === null && (cdnLocked || argoLocked);

  return (
    <Modal
      open
      onClose={onClose}
      title={
        mode === "create"
          ? t("inbounds.formCreateTitle")
          : t("inbounds.formEditTitle", { id })
      }
      size="xl"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("inbounds.btnCancel")}
          </Button>
          <Button
            variant="primary"
            disabled={save.isPending || !initialised}
            onClick={() => {
              setErr("");
              save.mutate();
            }}
          >
            {save.isPending
              ? t("inbounds.saving")
              : mode === "create"
              ? t("inbounds.btnCreate")
              : t("inbounds.btnSave")}
          </Button>
        </>
      }
    >
      {!initialised && <p className="text-white/50">{t("inbounds.loading")}</p>}
      {initialised && (
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-4">
            <Field
              label={t("inbounds.fieldTag")}
              hint={
                mode === "edit"
                  ? t("inbounds.hintTagReadOnly")
                  : t("inbounds.hintTag")
              }
            >
              {mode === "edit" ? (
                <ReadOnlyValue mono>{tag}</ReadOnlyValue>
              ) : (
                <Input
                  value={tag}
                  onChange={(e) => setTag(e.target.value)}
                  placeholder={t("inbounds.tagPlaceholder")}
                />
              )}
            </Field>
            <Field
              label={t("inbounds.fieldProtocolType")}
              hint={
                mode === "edit"
                  ? t("inbounds.hintProtocolReadOnly")
                  : protoSubtitle || undefined
              }
            >
              {mode === "edit" ? (
                <ReadOnlyValue>
                  {PROTOCOLS.find((p) => p.value === type)?.label ?? type}
                </ReadOnlyValue>
              ) : (
                <Select
                  value={type}
                  onChange={(e) => handleTypeChange(e.target.value)}
                >
                  {PROTOCOLS.map((p) => (
                    <option key={p.value} value={p.value}>
                      {p.label}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            {mode === "create" && (
              <Field label={t("inbounds.fieldListen")} hint={t("inbounds.hintListen")}>
                <Input
                  value={listen}
                  onChange={(e) => setListen(e.target.value)}
                />
              </Field>
            )}
            <Field
              label={t("inbounds.fieldPort")}
              hint={
                mode === "create"
                  ? t("inbounds.portRandomHint")
                  : cdnLocked
                  ? t("inbounds.portLockedCdn")
                  : argoLocked
                  ? t("inbounds.portLockedArgo")
                  : undefined
              }
            >
              {portLocked ? (
                <ReadOnlyValue mono>{port}</ReadOnlyValue>
              ) : (
                <Input
                  type="number"
                  value={port}
                  onChange={(e) => setPort(Number(e.target.value))}
                />
              )}
            </Field>
            {mode === "create" && (
              <Field
                label={t("inbounds.fieldNetwork")}
                hint={t("inbounds.hintNetwork")}
              >
                <Input
                  value={network}
                  onChange={(e) => setNetwork(e.target.value)}
                  placeholder={t("inbounds.networkPlaceholder")}
                />
              </Field>
            )}
            {mode === "create" && (
              <Field label={t("inbounds.fieldEnabled")}>
                <Toggle
                  checked={enabled}
                  onChange={setEnabled}
                  label={enabled ? t("inbounds.toggleOn") : t("inbounds.toggleOff")}
                />
              </Field>
            )}
          </div>
          <Field label={t("inbounds.fieldRemark")}>
            <Input
              value={remark}
              onChange={(e) => setRemark(e.target.value)}
            />
          </Field>

          {rawFallback !== null ? (
            <div className="space-y-2">
              <div className="rounded-lg border border-amber-400/30 bg-amber-500/10 p-3 text-xs text-amber-100">
                {t("inbounds.advRawWarn")}
              </div>
              <Field
                label={t("inbounds.fieldSettings")}
                hint={t("inbounds.hintSettings")}
              >
                <TextArea
                  rows={14}
                  value={rawFallback}
                  onChange={(e) => setRawFallback(e.target.value)}
                />
              </Field>
            </div>
          ) : mode === "create" ? (
            <ProtocolAdvancedForm
              type={type}
              advanced={advanced}
              setField={setAdvField}
              port={Number(port) || 0}
            />
          ) : null}

          <ErrorText>{err}</ErrorText>
        </div>
      )}
    </Modal>
  );
}

// ProtocolAdvancedForm renders the per-protocol whitelisted fields. Each
// branch matches the backend `advancedFieldsByType` whitelist in
// internal/control/api/inbound_structured.go — keep them in sync. Anything
// the user doesn't set here is autofilled server-side (secrets, TLS cert
// paths, etc.).
function ProtocolAdvancedForm({
  type,
  advanced,
  setField,
  port,
}: {
  type: string;
  advanced: Record<string, any>;
  setField: (key: string, value: any) => void;
  port: number;
}) {
  const { t } = useTranslation();
  const sniSelect = (key = "sni") => (
    <Field
      label={t("inbounds.advSniLabel")}
      hint={t("inbounds.advSniHint")}
    >
      <Select
        value={advanced[key] ?? ""}
        onChange={(e) => setField(key, e.target.value)}
      >
        {SNI_CANDIDATES.map((s) => (
          <option key={s} value={s}>
            {s}
          </option>
        ))}
        {advanced[key] && !SNI_CANDIDATES.includes(advanced[key]) && (
          <option value={advanced[key]}>{advanced[key]}</option>
        )}
      </Select>
    </Field>
  );

  switch (type) {
    case "vless":
      return (
        <div className="grid grid-cols-2 gap-4">
          {sniSelect()}
          <Field
            label={t("inbounds.advServerPortTarget")}
            hint={t("inbounds.advServerPortTargetHint")}
          >
            <Input
              type="number"
              value={advanced.server_port_target ?? 443}
              onChange={(e) =>
                setField("server_port_target", Number(e.target.value))
              }
            />
          </Field>
          <div className="col-span-2 text-xs text-white/50">
            {t("inbounds.advRealityAutofillNote")}
          </div>
        </div>
      );

    case "vless-xhttp":
      return (
        <div className="grid grid-cols-2 gap-4">
          <Field
            label={t("inbounds.advXhttpSecurity")}
            hint={t("inbounds.advXhttpSecurityHint")}
          >
            <Select
              value={advanced.security ?? "reality"}
              onChange={(e) => setField("security", e.target.value)}
            >
              <option value="reality">reality</option>
              <option value="tls">tls</option>
            </Select>
          </Field>
          {sniSelect()}
          <Field
            label={t("inbounds.advXhttpPath")}
            hint={t("inbounds.advXhttpPathHint")}
          >
            <Input
              value={advanced.xhttp_path ?? "/xhttp"}
              onChange={(e) => setField("xhttp_path", e.target.value)}
            />
          </Field>
          <Field
            label={t("inbounds.advXhttpHost")}
            hint={t("inbounds.advXhttpHostHint")}
          >
            <Input
              value={advanced.xhttp_host ?? ""}
              onChange={(e) => setField("xhttp_host", e.target.value)}
              placeholder={t("inbounds.advXhttpHostPlaceholder")}
            />
          </Field>
          {advanced.security === "tls" && (
            <>
              <Field
                label={t("inbounds.advCdnMode")}
                hint={t("inbounds.advCdnModeHint")}
              >
                <Toggle
                  checked={!!advanced.cdn_mode}
                  onChange={(v) => setField("cdn_mode", v)}
                  label={
                    advanced.cdn_mode
                      ? t("inbounds.advCdnModeOn")
                      : t("inbounds.advCdnModeOff")
                  }
                />
              </Field>
              <Field
                label={t("inbounds.advArgoBound")}
                hint={t("inbounds.advArgoBoundHint")}
              >
                <Toggle
                  checked={!!advanced.argo_bound}
                  onChange={(v) => setField("argo_bound", v)}
                  label={
                    advanced.argo_bound
                      ? t("inbounds.advArgoBoundOn")
                      : t("inbounds.advArgoBoundOff")
                  }
                />
              </Field>
              <AccelStatusNote
                port={port}
                cdnOn={!!advanced.cdn_mode}
                argoOn={!!advanced.argo_bound}
              />
            </>
          )}
        </div>
      );

    case "vless-ws":
    case "vmess":
    case "vmess-ws":
      return (
        <div className="grid grid-cols-2 gap-4">
          <Field
            label={t("inbounds.advWsPath")}
            hint={t("inbounds.advWsPathHint")}
          >
            <Input
              value={advanced.ws_path ?? "/"}
              onChange={(e) => setField("ws_path", e.target.value)}
            />
          </Field>
          <Field
            label={t("inbounds.advWsHost")}
            hint={t("inbounds.advWsHostHint")}
          >
            <Input
              value={advanced.ws_host ?? ""}
              onChange={(e) => setField("ws_host", e.target.value)}
              placeholder={t("inbounds.advWsHostPlaceholder")}
            />
          </Field>
          <Field
            label={t("inbounds.advCdnMode")}
            hint={t("inbounds.advCdnModeHint")}
          >
            <Toggle
              checked={!!advanced.cdn_mode}
              onChange={(v) => setField("cdn_mode", v)}
              label={
                advanced.cdn_mode
                  ? t("inbounds.advCdnModeOn")
                  : t("inbounds.advCdnModeOff")
              }
            />
          </Field>
          <Field
            label={t("inbounds.advArgoBound")}
            hint={t("inbounds.advArgoBoundHint")}
          >
            <Toggle
              checked={!!advanced.argo_bound}
              onChange={(v) => setField("argo_bound", v)}
              label={
                advanced.argo_bound
                  ? t("inbounds.advArgoBoundOn")
                  : t("inbounds.advArgoBoundOff")
              }
            />
          </Field>
          <AccelStatusNote
            port={port}
            cdnOn={!!advanced.cdn_mode}
            argoOn={!!advanced.argo_bound}
          />
        </div>
      );

    case "hysteria2":
      return (
        <div className="grid grid-cols-2 gap-4">
          <Field
            label={t("inbounds.advHy2Obfs")}
            hint={t("inbounds.advHy2ObfsHint")}
          >
            <Toggle
              checked={!!advanced.obfs}
              onChange={(v) => setField("obfs", v)}
              label={
                advanced.obfs
                  ? t("inbounds.advHy2ObfsOn")
                  : t("inbounds.advHy2ObfsOff")
              }
            />
          </Field>
          {sniSelect()}
          <Field
            label={t("inbounds.advHy2UpMbps")}
            hint={t("inbounds.advHy2BandwidthHint")}
          >
            <Input
              type="number"
              value={advanced.up_mbps ?? 100}
              onChange={(e) => setField("up_mbps", Number(e.target.value))}
            />
          </Field>
          <Field label={t("inbounds.advHy2DownMbps")}>
            <Input
              type="number"
              value={advanced.down_mbps ?? 500}
              onChange={(e) => setField("down_mbps", Number(e.target.value))}
            />
          </Field>
          <div className="col-span-2 text-xs text-white/50">
            {t("inbounds.advHy2AutofillNote")}
          </div>
        </div>
      );

    case "trojan":
      return (
        <div className="grid grid-cols-2 gap-4">
          {sniSelect()}
          <Field
            label={t("inbounds.advAcmeManaged")}
            hint={t("inbounds.advAcmeManagedHint")}
          >
            <Toggle
              checked={!!advanced.acme_managed}
              onChange={(v) => setField("acme_managed", v)}
              label={
                advanced.acme_managed
                  ? t("inbounds.advAcmeOn")
                  : t("inbounds.advAcmeOff")
              }
            />
          </Field>
          <div className="col-span-2 text-xs text-white/50">
            {t("inbounds.advTrojanAutofillNote")}
          </div>
        </div>
      );

    case "shadowsocks":
      return (
        <div className="grid grid-cols-2 gap-4">
          <Field
            label={t("inbounds.advSsMethod")}
            hint={t("inbounds.advSsMethodHint")}
          >
            <Select
              value={advanced.method ?? "2022-blake3-aes-128-gcm"}
              onChange={(e) => setField("method", e.target.value)}
            >
              <option value="2022-blake3-aes-128-gcm">
                2022-blake3-aes-128-gcm
              </option>
              <option value="2022-blake3-aes-256-gcm">
                2022-blake3-aes-256-gcm
              </option>
              <option value="2022-blake3-chacha20-poly1305">
                2022-blake3-chacha20-poly1305
              </option>
            </Select>
          </Field>
          <div className="col-span-2 text-xs text-white/50">
            {t("inbounds.advSsAutofillNote")}
          </div>
        </div>
      );

    case "tuic":
      return (
        <div className="grid grid-cols-2 gap-4">
          <Field
            label={t("inbounds.advTuicCC")}
            hint={t("inbounds.advTuicCCHint")}
          >
            <Select
              value={advanced.congestion_control ?? "bbr"}
              onChange={(e) =>
                setField("congestion_control", e.target.value)
              }
            >
              <option value="bbr">bbr</option>
              <option value="cubic">cubic</option>
              <option value="new_reno">new_reno</option>
            </Select>
          </Field>
          {sniSelect()}
          <Field
            label={t("inbounds.advAcmeManaged")}
            hint={t("inbounds.advAcmeManagedHint")}
          >
            <Toggle
              checked={!!advanced.acme_managed}
              onChange={(v) => setField("acme_managed", v)}
              label={
                advanced.acme_managed
                  ? t("inbounds.advAcmeOn")
                  : t("inbounds.advAcmeOff")
              }
            />
          </Field>
          <div className="col-span-2 text-xs text-white/50">
            {t("inbounds.advTuicAutofillNote")}
          </div>
        </div>
      );

    case "anytls":
      return (
        <div className="grid grid-cols-2 gap-4">
          {sniSelect()}
          <Field
            label={t("inbounds.advAcmeManaged")}
            hint={t("inbounds.advAcmeManagedHint")}
          >
            <Toggle
              checked={!!advanced.acme_managed}
              onChange={(v) => setField("acme_managed", v)}
              label={
                advanced.acme_managed
                  ? t("inbounds.advAcmeOn")
                  : t("inbounds.advAcmeOff")
              }
            />
          </Field>
          <div className="col-span-2 text-xs text-white/50">
            {t("inbounds.advAnyTLSAutofillNote")}
          </div>
        </div>
      );

    case "socks":
      return (
        <div className="grid grid-cols-2 gap-4">
          <Field
            label={t("inbounds.advSocksRequireAuth")}
            hint={t("inbounds.advSocksRequireAuthHint")}
          >
            <Toggle
              checked={!!advanced.require_auth}
              onChange={(v) => setField("require_auth", v)}
              label={
                advanced.require_auth
                  ? t("inbounds.advSocksAuthOn")
                  : t("inbounds.advSocksAuthOff")
              }
            />
          </Field>
          <Field
            label={t("inbounds.advSocksUsername")}
            hint={t("inbounds.advSocksUsernameHint")}
          >
            <Input
              value={advanced.username ?? ""}
              onChange={(e) => setField("username", e.target.value)}
            />
          </Field>
          <div className="col-span-2 text-xs text-white/50">
            {t("inbounds.advSocksAuthNote")}
          </div>
        </div>
      );

    default:
      return (
        <div className="rounded-lg border border-white/10 bg-white/[0.03] p-3 text-xs text-white/60">
          {t("inbounds.advUnknownProto")}
        </div>
      );
  }
}

function ClientsModal({
  inbound,
  onClose,
}: {
  inbound: Inbound;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [editingClient, setEditingClient] = useState<Client | null>(null);
  const [previewing, setPreviewing] = useState<Client | null>(null);
  const [pendingDeleteClient, setPendingDeleteClient] = useState<Client | null>(null);

  const { data: clients } = useQuery({
    queryKey: ["clients", inbound.id],
    queryFn: () => call<Client[]>(api.get(`/inbounds/${inbound.id}/clients`)),
  });

  // Argo gate for the share view: if this inbound rides Argo but the tunnel
  // isn't up, every subscription/URI here omits it (resolver drops the dead
  // loopback link), so warn before the operator copies an "empty" sub.
  const inboundArgoBound = (() => {
    try {
      const s = JSON.parse(inbound.settings || "{}");
      return s.argo_bound === true || s.argo_bound === "true";
    } catch {
      return false;
    }
  })();
  const { data: argoStatus } = useQuery({
    queryKey: ["argo-status"],
    queryFn: () => call<{ state: string }>(api.get("/argo/status")),
    refetchInterval: 3000,
    enabled: inboundArgoBound,
  });
  const argoNeedsStart = inboundArgoBound && argoStatus?.state !== "running";

  const del = useMutation({
    mutationFn: (id: number) =>
      call(api.delete(`/inbounds/${inbound.id}/clients/${id}`)),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["clients", inbound.id] });
      qc.invalidateQueries({ queryKey: ["inbounds"] });
    },
  });

  return (
    <>
      <Modal
        open
        onClose={onClose}
        title={t("inbounds.clientsOfTitle", { tag: inbound.tag })}
        size="xl"
        footer={
          <Button variant="primary" onClick={onClose}>
            {t("inbounds.btnClose")}
          </Button>
        }
      >
        {argoNeedsStart && (
          <div className="mb-3 rounded-md border border-amber-400/40 bg-amber-400/10 px-3 py-2 text-xs text-amber-200/90">
            {t("inbounds.argoSubGate")}{" "}
            <Link to="/inbound?tab=argo" className="underline">
              {t("inbounds.argoSubGateCta")} →
            </Link>
          </div>
        )}
        <p className="mb-3 text-xs text-white/45">{t("inbounds.clientsManageHint")}</p>
        {clients && clients.length === 0 && (
          <p className="text-white/50">{t("inbounds.noClientsYet")}</p>
        )}
        <div className="space-y-2">
          {clients?.map((cl) => (
            <div
              key={cl.id}
              className="rounded-lg border border-white/10 p-3 flex items-center justify-between gap-3 flex-wrap"
            >
              <div className="text-sm">
                <div className="flex items-center gap-2">
                  <Badge tone={cl.enabled ? "success" : "neutral"}>
                    {cl.enabled
                      ? t("inbounds.clientOn")
                      : t("inbounds.clientOff")}
                  </Badge>
                  <span className="font-medium">{cl.email}</span>
                </div>
                <div className="text-xs text-white/50 mt-1">
                  ↑ {fmtBytes(cl.traffic_up)} · ↓ {fmtBytes(cl.traffic_down)}{" "}
                  {cl.quota_bytes > 0 &&
                    `· ${t("inbounds.quotaTag", { quota: fmtBytes(cl.quota_bytes) })}`}
                  {cl.expiry_at > 0 &&
                    ` · ${t("inbounds.expiresTag", { time: fmtTime(cl.expiry_at) })}`}
                </div>
              </div>
              <div className="flex gap-2">
                <Button
                  variant="ghost"
                  disabled={!cl.enabled}
                  title={!cl.enabled ? t("inbounds.shareDisabledTip") : undefined}
                  onClick={() => setPreviewing(cl)}
                >
                  {t("inbounds.shareUris")}
                </Button>
                <Button variant="ghost" onClick={() => setEditingClient(cl)}>
                  {t("inbounds.btnEdit")}
                </Button>
                <Button
                  variant="danger"
                  onClick={() => setPendingDeleteClient(cl)}
                >
                  {t("inbounds.btnDelete")}
                </Button>
              </div>
            </div>
          ))}
        </div>
      </Modal>

      {editingClient && (
        <EditUserModal
          user={clientToUserRow(editingClient)}
          onClose={() => setEditingClient(null)}
          onSaved={() => {
            setEditingClient(null);
            qc.invalidateQueries({ queryKey: ["clients", inbound.id] });
            qc.invalidateQueries({ queryKey: ["inbounds"] });
            qc.invalidateQueries({ queryKey: ["users"] });
          }}
        />
      )}
      {previewing && clients && (
        <SharePreviewModal
          clients={clients}
          initialClientID={previewing.id}
          onClose={() => setPreviewing(null)}
        />
      )}
      <ConfirmDialog
        open={pendingDeleteClient !== null}
        title={t("inbounds.btnDelete")}
        body={
          pendingDeleteClient
            ? t("inbounds.confirmDeleteClient", {
                email: pendingDeleteClient.email,
              })
            : ""
        }
        confirmLabel={t("common.confirm")}
        cancelLabel={t("common.cancel")}
        variant="danger"
        busy={del.isPending}
        onCancel={() => setPendingDeleteClient(null)}
        onConfirm={() => {
          if (!pendingDeleteClient) return;
          del.mutate(pendingDeleteClient.id, {
            onSettled: () => setPendingDeleteClient(null),
          });
        }}
      />
    </>
  );
}

// ClientFormModal dispatches to a simplified create form (just a nickname +
// collapsible advanced sections) or the legacy full-field edit form. Splitting
// the two flows lets us minimise input on the common path (per [[feedback-
// minimize-user-input]]) without losing the ability to fine-tune existing
// clients later.
function ClientFormModal(props: {
  inboundID: number;
  client?: Client;
  onClose: () => void;
  onSaved: (created?: Client) => void;
}) {
  if (props.client) {
    return <ClientEditModal {...props} client={props.client} />;
  }
  return <ClientCreateSimpleModal {...props} />;
}

// ClientCreateSimpleModal — "just give it a name" creation flow.
// The user types a nickname (iPhone / 老婆 / 出差用); we derive everything else
// (email, uuid, password, flow) from that + sensible defaults. Two collapsed
// sections give power users an escape hatch for explicit email/UUID/flow and
// for quota/expiry.
function ClientCreateSimpleModal({
  inboundID,
  onClose,
  onSaved,
}: {
  inboundID: number;
  onClose: () => void;
  onSaved: (created?: Client) => void;
}) {
  const { t } = useTranslation();
  const [nickname, setNickname] = useState("");
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [showLimits, setShowLimits] = useState(false);
  const [emailOverride, setEmailOverride] = useState("");
  const [uuidOverride, setUuidOverride] = useState("");
  const [flow, setFlow] = useState("");
  const [quotaGB, setQuotaGB] = useState(0);
  const [expiryDays, setExpiryDays] = useState(0);
  const [err, setErr] = useState("");
  // Set when the API rejects the add with SS_INBOUND_SINGLE_CLIENT (422).
  // Triggers the "create new SS inbound" recovery path.
  const [ssBlocked, setSsBlocked] = useState(false);

  const buildPayload = () => {
    const slug =
      (nickname || "client")
        .trim()
        .toLowerCase()
        .replace(/[^a-z0-9-]+/g, "-")
        .replace(/^-+|-+$/g, "") || "client";
    const email =
      emailOverride.trim() || `${slug}-${randomHex(3)}@edgenest.local`;
    return {
      email,
      uuid: uuidOverride.trim() || randomUUID(),
      password: randomHex(16),
      flow,
      quota_bytes: quotaGB > 0 ? quotaGB * 1024 * 1024 * 1024 : 0,
      expiry_at:
        expiryDays > 0
          ? Math.floor(Date.now() / 1000) + expiryDays * 86400
          : 0,
      enabled: true,
    };
  };

  const save = useMutation({
    mutationFn: async () => {
      return await call<Client>(
        api.post(`/inbounds/${inboundID}/clients`, buildPayload()),
      );
    },
    onSuccess: (created) => onSaved(created),
    onError: (e: any) => {
      const code = e?.response?.data?.error?.code as string | undefined;
      if (code === "SS_INBOUND_SINGLE_CLIENT") {
        setSsBlocked(true);
        setErr(t("inbounds.ssSingleClientError"));
        return;
      }
      setErr(e?.response?.data?.error?.message ?? e.message);
    },
  });

  // Recovery for SS_INBOUND_SINGLE_CLIENT: spin up a fresh SS inbound that
  // mirrors the source's method/port baseline, then add the client to it.
  // We don't try to copy settings exactly — the autofill on the backend
  // mints a fresh PSK and lands the operator on a working inbound.
  const createNewSsInbound = useMutation({
    mutationFn: async () => {
      const src = await call<Inbound>(api.get(`/inbounds/${inboundID}`));
      const tag = `${src.tag}-${Math.floor(Date.now() / 1000) % 100000}`;
      const newIB = await call<{ id: number }>(
        api.post("/inbounds", {
          tag,
          type: "shadowsocks",
          port: randomPortFor("shadowsocks"),
          listen: src.listen || "::",
          network: "",
          remark: src.remark || tag,
          enabled: true,
          advanced: { method: "2022-blake3-aes-128-gcm" },
        }),
      );
      return await call<Client>(
        api.post(`/inbounds/${newIB.id}/clients`, buildPayload()),
      );
    },
    onSuccess: (created) => onSaved(created),
    onError: (e: any) =>
      setErr(e?.response?.data?.error?.message ?? e.message),
  });

  return (
    <Modal
      open
      onClose={onClose}
      title={t("inbounds.clientCreateTitle")}
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("inbounds.btnCancel")}
          </Button>
          {ssBlocked ? (
            <Button
              variant="primary"
              disabled={createNewSsInbound.isPending}
              onClick={() => {
                setErr("");
                createNewSsInbound.mutate();
              }}
            >
              {createNewSsInbound.isPending
                ? t("inbounds.saving")
                : t("inbounds.ssSingleClientCreateNew")}
            </Button>
          ) : (
            <Button
              variant="primary"
              disabled={save.isPending}
              onClick={() => {
                setErr("");
                save.mutate();
              }}
            >
              {save.isPending
                ? t("inbounds.saving")
                : t("inbounds.btnSaveAndShare")}
            </Button>
          )}
        </>
      }
    >
      <div className="space-y-4">
        <Field
          label={t("inbounds.fieldClientNickname")}
          hint={t("inbounds.hintClientNickname")}
        >
          <Input
            value={nickname}
            onChange={(e) => setNickname(e.target.value)}
            placeholder={t("inbounds.placeholderClientNickname")}
            autoFocus
          />
        </Field>

        <details
          className="rounded-lg border border-white/10 bg-white/[0.02] px-3 py-2"
          open={showAdvanced}
          onToggle={(e) =>
            setShowAdvanced((e.target as HTMLDetailsElement).open)
          }
        >
          <summary className="cursor-pointer text-sm text-white/70 select-none">
            {t("inbounds.toggleAdvancedFields")}
          </summary>
          <div className="mt-3 space-y-3">
            <Field
              label={t("inbounds.fieldClientEmail")}
              hint={t("inbounds.hintClientEmailOverride")}
            >
              <Input
                value={emailOverride}
                onChange={(e) => setEmailOverride(e.target.value)}
                placeholder={t("inbounds.placeholderEmailOverride")}
              />
            </Field>
            <Field
              label={t("inbounds.fieldClientUuid")}
              hint={t("inbounds.hintClientUuidOverride")}
            >
              <div className="flex gap-2">
                <Input
                  value={uuidOverride}
                  onChange={(e) => setUuidOverride(e.target.value)}
                  className="font-mono"
                  placeholder={t("inbounds.placeholderUuidOverride")}
                />
                <Button
                  variant="ghost"
                  onClick={() => setUuidOverride(randomUUID())}
                >
                  {t("inbounds.btnGen")}
                </Button>
              </div>
            </Field>
            <Field
              label={t("inbounds.fieldClientFlow")}
              hint={t("inbounds.hintClientFlow")}
            >
              <Input
                value={flow}
                onChange={(e) => setFlow(e.target.value)}
                placeholder="xtls-rprx-vision"
              />
            </Field>
          </div>
        </details>

        <details
          className="rounded-lg border border-white/10 bg-white/[0.02] px-3 py-2"
          open={showLimits}
          onToggle={(e) =>
            setShowLimits((e.target as HTMLDetailsElement).open)
          }
        >
          <summary className="cursor-pointer text-sm text-white/70 select-none">
            {t("inbounds.toggleQuotaExpiry")}
          </summary>
          <div className="mt-3 grid grid-cols-2 gap-4">
            <Field
              label={t("inbounds.fieldQuota")}
              hint={t("inbounds.hintQuota")}
            >
              <Input
                type="number"
                value={quotaGB}
                onChange={(e) => setQuotaGB(Number(e.target.value))}
              />
            </Field>
            <Field
              label={t("inbounds.fieldExpiryDays")}
              hint={t("inbounds.hintExpiry")}
            >
              <Input
                type="number"
                value={expiryDays}
                onChange={(e) => setExpiryDays(Number(e.target.value))}
              />
            </Field>
          </div>
        </details>

        <ErrorText>{err}</ErrorText>
      </div>
    </Modal>
  );
}

// ClientEditModal — legacy full-field form, used when editing an existing
// client. Edits surface raw email/uuid/password/flow because the user is
// modifying a known record and presumably wants explicit control.
function ClientEditModal({
  inboundID,
  client,
  onClose,
  onSaved,
}: {
  inboundID: number;
  client: Client;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [email, setEmail] = useState(client.email);
  const [uuid, setUUID] = useState(client.uuid);
  const [password, setPassword] = useState(client.password);
  const [flow, setFlow] = useState(client.flow);
  const [quotaGB, setQuotaGB] = useState(
    Math.round(client.quota_bytes / 1024 / 1024 / 1024),
  );
  const [expiryDays, setExpiryDays] = useState(0);
  const [enabled, setEnabled] = useState(client.enabled);
  const [err, setErr] = useState("");

  const save = useMutation({
    mutationFn: () => {
      const expiry =
        expiryDays > 0
          ? Math.floor(Date.now() / 1000) + expiryDays * 86400
          : client.expiry_at;
      const body = {
        email: email.trim(),
        uuid: uuid.trim() || randomUUID(),
        password: password.trim() || (uuid ? "" : randomHex(16)),
        flow,
        quota_bytes: quotaGB > 0 ? quotaGB * 1024 * 1024 * 1024 : 0,
        expiry_at: expiry,
        enabled,
      };
      return call(api.put(`/inbounds/${inboundID}/clients/${client.id}`, body));
    },
    onSuccess: () => onSaved(),
    onError: (e: any) =>
      setErr(e?.response?.data?.error?.message ?? e.message),
  });

  return (
    <Modal
      open
      onClose={onClose}
      title={t("inbounds.clientEditTitle")}
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("inbounds.btnCancel")}
          </Button>
          <Button
            variant="primary"
            disabled={save.isPending}
            onClick={() => {
              setErr("");
              save.mutate();
            }}
          >
            {save.isPending ? t("inbounds.saving") : t("inbounds.btnSave")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field
          label={t("inbounds.fieldClientEmail")}
          hint={t("inbounds.hintClientEmail")}
        >
          <Input value={email} onChange={(e) => setEmail(e.target.value)} />
        </Field>
        <Field
          label={t("inbounds.fieldClientUuid")}
          hint={t("inbounds.hintClientUuid")}
        >
          <div className="flex gap-2">
            <Input
              value={uuid}
              onChange={(e) => setUUID(e.target.value)}
              className="font-mono"
            />
            <Button variant="ghost" onClick={() => setUUID(randomUUID())}>
              {t("inbounds.btnGen")}
            </Button>
          </div>
        </Field>
        <Field
          label={t("inbounds.fieldClientPassword")}
          hint={t("inbounds.hintClientPassword")}
        >
          <div className="flex gap-2">
            <Input
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="font-mono"
            />
            <Button variant="ghost" onClick={() => setPassword(randomHex(16))}>
              {t("inbounds.btnGen")}
            </Button>
          </div>
        </Field>
        <Field label={t("inbounds.fieldClientFlow")}>
          <Input value={flow} onChange={(e) => setFlow(e.target.value)} />
        </Field>
        <div className="grid grid-cols-2 gap-4">
          <Field
            label={t("inbounds.fieldQuota")}
            hint={t("inbounds.hintQuota")}
          >
            <Input
              type="number"
              value={quotaGB}
              onChange={(e) => setQuotaGB(Number(e.target.value))}
            />
          </Field>
          <Field
            label={t("inbounds.fieldAddExpiryDays", {
              current: client.expiry_at
                ? fmtTime(client.expiry_at)
                : t("inbounds.expiryNone"),
            })}
            hint={t("inbounds.hintExpiry")}
          >
            <Input
              type="number"
              value={expiryDays}
              onChange={(e) => setExpiryDays(Number(e.target.value))}
            />
          </Field>
        </div>
        <Toggle
          checked={enabled}
          onChange={setEnabled}
          label={
            enabled
              ? t("inbounds.toggleEnabled")
              : t("inbounds.toggleDisabled")
          }
        />
        <ErrorText>{err}</ErrorText>
      </div>
    </Modal>
  );
}

// clientToUserRow adapts a per-inbound Client into the user-centric UserRow the
// shared EditUserModal consumes — so editing a user from the inbound view and
// from the multi-user tab is the exact same (global) edit.
function clientToUserRow(cl: Client): UserRow {
  return {
    email: cl.email,
    inbound_tags: [],
    inbound_ids: [],
    inbound_count: 0,
    traffic_up: cl.traffic_up,
    traffic_down: cl.traffic_down,
    quota_bytes: cl.quota_bytes,
    quota_used_pct: -1,
    expiry_at: cl.expiry_at,
    enabled: cl.enabled,
    over_quota: false,
    sub_id: 0,
  };
}

// ShareLinkRow mirrors the subscription tab's SubFormatRow exactly (the proven
// copy + inline-QR pattern): a per-protocol share URI with copy + 扫码 buttons.
// copyText works on HTTP panels via the execCommand fallback; the QR is an
// inline SVG (no popup), so nothing here can trip a browser popup blocker.
function ShareLinkRow({ label, url }: { label: string; url: string }) {
  const { t } = useTranslation();
  const [showQr, setShowQr] = useState(false);
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await copyText(url);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard blocked — text stays selectable for manual copy
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
          <Button onClick={() => setShowQr((v) => !v)}>{t("inbounds.subFormatQR")}</Button>
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

interface ShareLink {
  uri: string;
  server_host: string; // real server IP — NOT the CDN anycast / Argo host in the URI
  cdn: boolean;
  argo: boolean;
}

function SharePreviewModal({
  clients,
  initialClientID,
  onClose,
}: {
  clients: Client[];
  initialClientID: number;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [clientID, setClientID] = useState(initialClientID);
  const [hostFilter, setHostFilter] = useState("");
  const { data, error } = useQuery({
    queryKey: ["client-preview", clientID],
    queryFn: () =>
      call<{ client_id: number; email: string; host: string; links: ShareLink[] }>(
        api.get(`/clients/${clientID}/preview`),
      ),
  });

  const selected = clients.find((c) => c.id === clientID);
  // ?? [] guards the disabled-user case where the encoder returns null.
  const allLinks = data?.links ?? [];
  // Filter options are the REAL server IPs (server_host), so a CDN link's
  // Cloudflare anycast IP never becomes its own bucket — it sits under the
  // server IP it fronts, and picking that IP shows the direct + CDN links together.
  const hosts = Array.from(new Set(allLinks.map((l) => l.server_host).filter(Boolean)));
  const links = hostFilter ? allLinks.filter((l) => l.server_host === hostFilter) : allLinks;

  return (
    <Modal
      open
      onClose={onClose}
      title={t("inbounds.shareUris")}
      size="lg"
      footer={
        <Button variant="primary" onClick={onClose}>
          {t("inbounds.btnClose")}
        </Button>
      }
    >
      {error && (
        <ErrorText>{(error as any)?.response?.data?.error?.message}</ErrorText>
      )}
      <div className="grid grid-cols-2 gap-3 mb-3">
        <Field label={t("inbounds.shareFilterUser")}>
          <Select value={clientID} onChange={(e) => setClientID(Number(e.target.value))}>
            {clients.map((c) => (
              <option key={c.id} value={c.id}>
                {c.email}
                {c.enabled ? "" : ` (${t("inbounds.clientOff")})`}
              </option>
            ))}
          </Select>
        </Field>
        {hosts.length > 1 && (
          <Field label={t("inbounds.shareFilterHost")}>
            <Select value={hostFilter} onChange={(e) => setHostFilter(e.target.value)}>
              <option value="">{t("inbounds.shareFilterHostAll")}</option>
              {hosts.map((h) => (
                <option key={h} value={h}>
                  {h}
                </option>
              ))}
            </Select>
          </Field>
        )}
      </div>
      {data && links.length === 0 && (
        <div className="rounded-lg border border-amber-400/40 bg-amber-400/10 px-3 py-3 text-sm text-amber-200">
          {selected?.enabled ? t("inbounds.shareNoLinks") : t("inbounds.shareDisabledHint")}
        </div>
      )}
      {data && links.length > 0 && (
        <div className="space-y-2 max-h-[24rem] overflow-y-auto pr-1">
          {links.map((l, i) => (
            <ShareLinkRow
              key={i}
              label={
                l.uri.split("://")[0].toUpperCase() +
                (l.cdn ? " · CDN" : l.argo ? " · Argo" : "")
              }
              url={l.uri}
            />
          ))}
        </div>
      )}
    </Modal>
  );
}

function randomUUID(): string {
  const c = window.crypto as Crypto;
  if (typeof c.randomUUID === "function") {
    return c.randomUUID();
  }
  const bytes = c.getRandomValues(new Uint8Array(16));
  const hex: string[] = [];
  for (let i = 0; i < 16; i++) {
    let b = bytes[i];
    if (i === 6) b = (b & 0x0f) | 0x40;
    if (i === 8) b = (b & 0x3f) | 0x80;
    hex.push(b.toString(16).padStart(2, "0"));
  }
  return hex.join("").replace(
    /(.{8})(.{4})(.{4})(.{4})(.{12})/,
    "$1-$2-$3-$4-$5",
  );
}

function randomHex(byteLen: number): string {
  const bytes = (window.crypto as Crypto).getRandomValues(
    new Uint8Array(byteLen),
  );
  const hex: string[] = [];
  for (let i = 0; i < byteLen; i++) {
    hex.push(bytes[i].toString(16).padStart(2, "0"));
  }
  return hex.join("");
}
