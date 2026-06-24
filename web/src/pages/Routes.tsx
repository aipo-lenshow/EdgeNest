import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useSearchParams } from "react-router-dom";
import { api, call } from "../api/client";
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

interface RouteRule {
  id: number;
  type: string;
  value: string;
  outbound: string;
  enabled: boolean;
  order: number;
  source: string; // "ai" | "streaming" | "custom"
}

const TYPES = [
  "domain",
  "domain_suffix",
  "domain_keyword",
  "domain_regex",
  "geosite",
  "geoip",
  "ip_cidr",
  "process_name",
];

// Outbound IDs the engine plumbing recognises. "warp" only resolves when the
// WARP page has been configured — we don't filter here, but we hint in the UI.
const OUTBOUNDS = ["direct", "block", "warp"];

const SOURCES = ["ai", "streaming", "google", "social", "dev", "cn", "ads", "custom", "captured"];

// PRESET_KEYS are the sources that come from a curated preset (vs custom /
// captured). Rules tagged with one are "half-locked" in the editor — the domain
// and type are fixed by the preset, only the outbound and on/off are the
// operator's to change.
const PRESET_KEYS = ["ai", "streaming", "google", "social", "dev", "cn", "ads"];
const isPresetSource = (s: string) => PRESET_KEYS.includes(s);

type BulkAction = "delete" | "enable" | "disable";

function sourceTone(src: string): "neutral" | "success" | "warn" | "info" {
  if (src === "ai") return "success";
  if (src === "streaming") return "warn";
  if (src === "captured") return "info";
  return "neutral";
}

interface PresetDTO {
  key: string;
  name: string;
  domains: string[];
  recommend: string; // "warp" | "direct" | "block"
}

// RoutePresetsCard offers one-click category routing AND shows live applied
// state: for each curated group it cross-references the actual rules (by
// source tag) so the operator sees, at a glance, how many of the group's
// domains are routed, whether they're on or off, and where they go — top and
// bottom stay in sync. Expand a group to see every domain's state; "view rules"
// filters the table to that group.
function RoutePresetsCard({
  rules,
  onApplied,
  onViewRules,
  initialPreset,
}: {
  rules: RouteRule[];
  onApplied: () => void;
  onViewRules: (source: string) => void;
  initialPreset?: string | null;
}) {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = useState(true);
  const [outbounds, setOutbounds] = useState<Record<string, string>>({});
  const [err, setErr] = useState("");

  const presets = useQuery({
    queryKey: ["route-presets"],
    queryFn: () => call<PresetDTO[]>(api.get("/routes/presets")),
  });

  // A deep-link (?preset=ai from the Detect tab) opens the card, pre-selects
  // WARP for that group and filters the table to it so its rules are front and
  // centre.
  useEffect(() => {
    if (initialPreset) {
      setCollapsed(false);
      setOutbounds((m) => ({ ...m, [initialPreset]: m[initialPreset] ?? "warp" }));
      onViewRules(initialPreset);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialPreset]);

  const label = (p: PresetDTO) => {
    const k = `warp.preset_${p.key}`;
    const s = t(k);
    return s === k ? p.name : s;
  };

  // Per-group derived state from the live rules — this is what keeps the card
  // honest (delete/disable a rule below → these recompute instantly).
  const stateFor = (p: PresetDTO) => {
    const applied = rules.filter((r) => r.source === p.key);
    const byValue = new Map(applied.map((r) => [r.value, r]));
    const present = p.domains.filter((d) => byValue.has(d));
    const enabled = present.filter((d) => byValue.get(d)?.enabled).length;
    const outs = Array.from(new Set(applied.map((r) => r.outbound)));
    return {
      applied,
      byValue,
      presentCount: present.length,
      enabledCount: enabled,
      total: p.domains.length,
      missing: p.domains.filter((d) => !byValue.has(d)),
      outbound: outs.length === 0 ? "" : outs.length === 1 ? outs[0] : "mixed",
    };
  };

  const apply = useMutation({
    mutationFn: (vars: { p: PresetDTO; outbound: string; enabled: boolean }) =>
      call<{ added: number; skipped: number }>(
        api.post("/routes/presets/apply", {
          group: vars.p.key,
          outbound: vars.outbound,
          enabled: vars.enabled,
        }),
      ),
    onSuccess: () => {
      setErr("");
      onApplied();
    },
    onError: (e: any) => setErr(e?.response?.data?.error?.message ?? e.message),
  });

  const remove = useMutation({
    mutationFn: (ids: number[]) =>
      call(api.post("/routes/bulk", { action: "delete", ids })),
    onSuccess: () => {
      setErr("");
      onApplied();
    },
    onError: (e: any) => setErr(e?.response?.data?.error?.message ?? e.message),
  });

  const busy = apply.isPending || remove.isPending;

  const chosenOut = (p: PresetDTO, st: ReturnType<typeof stateFor>) =>
    outbounds[p.key] ??
    (st.outbound && st.outbound !== "mixed" ? st.outbound : p.recommend ?? "warp");

  return (
    <div className="mb-3 rounded-xl border border-white/10 bg-white/[0.03]">
      <button
        type="button"
        onClick={() => setCollapsed((c) => !c)}
        className="flex w-full items-center justify-between px-4 py-2.5 text-sm"
      >
        <span className="font-medium">{t("routes.presetsCardTitle")}</span>
        <span className="text-white/40">{collapsed ? "▸" : "▾"}</span>
      </button>
      {!collapsed && (
        <div className="border-t border-white/10 px-4 py-3">
          <p className="mb-3 text-xs text-white/50">{t("routes.presetsCardIntro")}</p>
          <div className="grid gap-2 sm:grid-cols-2">
            {(presets.data ?? []).map((p) => {
              const st = stateFor(p);
              const out = chosenOut(p, st);
              return (
                <div
                  key={p.key}
                  className="flex flex-wrap items-center gap-2 rounded-lg border border-white/10 bg-white/[0.02] px-3 py-2"
                >
                  <span className="grow text-sm">{label(p)}</span>
                  {/* Live state badge — based on ENABLED rules, so rules that are
                      only materialised (created switched-off) read as "not on",
                      not "applied". */}
                  {st.enabledCount === 0 ? (
                    <Badge tone="neutral">{t("routes.presetNoneOn")}</Badge>
                  ) : st.enabledCount < st.total ? (
                    <Badge tone="warn">
                      {t("routes.presetSomeOn", { n: st.enabledCount, m: st.total })}
                    </Badge>
                  ) : (
                    <Badge tone="success">{t("routes.presetAllOn", { m: st.total })}</Badge>
                  )}
                  {/* outbound is the egress for rules this group still needs to
                      materialise; once all exist there's nothing left to create. */}
                  {st.missing.length > 0 && (
                    <div className="w-20">
                      <Select
                        value={out}
                        onChange={(e) => setOutbounds((m) => ({ ...m, [p.key]: e.target.value }))}
                      >
                        {OUTBOUNDS.map((o) => (
                          <option key={o} value={o}>{o}</option>
                        ))}
                      </Select>
                    </div>
                  )}
                  {/* The one gateway: pull this group's rules into the table below
                      (materialised SWITCHED-OFF if not there yet — nothing routes
                      until you enable them in the table), then filter to them. */}
                  <Button
                    variant="primary"
                    disabled={busy}
                    onClick={() => {
                      if (st.missing.length > 0)
                        apply.mutate({ p, outbound: out, enabled: false });
                      onViewRules(p.key);
                    }}
                  >
                    {t("routes.presetViewRules")}
                  </Button>
                  {st.presentCount > 0 && (
                    <Button
                      variant="danger"
                      disabled={busy}
                      onClick={() => remove.mutate(st.applied.map((r) => r.id))}
                    >
                      {t("routes.presetRemoveGroup")}
                    </Button>
                  )}
                </div>
              );
            })}
          </div>
          {err && <div className="mt-2"><ErrorText>{err}</ErrorText></div>}
        </div>
      )}
    </div>
  );
}

export default function RoutesPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [searchParams] = useSearchParams();
  const presetParam = searchParams.get("preset");
  const { data: rules = [] } = useQuery({
    queryKey: ["routes"],
    queryFn: () => call<RouteRule[]>(api.get("/routes")),
  });
  // WARP enabled state — a rule pointing at the "warp" outbound is a no-op until
  // WARP is turned on (render.go skips routes to inactive outbounds), so warn.
  const { data: warp } = useQuery({
    queryKey: ["warp"],
    queryFn: () => call<{ enabled: boolean }>(api.get("/warp")),
  });
  const warpRulesDangling = useMemo(
    () =>
      warp && !warp.enabled
        ? rules.filter((r) => r.outbound === "warp" && r.enabled).length
        : 0,
    [warp, rules],
  );

  const [editing, setEditing] = useState<RouteRule | null>(null);
  const [creating, setCreating] = useState(false);
  const [capturing, setCapturing] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<RouteRule | null>(null);
  const [pendingBulkDelete, setPendingBulkDelete] = useState(false);
  const [bulkErr, setBulkErr] = useState("");

  // Local order mirror so drag works without round-tripping every move.
  const [order, setOrder] = useState<RouteRule[]>([]);
  useEffect(() => {
    setOrder(rules);
  }, [rules]);

  // Filters.
  const [search, setSearch] = useState("");
  const [fSource, setFSource] = useState("all");
  const [fOutbound, setFOutbound] = useState("all");
  const [fState, setFState] = useState("all");

  // Selection (by rule id).
  const [selected, setSelected] = useState<Set<number>>(new Set());

  // Drag state — only meaningful when no filter is active (reorder needs the
  // full ordered list; a filtered view would map indices ambiguously).
  const [dragIdx, setDragIdx] = useState<number | null>(null);

  const filterActive =
    search.trim() !== "" ||
    fSource !== "all" ||
    fOutbound !== "all" ||
    fState !== "all";

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    return order.filter((r) => {
      if (fSource !== "all" && r.source !== fSource) return false;
      if (fOutbound !== "all" && r.outbound !== fOutbound) return false;
      if (fState === "on" && !r.enabled) return false;
      if (fState === "off" && r.enabled) return false;
      if (q && !(`${r.type} ${r.value} ${r.outbound}`.toLowerCase().includes(q)))
        return false;
      return true;
    });
  }, [order, search, fSource, fOutbound, fState]);

  // Summary-chip counts respect the OTHER active filters (source / state /
  // search) so a chip's number always equals what the table shows when you
  // click it — fixes "chip says 12 but the table is empty" caused by a second,
  // forgotten filter (e.g. a leftover source filter from "view rules").
  const sQ = search.trim().toLowerCase();
  const sMatch = (r: RouteRule) =>
    !sQ || `${r.type} ${r.value} ${r.outbound}`.toLowerCase().includes(sQ);
  const outCount = (ob: string) =>
    order.filter(
      (r) =>
        r.outbound === ob &&
        (fSource === "all" || r.source === fSource) &&
        (fState === "all" || (fState === "on" ? r.enabled : !r.enabled)) &&
        sMatch(r),
    ).length;
  const offCount = order.filter(
    (r) =>
      !r.enabled &&
      (fSource === "all" || r.source === fSource) &&
      (fOutbound === "all" || r.outbound === fOutbound) &&
      sMatch(r),
  ).length;

  // Prune selection to ids that still exist after refetch.
  useEffect(() => {
    setSelected((prev) => {
      const live = new Set(order.map((r) => r.id));
      const next = new Set<number>();
      prev.forEach((id) => live.has(id) && next.add(id));
      return next.size === prev.size ? prev : next;
    });
  }, [order]);

  // A bulk message ("all selected are preset rules", "skipped N", an API error)
  // is about the selection that produced it — clear it the moment the selection
  // changes so a stale notice can't linger after deselecting or picking others.
  useEffect(() => {
    setBulkErr("");
  }, [selected]);

  const reorder = useMutation({
    mutationFn: (ids: number[]) => call(api.post("/routes/reorder", { ids })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["routes"] }),
  });

  const del = useMutation({
    mutationFn: (id: number) => call(api.delete(`/routes/${id}`)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["routes"] }),
  });

  const bulk = useMutation({
    mutationFn: (vars: { action: BulkAction; ids: number[] }) =>
      call(api.post("/routes/bulk", vars)),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["routes"] });
      setSelected(new Set());
    },
    onError: (e: any) =>
      setBulkErr(e?.response?.data?.error?.message ?? e.message),
  });

  function runBulk(action: BulkAction) {
    setBulkErr("");
    let ids = [...selected];
    if (action === "delete") {
      // Preset rules are protected from deletion — drop them from a bulk delete
      // (they can still be bulk enabled/disabled). Remove a whole preset via the
      // preset card's "remove group".
      const byId = new Map(order.map((r) => [r.id, r]));
      const before = ids.length;
      ids = ids.filter((id) => !isPresetSource(byId.get(id)?.source ?? ""));
      if (ids.length === 0) {
        setBulkErr(t("routes.bulkAllPreset"));
        return;
      }
      if (ids.length < before) setBulkErr(t("routes.bulkSomePreset", { n: before - ids.length }));
    }
    bulk.mutate({ action, ids });
  }

  // Per-row quick on/off — same endpoint as bulk, but for one rule and without
  // touching the multi-select. Lets the operator flip a rule from the table and
  // see the preset card's applied/enabled counts update immediately.
  const toggleRule = useMutation({
    mutationFn: (r: RouteRule) =>
      call(api.post("/routes/bulk", {
        action: r.enabled ? "disable" : "enable",
        ids: [r.id],
      })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["routes"] }),
  });

  // Drag-and-drop reorder over the full list (drag is disabled while filtered).
  function onDrop(to: number) {
    if (dragIdx === null || dragIdx === to) {
      setDragIdx(null);
      return;
    }
    const next = [...order];
    const [it] = next.splice(dragIdx, 1);
    next.splice(to, 0, it);
    setOrder(next);
    setDragIdx(null);
    reorder.mutate(next.map((r) => r.id));
  }

  const filteredIds = filtered.map((r) => r.id);
  const allSelected =
    filteredIds.length > 0 && filteredIds.every((id) => selected.has(id));
  function toggleSelectAll() {
    setSelected((prev) => {
      const next = new Set(prev);
      if (allSelected) filteredIds.forEach((id) => next.delete(id));
      else filteredIds.forEach((id) => next.add(id));
      return next;
    });
  }
  function toggleOne(id: number) {
    setSelected((prev) => {
      const next = new Set(prev);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });
  }

  return (
    <>
      <PageHeader
        title={t("routes.pageTitle")}
        subtitle={t("routes.pageSubtitle")}
        action={
          <div className="flex items-center gap-2">
            <Link
              to="/outbound?tab=warp"
              className="inline-flex items-center rounded-lg border border-white/10 bg-white/5 px-3 py-1.5 text-sm text-white/80 hover:bg-white/10 hover:text-white transition"
            >
              {t("routes.warpLink")}
            </Link>
            <Button onClick={() => setCapturing(true)}>
              {t("routes.captureBtn")}
            </Button>
            <Button variant="primary" onClick={() => setCreating(true)}>
              {t("routes.newRule")}
            </Button>
          </div>
        }
      />

      {warpRulesDangling > 0 && (
        <div className="mb-3 flex flex-wrap items-center gap-2 rounded-xl border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-300">
          <span className="grow">
            {t("routes.warpDisabledWarn", { count: warpRulesDangling })}
          </span>
          <Link
            to="/outbound?tab=warp"
            className="inline-flex items-center rounded-lg border border-amber-400/40 bg-amber-400/10 px-3 py-1 text-xs text-amber-300 hover:bg-amber-400/20 transition whitespace-nowrap"
          >
            {t("routes.warpDisabledLink")}
          </Link>
        </div>
      )}

      <RoutePresetsCard
        rules={order}
        onApplied={() => qc.invalidateQueries({ queryKey: ["routes"] })}
        onViewRules={(s) => {
          setFSource(s);
          setSearch("");
          setFOutbound("all");
          setFState("all");
        }}
        initialPreset={presetParam}
      />

      {/* Summary bar: live counts, click a segment to filter the table. */}
      {order.length > 0 && (
        <div className="mb-3 flex flex-wrap items-center gap-2 text-xs">
          <span className="text-white/50">
            {t("routes.summaryTotal", { count: order.length })}
          </span>
          {["warp", "block", "direct"].map((ob) => (
            <button
              key={ob}
              type="button"
              onClick={() => setFOutbound((cur) => (cur === ob ? "all" : ob))}
              className={
                "rounded-full border px-2.5 py-0.5 transition " +
                (fOutbound === ob
                  ? "border-emerald-400/60 bg-emerald-400/10 text-emerald-200"
                  : "border-white/10 bg-white/5 text-white/60 hover:text-white/90")
              }
            >
              {ob} {outCount(ob)}
            </button>
          ))}
          <button
            type="button"
            onClick={() => setFState((cur) => (cur === "off" ? "all" : "off"))}
            className={
              "rounded-full border px-2.5 py-0.5 transition " +
              (fState === "off"
                ? "border-amber-400/60 bg-amber-400/10 text-amber-200"
                : "border-white/10 bg-white/5 text-white/60 hover:text-white/90")
            }
          >
            {t("routes.summaryOff", { count: offCount })}
          </button>
          {(fOutbound !== "all" || fState !== "all" || fSource !== "all" || search.trim() !== "") && (
            <button
              type="button"
              onClick={() => {
                setFOutbound("all");
                setFState("all");
                setFSource("all");
                setSearch("");
              }}
              className="rounded-full border border-white/10 bg-white/5 px-2.5 py-0.5 text-white/60 hover:text-white/90"
            >
              {t("routes.summaryClearFilter")}
            </button>
          )}
        </div>
      )}

      {/* Filter bar */}
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="grow min-w-[180px]">
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("routes.searchPlaceholder")}
          />
        </div>
        <div className="w-36">
          <Select value={fSource} onChange={(e) => setFSource(e.target.value)}>
            <option value="all">{t("routes.filterSourceAll")}</option>
            {SOURCES.map((s) => (
              <option key={s} value={s}>
                {t(`routes.source_${s}`)}
              </option>
            ))}
          </Select>
        </div>
        <div className="w-32">
          <Select
            value={fOutbound}
            onChange={(e) => setFOutbound(e.target.value)}
          >
            <option value="all">{t("routes.filterOutboundAll")}</option>
            {OUTBOUNDS.map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </Select>
        </div>
        <div className="w-28">
          <Select value={fState} onChange={(e) => setFState(e.target.value)}>
            <option value="all">{t("routes.filterStateAll")}</option>
            <option value="on">{t("routes.stateOn")}</option>
            <option value="off">{t("routes.stateOff")}</option>
          </Select>
        </div>
      </div>

      {/* Bulk toolbar */}
      {selected.size > 0 && (
        <div className="mb-3 flex flex-wrap items-center gap-2 rounded-xl border border-white/10 bg-white/[0.04] px-3 py-2">
          <span className="text-sm text-white/70">
            {t("routes.bulkSelected", { count: selected.size })}
          </span>
          <div className="grow" />
          <Button disabled={bulk.isPending} onClick={() => runBulk("enable")}>
            {t("routes.bulkEnable")}
          </Button>
          <Button disabled={bulk.isPending} onClick={() => runBulk("disable")}>
            {t("routes.bulkDisable")}
          </Button>
          <Button
            variant="danger"
            disabled={bulk.isPending}
            onClick={() => setPendingBulkDelete(true)}
          >
            {t("routes.bulkDelete")}
          </Button>
          <Button onClick={() => setExporting(true)}>
            {t("routes.exportBtn")}
          </Button>
          <Button variant="ghost" onClick={() => setSelected(new Set())}>
            {t("routes.bulkClear")}
          </Button>
          {bulk.isPending && (
            <span className="text-xs text-white/50">{t("routes.bulkBusy")}</span>
          )}
        </div>
      )}
      <ErrorText>{bulkErr}</ErrorText>

      {filterActive && (
        <p className="mb-2 text-xs text-white/40">{t("routes.dragDisabledHint")}</p>
      )}

      <div className="rounded-2xl border border-white/10 bg-white/[0.03] overflow-hidden">
        <div className="max-h-[28rem] overflow-y-auto">
        <table className="w-full text-sm">
          <thead className="bg-white/5 text-white/50 text-xs uppercase">
            <tr>
              <th className="px-3 py-2 text-left w-8">
                <input
                  type="checkbox"
                  className="accent-emerald-500"
                  checked={allSelected}
                  onChange={toggleSelectAll}
                  aria-label={t("routes.selectAll")}
                />
              </th>
              <th className="px-2 py-2 text-left w-6"></th>
              <th className="px-3 py-2 text-left w-24">{t("routes.tableSource")}</th>
              <th className="px-3 py-2 text-left">{t("routes.tableType")}</th>
              <th className="px-3 py-2 text-left">{t("routes.tableValue")}</th>
              <th className="px-3 py-2 text-left">{t("routes.tableOutbound")}</th>
              <th className="px-3 py-2 text-left w-20">{t("routes.tableState")}</th>
              <th className="px-3 py-2 text-right w-32">{t("routes.tableActions")}</th>
            </tr>
          </thead>
          <tbody>
            {filtered.length === 0 && (
              <tr>
                <td
                  colSpan={8}
                  className="px-3 py-8 text-center text-white/40 text-sm"
                >
                  {order.length === 0
                    ? t("routes.emptyState")
                    : t("routes.noMatch")}
                </td>
              </tr>
            )}
            {filtered.map((r) => {
              const idx = order.findIndex((x) => x.id === r.id);
              return (
                <tr
                  key={r.id}
                  draggable={!filterActive}
                  onDragStart={() => !filterActive && setDragIdx(idx)}
                  onDragOver={(e) => !filterActive && e.preventDefault()}
                  onDrop={() => !filterActive && onDrop(idx)}
                  className={`border-t border-white/5 hover:bg-white/[0.02] ${
                    dragIdx === idx ? "opacity-40" : ""
                  } ${selected.has(r.id) ? "bg-emerald-500/[0.06]" : ""}`}
                >
                  <td className="px-3 py-2">
                    <input
                      type="checkbox"
                      className="accent-emerald-500"
                      checked={selected.has(r.id)}
                      onChange={() => toggleOne(r.id)}
                    />
                  </td>
                  <td className="px-2 py-2">
                    <span
                      className={`select-none ${
                        filterActive
                          ? "text-white/15"
                          : "text-white/30 cursor-grab"
                      }`}
                      title={
                        filterActive
                          ? t("routes.dragDisabledHint")
                          : t("routes.dragHandle")
                      }
                    >
                      ⠿
                    </span>
                  </td>
                  <td className="px-3 py-2">
                    <Badge tone={sourceTone(r.source)}>
                      {t(`routes.source_${r.source}`, { defaultValue: r.source })}
                    </Badge>
                  </td>
                  <td className="px-3 py-2 font-mono text-xs">{r.type}</td>
                  <td className="px-3 py-2 font-mono text-xs break-all">
                    {r.value}
                  </td>
                  <td className="px-3 py-2">
                    <code className="text-emerald-300">{r.outbound}</code>
                  </td>
                  <td className="px-3 py-2">
                    <button
                      type="button"
                      disabled={toggleRule.isPending}
                      onClick={() => toggleRule.mutate(r)}
                      title={t("routes.toggleHint")}
                      className="cursor-pointer"
                    >
                      {r.enabled ? (
                        <Badge tone="success" dot solid>{t("routes.stateOn")}</Badge>
                      ) : (
                        <Badge tone="neutral" dot>{t("routes.stateOff")}</Badge>
                      )}
                    </button>
                  </td>
                  <td className="px-3 py-2 text-right">
                    <div className="inline-flex gap-1">
                      <Button onClick={() => setEditing(r)}>
                        {t("routes.btnEdit")}
                      </Button>
                      {/* Preset rules can't be deleted one-by-one (that would
                          desync the group) — disable them in place, or use the
                          preset card's "remove group". Only custom/captured
                          rules get a delete button. */}
                      {isPresetSource(r.source) ? (
                        <span
                          className="inline-flex items-center px-1 text-xs text-white/25"
                          title={t("routes.presetNoDeleteHint")}
                        >
                          🔒
                        </span>
                      ) : (
                        <Button variant="danger" onClick={() => setPendingDelete(r)}>
                          {t("routes.btnDelete")}
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
        </div>
      </div>

      <CaptureModal
        open={capturing}
        onClose={() => setCapturing(false)}
        onApplied={() => {
          qc.invalidateQueries({ queryKey: ["routes"] });
          setCapturing(false);
        }}
      />

      <Modal
        open={exporting}
        onClose={() => setExporting(false)}
        title={t("routes.exportModalTitle")}
        footer={
          <Button variant="ghost" onClick={() => setExporting(false)}>
            {t("routes.btnCancel")}
          </Button>
        }
      >
        <div className="space-y-3">
          <p className="text-xs text-white/50">{t("routes.exportModalHint")}</p>
          <RuleExport
            collapsible={false}
            rules={order
              .filter((r) => selected.has(r.id))
              .map((r) => ({
                type: r.type,
                value: r.value,
                outbound: r.outbound,
              }))}
          />
        </div>
      </Modal>

      <RuleModal
        open={creating || !!editing}
        rule={editing}
        onClose={() => {
          setCreating(false);
          setEditing(null);
        }}
        onSaved={() => {
          qc.invalidateQueries({ queryKey: ["routes"] });
          setCreating(false);
          setEditing(null);
        }}
        currentCount={order.length}
        existingSources={Array.from(
          new Set(order.map((r) => r.source).filter(Boolean)),
        )}
      />

      <ConfirmDialog
        open={pendingDelete !== null}
        title={t("routes.btnDelete")}
        body={
          pendingDelete ? t("routes.confirmDelete", { id: pendingDelete.id }) : ""
        }
        confirmLabel={t("common.confirm")}
        cancelLabel={t("common.cancel")}
        variant="danger"
        onCancel={() => setPendingDelete(null)}
        onConfirm={() => {
          const r = pendingDelete;
          setPendingDelete(null);
          if (r) del.mutate(r.id);
        }}
      />

      <ConfirmDialog
        open={pendingBulkDelete}
        title={t("routes.bulkDelete")}
        body={t("routes.confirmBulkDelete", { count: selected.size })}
        confirmLabel={t("common.confirm")}
        cancelLabel={t("common.cancel")}
        variant="danger"
        onCancel={() => setPendingBulkDelete(false)}
        onConfirm={() => {
          setPendingBulkDelete(false);
          runBulk("delete");
        }}
      />
    </>
  );
}

function RuleModal({
  open,
  rule,
  onClose,
  onSaved,
  currentCount,
  existingSources,
}: {
  open: boolean;
  rule: RouteRule | null;
  onClose: () => void;
  onSaved: () => void;
  currentCount: number;
  existingSources: string[];
}) {
  const { t } = useTranslation();
  const mode: "create" | "edit" = rule ? "edit" : "create";
  // Preset rules are half-locked: the domain/type/source come from the curated
  // catalogue and shouldn't be hand-edited (that would silently desync the
  // preset's applied count). Only the outbound and on/off are the operator's.
  const locked = mode === "edit" && isPresetSource(rule?.source ?? "");
  const [type, setType] = useState(rule?.type ?? "domain_suffix");
  const [value, setValue] = useState(rule?.value ?? "");
  const [outbound, setOutbound] = useState(rule?.outbound ?? "direct");
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);
  // "" = the default, ungrouped bucket (stored as "custom" on save). We never
  // surface "custom" itself as a pickable tag — it's the absence of a tag.
  const [source, setSource] = useState(
    rule?.source && rule.source !== "custom" ? rule.source : "",
  );
  // customMode = the operator picked "new tag" and is typing a free-form one.
  const [customMode, setCustomMode] = useState(false);
  const [err, setErr] = useState("");

  // Tags you can pick: preset categories + any tag already in use — minus the
  // two SYSTEM tags. "custom" is the implicit default (no tag), and "captured"
  // is auto-applied by domain capture, so neither is something you choose here.
  const SYSTEM_SOURCES = ["custom", "captured"];
  const pickableSources = Array.from(
    new Set([...SOURCES, ...existingSources]),
  ).filter((s) => !SYSTEM_SOURCES.includes(s));
  // Keep an already-set system/legacy tag (e.g. editing a captured rule) shown
  // so editing doesn't silently drop it.
  const editingTag =
    rule?.source && rule.source !== "custom" && !pickableSources.includes(rule.source)
      ? rule.source
      : null;

  useEffect(() => {
    if (open) {
      setType(rule?.type ?? "domain_suffix");
      setValue(rule?.value ?? "");
      setOutbound(rule?.outbound ?? "direct");
      setEnabled(rule?.enabled ?? true);
      setSource(rule?.source && rule.source !== "custom" ? rule.source : "");
      setCustomMode(false);
      setErr("");
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, rule]);

  const sourceLabel = (s: string) =>
    SOURCES.includes(s) ? t(`routes.source_${s}`) : s;

  const m = useMutation({
    mutationFn: () => {
      const body = {
        type,
        value,
        outbound,
        enabled,
        source: source.trim() || "custom",
        order: rule?.order ?? currentCount,
      };
      if (mode === "edit" && rule) {
        return call(api.put(`/routes/${rule.id}`, body));
      }
      return call(api.post("/routes", body));
    },
    onSuccess: onSaved,
    onError: (e: any) => setErr(e?.response?.data?.error?.message ?? e.message),
  });

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={
        mode === "edit"
          ? t("routes.modalEditTitle", { id: rule?.id })
          : t("routes.modalCreateTitle")
      }
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("routes.btnCancel")}
          </Button>
          <Button
            variant="primary"
            disabled={m.isPending}
            onClick={() => {
              setErr("");
              m.mutate();
            }}
          >
            {m.isPending ? t("routes.btnSaving") : t("routes.btnSave")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {locked && (
          <div className="rounded-md border border-sky-500/30 bg-sky-500/10 px-3 py-2 text-xs text-sky-200">
            {t("routes.presetLockedHint")}
          </div>
        )}
        <Field label={t("routes.fieldMatchType")}>
          <Select
            value={type}
            disabled={locked}
            onChange={(e) => setType(e.target.value)}
          >
            {TYPES.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("routes.fieldValue")} hint={hintFor(type, t)}>
          <Input value={value} disabled={locked} onChange={(e) => setValue(e.target.value)} />
        </Field>
        <Field
          label={t("routes.fieldOutbound")}
          hint={t("routes.fieldOutboundHint")}
        >
          <Select value={outbound} onChange={(e) => setOutbound(e.target.value)}>
            {OUTBOUNDS.map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("routes.fieldSource")} hint={t("routes.fieldSourceHint")}>
          <Select
            value={customMode ? "__new__" : source}
            disabled={locked}
            onChange={(e) => {
              const v = e.target.value;
              if (v === "__new__") {
                setCustomMode(true);
                setSource("");
              } else {
                setCustomMode(false);
                setSource(v);
              }
            }}
          >
            <option value="">{t("routes.sourceDefault")}</option>
            {pickableSources.map((s) => (
              <option key={s} value={s}>
                {sourceLabel(s)}
              </option>
            ))}
            {editingTag && (
              <option value={editingTag}>{sourceLabel(editingTag)}</option>
            )}
            <option value="__new__">{t("routes.sourceNew")}</option>
          </Select>
          {customMode && (
            <Input
              className="mt-2"
              value={source}
              onChange={(e) => setSource(e.target.value)}
              placeholder={t("routes.sourceNewPlaceholder")}
              autoFocus
            />
          )}
        </Field>
        <Toggle
          checked={enabled}
          onChange={setEnabled}
          label={t("routes.fieldEnabled")}
        />
        <ErrorText>{err}</ErrorText>
      </div>
    </Modal>
  );
}

interface CaptureGroup {
  registrable: string;
  hosts: string[];
  count: number;
  noise: boolean;
  bytes: number;
}

// fmtBytes renders a traffic figure compactly (B/KB/MB/GB).
function fmtBytes(n: number): string {
  if (!n) return "";
  const u = ["B", "KB", "MB", "GB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${u[i]}`;
}

// DomainChecklist renders captured domains in two buckets: service-relevant
// (shown, with a select-all) and background/telemetry noise (collapsed, opt-in).
// Shared by both capture modes so live capture's heavy device noise — and the
// URL mode's analytics chatter — fold away without losing anything.
function DomainChecklist({
  domains,
  selected,
  setSelected,
}: {
  domains: CaptureGroup[];
  selected: Set<string>;
  setSelected: (updater: (prev: Set<string>) => Set<string>) => void;
}) {
  const { t } = useTranslation();
  const [showNoise, setShowNoise] = useState(false);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [q, setQ] = useState("");
  const match = (d: CaptureGroup) => {
    const needle = q.trim().toLowerCase();
    if (!needle) return true;
    return (
      d.registrable.includes(needle) ||
      d.hosts.some((h) => h.toLowerCase().includes(needle))
    );
  };
  const relevant = domains.filter((d) => !d.noise && match(d));
  const noise = domains.filter((d) => d.noise && match(d));

  const toggle = (reg: string) =>
    setSelected((prev) => {
      const n = new Set(prev);
      n.has(reg) ? n.delete(reg) : n.add(reg);
      return n;
    });
  const relevantRegs = relevant.map((d) => d.registrable);
  const allRelevantSel =
    relevantRegs.length > 0 && relevantRegs.every((r) => selected.has(r));
  const toggleAllRelevant = () =>
    setSelected((prev) => {
      const n = new Set(prev);
      if (allRelevantSel) relevantRegs.forEach((r) => n.delete(r));
      else relevantRegs.forEach((r) => n.add(r));
      return n;
    });
  const toggleExpand = (reg: string) =>
    setExpanded((prev) => {
      const n = new Set(prev);
      n.has(reg) ? n.delete(reg) : n.add(reg);
      return n;
    });

  const row = (d: CaptureGroup) => (
    <div key={d.registrable} className="px-3 py-2">
      <div className="flex items-center gap-2">
        <input
          type="checkbox"
          className="accent-emerald-500"
          checked={selected.has(d.registrable)}
          onChange={() => toggle(d.registrable)}
        />
        <span className="font-mono text-sm break-all">{d.registrable}</span>
        {d.bytes > 0 && (
          <span
            className="shrink-0 rounded bg-emerald-500/15 px-1.5 text-[10px] text-emerald-300"
            title={t("routes.liveTrafficHint")}
          >
            {fmtBytes(d.bytes)}
          </span>
        )}
        {d.count >= 1 && (
          <button
            type="button"
            className="text-xs text-white/40 hover:text-white/70 shrink-0"
            onClick={() => toggleExpand(d.registrable)}
          >
            {t("routes.captureHosts", { count: d.count })}
            {expanded.has(d.registrable) ? " ▲" : " ▼"}
          </button>
        )}
      </div>
      {expanded.has(d.registrable) && (
        <ul className="mt-1 ml-6 space-y-0.5">
          {d.hosts.map((h) => (
            <li key={h} className="font-mono text-xs text-white/40 break-all">
              {h}
            </li>
          ))}
        </ul>
      )}
    </div>
  );

  return (
    <div className="space-y-2">
      {domains.length > 6 && (
        <Input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder={t("routes.captureSearch")}
        />
      )}
      {relevant.length > 0 && (
        <label className="flex items-center gap-2 text-xs text-white/70">
          <input
            type="checkbox"
            className="accent-emerald-500"
            checked={allRelevantSel}
            onChange={toggleAllRelevant}
          />
          {t("routes.captureSelectRelevant")}
        </label>
      )}
      <div className="max-h-56 overflow-y-auto rounded-xl border border-white/10 divide-y divide-white/5">
        {relevant.map(row)}
        {relevant.length === 0 && (
          <div className="px-3 py-3 text-center text-xs text-white/40">
            {t("routes.captureNoRelevant")}
          </div>
        )}
      </div>
      {noise.length > 0 && (
        <div>
          <button
            type="button"
            className="text-xs text-white/40 hover:text-white/70"
            onClick={() => setShowNoise((s) => !s)}
          >
            {showNoise ? "▼ " : "▶ "}
            {t("routes.captureBackground", { count: noise.length })}
          </button>
          {showNoise && (
            <div className="mt-1 max-h-40 overflow-y-auto rounded-xl border border-white/10 divide-y divide-white/5">
              {noise.map(row)}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
// ── Export captured / existing rules as other clients' routing-rule syntax ──
// Client configs route by "which proxy node", not EdgeNest's internal outbound,
// so we map: direct → DIRECT, block → REJECT, everything else (warp/proxy) → the
// user's proxy/policy name. Unsupported (type, format) pairs become comments so
// nothing is silently dropped.
interface ExpRule {
  type: string;
  value: string;
  outbound: string;
}

const EXPORT_FORMATS = [
  { v: "surge", l: "Surge / Shadowrocket" },
  { v: "clash", l: "Clash / Mihomo" },
  { v: "qx", l: "Quantumult X" },
  { v: "singbox", l: "sing-box" },
  { v: "plain", l: "plain" },
];

// EdgeNest rule type → each format's token (null = unsupported in that format).
const TYPE_TOKEN: Record<string, Record<string, string | null>> = {
  surge: {
    domain: "DOMAIN", domain_suffix: "DOMAIN-SUFFIX", domain_keyword: "DOMAIN-KEYWORD",
    domain_regex: null, ip_cidr: "IP-CIDR", geoip: "GEOIP", geosite: null, process_name: "PROCESS-NAME",
  },
  clash: {
    domain: "DOMAIN", domain_suffix: "DOMAIN-SUFFIX", domain_keyword: "DOMAIN-KEYWORD",
    domain_regex: "DOMAIN-REGEX", ip_cidr: "IP-CIDR", geoip: "GEOIP", geosite: "GEOSITE", process_name: "PROCESS-NAME",
  },
  qx: {
    domain: "host", domain_suffix: "host-suffix", domain_keyword: "host-keyword",
    domain_regex: "host-regex", ip_cidr: "ip-cidr", geoip: null, geosite: null, process_name: null,
  },
};

function exportPolicy(fmt: string, outbound: string, proxy: string): string {
  const direct = outbound === "direct" || outbound === "direct-v4" || outbound === "direct-v6";
  const block = outbound === "block";
  if (fmt === "qx") return direct ? "direct" : block ? "reject" : proxy;
  return direct ? "DIRECT" : block ? "REJECT" : proxy;
}

function renderClientRules(rules: ExpRule[], fmt: string, proxy: string): string {
  const p = proxy.trim() || "PROXY";
  if (fmt === "plain") {
    // Domain-ish values only — IP/geo rules have no place in a plain host list.
    return rules
      .filter((r) => r.type.startsWith("domain"))
      .map((r) => r.value)
      .join("\n");
  }
  if (fmt === "singbox") {
    // Group by policy then by type into sing-box route rules.
    const byPolicy: Record<string, Record<string, string[]>> = {};
    for (const r of rules) {
      const pol = r.outbound === "direct" ? "direct" : r.outbound === "block" ? "block" : p;
      (byPolicy[pol] ??= {});
      (byPolicy[pol][r.type] ??= []).push(r.value);
    }
    const out = Object.entries(byPolicy).flatMap(([pol, types]) =>
      Object.entries(types).map(([typ, vals]) => ({ [typ]: vals, outbound: pol })),
    );
    return JSON.stringify({ route: { rules: out } }, null, 2);
  }
  const tokens = TYPE_TOKEN[fmt] ?? TYPE_TOKEN.surge;
  return rules
    .map((r) => {
      const tok = tokens[r.type];
      const pol = exportPolicy(fmt, r.outbound, p);
      if (!tok) return `# unsupported in ${fmt}: ${r.type},${r.value}`;
      if (fmt === "clash") return `  - ${tok},${r.value},${pol}`;
      if (fmt === "qx") return `${tok}, ${r.value}, ${pol}`;
      return `${tok},${r.value},${pol}`; // surge / shadowrocket
    })
    .join("\n");
}

// RuleExport renders the given rules in a chosen client's syntax with a proxy
// policy name the operator sets (their node/group), plus copy.
function RuleExport({
  rules,
  collapsible = true,
}: {
  rules: ExpRule[];
  collapsible?: boolean;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(!collapsible);
  const [fmt, setFmt] = useState("surge");
  const [proxy, setProxy] = useState("PROXY");
  const [copied, setCopied] = useState(false);
  const text = renderClientRules(rules, fmt, proxy);

  const body = (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <div className="w-48">
          <Select value={fmt} onChange={(e) => setFmt(e.target.value)}>
            {EXPORT_FORMATS.map((f) => (
              <option key={f.v} value={f.v}>
                {f.v === "plain" ? t("routes.exportPlain") : f.l}
              </option>
            ))}
          </Select>
        </div>
        {fmt !== "plain" && (
          <div className="w-40">
            <Input
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
              placeholder={t("routes.exportPolicy")}
            />
          </div>
        )}
      </div>
      {fmt !== "plain" && (
        <p className="text-[11px] text-white/40">{t("routes.exportPolicyHint")}</p>
      )}
      <textarea
        readOnly
        value={text}
        className="w-full h-44 rounded-lg border border-white/10 bg-black/30 p-2 font-mono text-[11px] text-white/70"
      />
      <Button
        onClick={() => {
          navigator.clipboard?.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }}
      >
        {copied ? t("routes.exportCopied") : t("routes.exportCopy")}
      </Button>
    </div>
  );

  if (!collapsible) return body;
  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="text-xs text-white/40 hover:text-white/70"
      >
        {open ? "▼ " : "▶ "}
        {t("routes.exportTitle", { count: rules.length })}
      </button>
      {open && <div className="mt-2">{body}</div>}
    </div>
  );
}

interface CaptureResult {
  engine: "static";
  domains: CaptureGroup[];
}


// CaptureModal: type a website, capture every domain it touches, then turn the
// chosen ones into routing rules in one shot. Each registrable domain is one
// selectable row (route the whole service); its individual hosts show on expand.
function CaptureModal({
  open,
  onClose,
  onApplied,
}: {
  open: boolean;
  onClose: () => void;
  onApplied: () => void;
}) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<"url" | "live">("url");
  const [url, setUrl] = useState("");
  const [result, setResult] = useState<CaptureResult | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [outbound, setOutbound] = useState("warp");
  const [showExport, setShowExport] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    if (open) {
      setMode("url");
      setUrl("");
      setResult(null);
      setSelected(new Set());
      setOutbound("warp");
      setShowExport(false);
      setErr("");
    }
  }, [open]);

  const capture = useMutation({
    mutationFn: () => call<CaptureResult>(api.post("/routes/capture", { url })),
    onSuccess: (res) => {
      setResult(res);
      // Pre-select the service-relevant domains; background/telemetry noise is
      // left unchecked (and folded away) so the operator routes the right set.
      setSelected(
        new Set(res.domains.filter((d) => !d.noise).map((d) => d.registrable)),
      );
    },
    // Raw "invalid url: parse ..." Go errors mean nothing to a user — show a
    // friendly hint covering both bad input and an unreachable target.
    onError: () => setErr(t("routes.captureFailedHint")),
  });

  const apply = useMutation({
    mutationFn: () =>
      call<{ added: number; skipped: number }>(
        api.post("/routes/capture/apply", {
          domains: [...selected],
          outbound,
        }),
      ),
    onSuccess: onApplied,
    onError: (e: any) =>
      setErr(e?.response?.data?.error?.message ?? e.message),
  });

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t("routes.captureTitle")}
      footer={
        <Button variant="ghost" onClick={onClose}>
          {t("routes.btnCancel")}
        </Button>
      }
    >
      <div className="space-y-4">
        {/* Mode tabs: capture by URL (headless visit) vs live (real traffic). */}
        <div className="flex gap-1 rounded-lg border border-white/10 p-1 text-xs">
          {(["url", "live"] as const).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => {
                setErr("");
                setMode(m);
              }}
              className={`flex-1 rounded-md px-3 py-1.5 transition ${
                mode === m
                  ? "bg-white/10 text-white"
                  : "text-white/50 hover:text-white/80"
              }`}
            >
              {t(m === "url" ? "routes.captureModeUrl" : "routes.captureModeLive")}
            </button>
          ))}
        </div>

        {mode === "live" && <LiveCapturePanel onApplied={onApplied} />}

        {mode === "url" && (
        <>
        <p className="text-xs text-white/50">{t("routes.captureIntro")}</p>
        <div className="flex items-end gap-2">
          <div className="grow">
            <Field label={t("routes.captureUrlLabel")}>
              <Input
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder={t("routes.captureUrlPlaceholder")}
              />
            </Field>
          </div>
          <Button
            disabled={capture.isPending || !url.trim()}
            onClick={() => {
              setErr("");
              capture.mutate();
            }}
          >
            {capture.isPending ? t("routes.captureRunning") : t("routes.captureStart")}
          </Button>
        </div>

        {capture.isPending && (
          <p className="text-xs text-white/50">{t("routes.captureWait")}</p>
        )}

        {result && (
          <>
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <span className="text-white/50">
                {t("routes.captureFound", { count: result.domains.length })}
              </span>
            </div>

            <p className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
              {t("routes.captureStaticWarn")}
            </p>

            {result.domains.length > 0 && result.domains.length <= 6 && (
              <p className="rounded-lg border border-white/10 bg-white/[0.04] px-3 py-2 text-xs text-white/60">
                {t("routes.captureFewHint")}
              </p>
            )}

            {result.domains.length === 0 ? (
              <p className="text-sm text-white/40">{t("routes.captureEmpty")}</p>
            ) : (
              <>
                <div className="flex items-center justify-end">
                  <div className="w-32">
                    <Select
                      value={outbound}
                      onChange={(e) => setOutbound(e.target.value)}
                    >
                      {OUTBOUNDS.map((o) => (
                        <option key={o} value={o}>
                          {o}
                        </option>
                      ))}
                    </Select>
                  </div>
                </div>
                <DomainChecklist
                  domains={result.domains}
                  selected={selected}
                  setSelected={setSelected}
                />
                {/* Build route — primary action, directly under the list. */}
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs text-white/40">
                    {t("routes.captureApplyHint")}
                  </span>
                  <Button
                    variant="primary"
                    className="whitespace-nowrap shrink-0"
                    disabled={apply.isPending || selected.size === 0}
                    onClick={() => {
                      setErr("");
                      apply.mutate();
                    }}
                  >
                    {apply.isPending
                      ? t("routes.btnSaving")
                      : t("routes.captureApply", { count: selected.size })}
                  </Button>
                </div>
                {/* Export to other clients — collapsed; click the title to open. */}
                {selected.size > 0 && (
                  <div className="rounded-xl border border-sky-500/30 bg-sky-500/[0.06] p-3">
                    <button
                      type="button"
                      onClick={() => setShowExport((o) => !o)}
                      className="flex w-full items-center justify-between text-xs font-medium text-sky-200"
                    >
                      <span>{t("routes.exportInlineTitle")}</span>
                      <span className="text-white/40">{showExport ? "▾" : "▸"}</span>
                    </button>
                    {showExport && (
                      <div className="mt-2">
                        <RuleExport
                          collapsible={false}
                          rules={[...selected].map((d) => ({
                            type: "domain_suffix",
                            value: d,
                            outbound,
                          }))}
                        />
                      </div>
                    )}
                  </div>
                )}
              </>
            )}
          </>
        )}
        </>
        )}
        {mode === "url" && <ErrorText>{err}</ErrorText>}
      </div>
    </Modal>
  );
}

// LiveCapturePanel drives the real-traffic capture: the operator uses the
// service through their own client while this polls sing-box's clash_api and
// shows the domains their device actually reached, growing live. This is the
// only path that catches login/playback-gated domains (e.g. Netflix's video
// CDN) a headless page visit can't see.
function LiveCapturePanel({ onApplied }: { onApplied: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [source, setSource] = useState(""); // "" = all devices
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [outbound, setOutbound] = useState("warp");
  const [showExport, setShowExport] = useState(false);
  const [err, setErr] = useState("");

  interface LiveStatus {
    running: boolean;
    elapsedSec: number;
    domains: CaptureGroup[];
    sources: { ip: string; count: number }[];
    pollError: string;
  }
  const { data } = useQuery({
    queryKey: ["liveCapture", source],
    queryFn: () =>
      call<LiveStatus>(
        api.get(`/routes/capture/live/status${source ? `?source=${encodeURIComponent(source)}` : ""}`),
      ),
    refetchInterval: (q) =>
      (q.state.data as LiveStatus | undefined)?.running ? 2000 : false,
  });

  const start = useMutation({
    mutationFn: () => call(api.post("/routes/capture/live/start", {})),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["liveCapture"] }),
    onError: (e: any) => setErr(e?.response?.data?.error?.message ?? e.message),
  });
  const stop = useMutation({
    mutationFn: () => call(api.post("/routes/capture/live/stop", {})),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["liveCapture"] }),
    onError: (e: any) => setErr(e?.response?.data?.error?.message ?? e.message),
  });
  const apply = useMutation({
    mutationFn: () =>
      call(
        api.post("/routes/capture/apply", { domains: [...selected], outbound }),
      ),
    // Building rules ends the session: stop polling and wipe the accumulated
    // domains + sources so reopening starts clean, then close.
    onSuccess: async () => {
      try {
        await call(api.post("/routes/capture/live/stop", {}));
        await call(api.post("/routes/capture/live/clear", {}));
      } catch {
        /* best effort */
      }
      onApplied();
    },
    onError: (e: any) => setErr(e?.response?.data?.error?.message ?? e.message),
  });
  const clear = useMutation({
    mutationFn: () => call(api.post("/routes/capture/live/clear", {})),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["liveCapture"] }),
    onError: (e: any) => setErr(e?.response?.data?.error?.message ?? e.message),
  });

  const running = data?.running ?? false;
  const domains = data?.domains ?? [];

  return (
    <div className="space-y-3">
      <p className="text-xs text-white/50">{t("routes.liveIntro")}</p>

      <div className="flex flex-wrap items-center gap-2">
        {!running ? (
          <Button disabled={start.isPending} onClick={() => { setErr(""); start.mutate(); }}>
            {t("routes.liveStart")}
          </Button>
        ) : (
          <Button variant="danger" disabled={stop.isPending} onClick={() => stop.mutate()}>
            {t("routes.liveStop")}
          </Button>
        )}
        {running && (
          <span className="inline-flex items-center gap-1 text-xs text-emerald-300">
            <span className="h-2 w-2 animate-pulse rounded-full bg-emerald-400" />
            {t("routes.liveRecording", { sec: data?.elapsedSec ?? 0 })}
          </span>
        )}
        {domains.length > 0 && (
          <Button
            variant="ghost"
            disabled={clear.isPending}
            onClick={() => clear.mutate()}
          >
            {t("routes.liveClear")}
          </Button>
        )}
      </div>

      {running && (
        <p className="rounded-lg border border-sky-500/30 bg-sky-500/10 px-3 py-2 text-xs text-sky-200">
          {t("routes.liveUseHint")}
        </p>
      )}

      {data?.pollError && (
        <p className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
          {t("routes.livePollError")}
          <span className="block font-mono text-[10px] text-white/50">{data.pollError}</span>
        </p>
      )}

      {/* Source filter — pick your device to cut other apps' background noise. */}
      {(data?.sources?.length ?? 0) > 0 && (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span className="whitespace-nowrap text-white/50">
            {t("routes.liveSource")}
          </span>
          <div className="w-44">
            <Select value={source} onChange={(e) => setSource(e.target.value)}>
              <option value="">{t("routes.liveSourceAll")}</option>
              {data!.sources.map((s) => (
                <option key={s.ip} value={s.ip}>
                  {s.ip} ({s.count})
                </option>
              ))}
            </Select>
          </div>
          <Button
            variant="ghost"
            className="whitespace-nowrap shrink-0"
            onClick={() => qc.invalidateQueries({ queryKey: ["liveCapture"] })}
          >
            {t("routes.liveRefresh")}
          </Button>
        </div>
      )}

      <div className="flex items-center justify-between text-xs text-white/50">
        <span>{t("routes.liveFound", { count: domains.length })}</span>
        <div className="w-32">
          <Select value={outbound} onChange={(e) => setOutbound(e.target.value)}>
            {OUTBOUNDS.map((o) => (
              <option key={o} value={o}>{o}</option>
            ))}
          </Select>
        </div>
      </div>

      {domains.length > 0 && (
        <DomainChecklist
          domains={domains}
          selected={selected}
          setSelected={setSelected}
        />
      )}

      {/* Build route — the primary action, kept directly under the list. */}
      {domains.length > 0 && (
        <div className="flex items-center justify-between gap-2">
          <span className="text-xs text-white/40">{t("routes.captureApplyHint")}</span>
          <Button
            variant="primary"
            className="whitespace-nowrap shrink-0"
            disabled={apply.isPending || selected.size === 0}
            onClick={() => { setErr(""); apply.mutate(); }}
          >
            {apply.isPending
              ? t("routes.btnSaving")
              : t("routes.captureApply", { count: selected.size })}
          </Button>
        </div>
      )}

      {/* Export to other clients — collapsed; click the title to open. */}
      {selected.size > 0 && (
        <div className="rounded-xl border border-sky-500/30 bg-sky-500/[0.06] p-3">
          <button
            type="button"
            onClick={() => setShowExport((o) => !o)}
            className="flex w-full items-center justify-between text-xs font-medium text-sky-200"
          >
            <span>{t("routes.exportInlineTitle")}</span>
            <span className="text-white/40">{showExport ? "▾" : "▸"}</span>
          </button>
          {showExport && (
            <div className="mt-2">
              <RuleExport
                collapsible={false}
                rules={[...selected].map((d) => ({
                  type: "domain_suffix",
                  value: d,
                  outbound,
                }))}
              />
            </div>
          )}
        </div>
      )}

      {err && <ErrorText>{err}</ErrorText>}
    </div>
  );
}

function hintFor(type: string, t: (k: string) => string): string {
  switch (type) {
    case "domain":
      return t("routes.hintDomain");
    case "domain_suffix":
      return t("routes.hintDomainSuffix");
    case "domain_keyword":
      return t("routes.hintDomainKeyword");
    case "domain_regex":
      return t("routes.hintDomainRegex");
    case "geosite":
      return t("routes.hintGeosite");
    case "geoip":
      return t("routes.hintGeoip");
    case "ip_cidr":
      return t("routes.hintIpCidr");
    case "process_name":
      return t("routes.hintProcessName");
    default:
      return "";
  }
}
