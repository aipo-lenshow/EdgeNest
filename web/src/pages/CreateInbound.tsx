// CreateInbound — sidebar item 2.
//
// Two entry points share this page: the quick mode (zero-config IP-direct
// batch of up to four protocols → subscription in ten seconds) and the full
// wizard (4 steps that cover domain validation, client targeting, per-card
// CDN/Argo wiring, and a completion-page port verification checklist).
//
// Both modes hit POST /api/v1/wizard/create-funnel; the only difference is
// what's in the payload. The page therefore funnels through a single
// `create.mutate()` call no matter which mode is active.

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { Link } from "react-router-dom";
import { QRCodeSVG } from "qrcode.react";

import Layout from "../components/Layout";
import { Button, Card, ErrorText, Field, Input, Modal, PageHeader, Select, Toggle } from "../components/ui";
import { api, call } from "../api/client";
import PortInput, { type PortsReservedSnapshot } from "../components/PortInput";
import AccelStatusNote from "../components/AccelStatusNote";
import { SUB_FORMATS, subUrlForFmt, type SubFormat } from "../lib/subscription";
import { copyText } from "../lib/clipboard";
import CopyButton from "../components/CopyButton";
import {
  CLIENT_IDS,
  COMPAT_MATRIX,
  PROTO_IDS,
  PROTO_META,
  type ClientId,
  type ProtoId,
} from "../lib/protocolMeta";

type DomainStatus = "ok" | "proxied" | "mismatch" | "none";

interface ValidateRes {
  status: DomainStatus;
  domain: string;
  resolved_ips: string[];
  vps_public_ip: string;
}

interface ProtoSelection {
  id: ProtoId;
  port: number; // 0 = use default
  cdn: boolean;
  argoNamed: boolean;
  argoTemp: boolean;
  argoToken: string;
  // Hy2-only: turn salamander obfs on (default OFF). When toggled on the
  // wizard shows a red warning that Stash / Surge / Karing will drop the
  // node. Non-Hy2 protocols ignore this field.
  hy2Obfs: boolean;
  // Hy2-only port hopping (default OFF = both 0). When set, the server
  // nat-redirects the inbound UDP range [start,end] to the listen port so
  // clients spray across many ports to dodge single-port UDP throttling.
  hy2HopStart: number;
  hy2HopEnd: number;
}

interface FunnelInbound {
  id: number;
  ui_type: string;
  backend: string;
  port: number;
  tag: string;
  remark: string;
}

interface FunnelResult {
  inbounds: FunnelInbound[];
  client_email: string;
  subscription_id: number;
  subscription_token: string;
  subscription_url: string;
  host: string; // literal IP operator picked in Step1
  domain_status: DomainStatus;
  cert_mode?: string; // "none" | "self-signed" | "acme"
  cert_domain?: string;
  cert_error?: string;
}

interface AdvancedConfig {
  cdn_enabled: boolean;
  argo_enabled: boolean;
  argo_mode: string;
  argo_domain: string;
}

interface SystemInfo {
  panel_port: number;
  network_capability?: {
    ipv4: boolean;
    ipv4_addr: string;
    ipv4_addrs?: string[]; // full list, multi-IP VPS
    ipv6_global: boolean;
    ipv6_addr: string;
    ipv6_addrs?: string[]; // full list, multi-IP VPS
  };
}

// the inbound bind IP. A specific literal address picked from
// network_capability.ipv4_addrs / ipv6_addrs. Replaces the old "v4" | "v6"
// family token — the back-end now wants the concrete IP so listen + URI
// host both point at the operator-chosen address.

type Mode = "quick" | "scenario" | "full";
const QUICK_PROTOS: ProtoId[] = [
  "vless-reality",
  "hysteria2",
  "shadowsocks-2022",
  "socks5",
];

// XHTTP is the only transport the bundled sing-box can't serve — it needs the
// optional xray-core engine. requiresXray flags those protocols so the wizard
// can lock them until xray is installed (backendType mirrors the server-side
// engine routing).
function requiresXray(id: ProtoId): boolean {
  return PROTO_META[id].backendType === "vless-xhttp";
}

// useXrayInstalled reports whether xray-core is present on the host, sharing the
// ["xray-status"] query cache with the System info panel. Defaults to false
// (locked) while loading or on error so a stale/failed probe never opens a
// protocol the node can't serve — the backend funnel guard remains the real
// safety net.
function useXrayInstalled(): boolean {
  const { data } = useQuery({
    queryKey: ["xray-status"],
    queryFn: () => call<{ installed: boolean }>(api.get("/system/xray/status")),
    retry: false,
    staleTime: 60_000,
  });
  return data?.installed === true;
}

// XrayLockedHint is the inline notice shown under an XHTTP protocol the node
// can't serve yet, with a link to the System info panel (embedded in 总览/"/")
// where xray-core installs in one click.
function XrayLockedHint({ t }: { t: TFunction }) {
  return (
    <div className="mt-1 text-[11px] text-amber-600 dark:text-amber-400">
      {t("createInbound.xrayLocked")}{" "}
      <Link to="/" className="underline">
        {t("createInbound.xrayLockedCta")}
      </Link>
    </div>
  );
}

// SocksPlaintextHint warns that SOCKS5 carries no encryption or obfuscation, so
// it belongs on trusted / local networks; on public or network-restricted paths
// the cleartext is easily disrupted (observed on real carrier links — TCP
// reaches the port but the proxied payload is dropped), and an encrypted
// protocol is the right choice. Shown inline under the SOCKS5 card.
function SocksPlaintextHint({ t }: { t: TFunction }) {
  return (
    <div className="mt-1 text-[11px] text-amber-600 dark:text-amber-400">
      ⚠ {t("createInbound.socksPlaintextWarn")}
    </div>
  );
}

// Scenarios feed the 场景 mode chooser. Each scenario maps to 1-3 protocols
// the user will land on in Quick mode pre-checked, with rationale so they
// see why we picked these. Keep this list tight — 6 scenarios is the sweet
// spot before pickers start dithering.
type ScenarioId =
  | "personal-fast"
  | "work-travel"
  | "family-multi"
  | "share-one"
  | "stealth-max"
  | "lan-tool";

interface Scenario {
  id: ScenarioId;
  emoji: string;
  protocols: ProtoId[];
}

const SCENARIOS: Scenario[] = [
  { id: "personal-fast", emoji: "🚀", protocols: ["vless-reality", "hysteria2"] },
  { id: "work-travel", emoji: "💼", protocols: ["vless-reality", "shadowsocks-2022"] },
  { id: "family-multi", emoji: "👨‍👩‍👧", protocols: ["vless-reality", "hysteria2", "shadowsocks-2022"] },
  { id: "share-one", emoji: "🎁", protocols: ["shadowsocks-2022"] },
  { id: "stealth-max", emoji: "🛡️", protocols: ["vless-xhttp-reality"] },
  { id: "lan-tool", emoji: "🧰", protocols: ["socks5"] },
];

function newSelection(id: ProtoId, defaultsByID: Record<ProtoId, number>): ProtoSelection {
  return {
    id,
    port: defaultsByID[id],
    cdn: false,
    argoNamed: false,
    argoTemp: false,
    argoToken: "",
    hy2Obfs: false,
    hy2HopStart: 0,
    hy2HopEnd: 0,
  };
}

export default function CreateInboundPage() {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode | null>(null);
  const [seedProtos, setSeedProtos] = useState<ProtoId[]>([]);
  const [scenarioId, setScenarioId] = useState<string | null>(null);
  const [host, setHost] = useState<string>("");

  const { data: ports } = useQuery({
    queryKey: ["system-ports"],
    queryFn: () => call<PortsReservedSnapshot>(api.get("/system/ports/reserved")),
    retry: false,
  });
  const { data: advanced } = useQuery({
    queryKey: ["advanced"],
    queryFn: () => call<AdvancedConfig>(api.get("/advanced")),
    retry: false,
  });
  const { data: sysInfo } = useQuery({
    queryKey: ["system-info"],
    queryFn: () => call<SystemInfo>(api.get("/system/info")),
    retry: false,
  });
  const argoTokenReady = !!advanced && advanced.argo_enabled && advanced.argo_mode === "fixed";
  // Argo singleton (1a): a node hosts at most one argo_bound inbound. If one
  // already exists, every Argo option in the wizard is locked with a pointer to
  // it — the backend enforces this too (funnel argo guard), but locking up front
  // tells the user why instead of letting them build then bounce.
  const { data: existingInbounds } = useQuery({
    queryKey: ["inbounds"],
    queryFn: () =>
      call<{ tag: string; settings: string }[]>(api.get("/inbounds")),
    retry: false,
  });
  const existingArgoTag = (existingInbounds ?? []).find((ib) => {
    try {
      const s = JSON.parse(ib.settings || "{}");
      return s.argo_bound === true || s.argo_bound === "true";
    } catch {
      return false;
    }
  })?.tag;
  const cap = sysInfo?.network_capability;
  // capabilityIPs: full lists with legacy-shape fallback so an old
  // network.json (singular addr) still renders one option per family.
  const v4Addrs = useMemo<string[]>(() => {
    if (!cap) return [];
    if (cap.ipv4_addrs && cap.ipv4_addrs.length > 0) return cap.ipv4_addrs;
    if (cap.ipv4_addr) return [cap.ipv4_addr];
    return [];
  }, [cap]);
  const v6Addrs = useMemo<string[]>(() => {
    if (!cap) return [];
    if (cap.ipv6_addrs && cap.ipv6_addrs.length > 0) return cap.ipv6_addrs;
    if (cap.ipv6_addr) return [cap.ipv6_addr];
    return [];
  }, [cap]);
  // Auto-pick a sensible default once cap loads. Prefer v4 (matches the old
  // single-stack default + most users dial v4). Only changes `host` when it's
  // empty / no longer in the detected list — keeps user selections intact.
  useEffect(() => {
    if (!cap) return;
    const all = [...v4Addrs, ...v6Addrs];
    if (all.length === 0) return;
    if (!host || !all.includes(host)) {
      setHost(v4Addrs[0] ?? v6Addrs[0] ?? "");
    }
  }, [cap, v4Addrs, v6Addrs, host]);
  const vpsHost = useMemo(() => {
    // The completion checklist's nc lines need the VPS public IP; pull it
    // from the validate-domain echo when available, otherwise from system
    // info (which sits behind the same admin auth boundary).
    return ""; // filled per call site below from the result payload
  }, []);

  return (
    <Layout>
      <PageHeader
        title={t("createInbound.title")}
        subtitle={t("createInbound.subtitle")}
      />
      {mode === null && (
        <>
          <HostChooser
            v4Addrs={v4Addrs}
            v6Addrs={v6Addrs}
            value={host}
            onChange={setHost}
            t={t}
          />
          <div className="h-3" />
          <ModeChooser
            onPick={(m) => {
              setSeedProtos([]);
              setScenarioId(null);
              setMode(m);
            }}
            t={t}
          />
        </>
      )}
      {mode === "scenario" && (
        <ScenarioFlow
          onPick={(id, protos) => {
            setSeedProtos(protos);
            setScenarioId(id);
            setMode("quick");
          }}
          onBack={() => setMode(null)}
          t={t}
        />
      )}
      {mode === "quick" && (
        <QuickFlow
          ports={ports}
          panelPort={sysInfo?.panel_port ?? 0}
          seedProtos={seedProtos}
          scenarioId={scenarioId}
          host={host}
          onBack={() => {
            // Came from a scenario → step back to the scenario chooser, not
            // all the way to the mode picker. Plain Quick → mode picker.
            const cameFromScenario = !!scenarioId;
            setSeedProtos([]);
            setScenarioId(null);
            setMode(cameFromScenario ? "scenario" : null);
          }}
          t={t}
        />
      )}
      {mode === "full" && (
        <FullFlow
          ports={ports}
          panelPort={sysInfo?.panel_port ?? 0}
          advanced={advanced}
          argoTokenReady={argoTokenReady}
          existingArgoTag={existingArgoTag}
          host={host}
          onBack={() => setMode(null)}
          t={t}
        />
      )}
    </Layout>
  );
}

/* ───────────────────────── Host-family chooser ───────────────────────── */

// HostChooser is the always-visible Card that lets the user pick the literal
// IP every inbound created in this round will bind to AND advertise as the
// subscription URI's server. Regimes:
//   - single family + single IP: section auto-selects, no UI noise
//   - single family + N≥2 IPs: that family's section with radio list, plus
//     a multi-IP hint card explaining the per-(family, protocol) unique-IP
//     rule
//   - dual-stack: both v4 and v6 sections visible, cross-family exclusive
//     selection (one IP total per wizard run). Multi-IP hint appears if
//     either family has ≥2 IPs
//   - empty (no detected IPs): fail state, hint to re-run detect
//
// The chosen IP shows up server-side as inbound.Listen (sing-box bind) AND
// inbound.SubscriptionHost (URI server). For Argo-named inbounds the wizard
// overrides listen to 127.0.0.1 + leaves SubscriptionHost empty so the share
// resolver picks the Argo tunnel domain instead.
function HostChooser({
  v4Addrs,
  v6Addrs,
  value,
  onChange,
  t,
}: {
  v4Addrs: string[];
  v6Addrs: string[];
  value: string;
  onChange: (v: string) => void;
  t: TFunction;
}) {
  const showV4 = v4Addrs.length > 0;
  const showV6 = v6Addrs.length > 0;
  const multiIP = v4Addrs.length > 1 || v6Addrs.length > 1;
  const empty = !showV4 && !showV6;
  return (
    <Card title={t("createInbound.familyTitle")}>
      <div className="text-xs text-black/55 dark:text-white/55 mb-3">
        {t("createInbound.familyHint")}
      </div>
      {empty && (
        <div className="text-sm text-rose-500">
          {t("createInbound.hostEmpty")}
        </div>
      )}
      {multiIP && (
        <div className="mb-3 rounded-xl border border-amber-500/40 bg-amber-500/[0.08] p-3 text-xs text-amber-900 dark:text-amber-200">
          {t("createInbound.multiIPHint")}
        </div>
      )}
      <div className="grid sm:grid-cols-2 gap-3">
        {showV4 && (
          <FamilySection
            label="IPv4"
            addrs={v4Addrs}
            value={value}
            onChange={onChange}
          />
        )}
        {showV6 && (
          <FamilySection
            label="IPv6"
            addrs={v6Addrs}
            value={value}
            onChange={onChange}
          />
        )}
      </div>
      {showV4 && !showV6 && (
        <div className="mt-3 text-xs text-black/50 dark:text-white/50">
          {t("createInbound.familySingleStackNote")}
        </div>
      )}
      {!showV4 && showV6 && (
        <div className="mt-3 text-xs text-black/50 dark:text-white/50">
          {t("createInbound.familySingleStackNote")}
        </div>
      )}
    </Card>
  );
}

function FamilySection({
  label,
  addrs,
  value,
  onChange,
}: {
  label: string;
  addrs: string[];
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="rounded-xl border border-black/15 dark:border-white/15 p-3">
      <div className="text-sm font-semibold mb-2">{label}</div>
      <div className="flex flex-col gap-1.5">
        {addrs.map((addr) => {
          const active = value === addr;
          return (
            <label
              key={addr}
              className={`flex items-center gap-2 rounded-lg border px-2.5 py-1.5 cursor-pointer transition ${
                active
                  ? "border-emerald-500/60 bg-emerald-500/10"
                  : "border-black/10 dark:border-white/10 hover:border-emerald-500/40 hover:bg-emerald-500/5"
              }`}
            >
              <input
                type="radio"
                name="host-ip"
                checked={active}
                onChange={() => onChange(addr)}
                className="accent-emerald-500"
              />
              <span className="text-xs font-mono break-all text-black/70 dark:text-white/70">
                {addr}
              </span>
            </label>
          );
        })}
      </div>
    </div>
  );
}

/* ───────────────────────── Mode chooser ───────────────────────── */

function ModeChooser({ onPick, t }: { onPick: (m: Mode) => void; t: TFunction }) {
  const cards: { m: Mode; emoji: string; key: "Quick" | "Scenario" | "Full"; border: string; bg: string; hover: string }[] = [
    { m: "quick", emoji: "🚀", key: "Quick", border: "border-emerald-500/40", bg: "bg-emerald-500/[0.04]", hover: "hover:bg-emerald-500/10" },
    { m: "scenario", emoji: "🎯", key: "Scenario", border: "border-amber-500/40", bg: "bg-amber-500/[0.04]", hover: "hover:bg-amber-500/10" },
    { m: "full", emoji: "🔧", key: "Full", border: "border-blue-500/40", bg: "bg-blue-500/[0.04]", hover: "hover:bg-blue-500/10" },
  ];
  return (
    <div className="flex flex-col gap-3">
      {cards.map((c) => (
        <button
          key={c.m}
          onClick={() => onPick(c.m)}
          className={`text-left rounded-2xl border ${c.border} ${c.bg} p-5 ${c.hover} transition`}
        >
          <div className="flex items-start gap-3">
            <div className="text-2xl shrink-0">{c.emoji}</div>
            <div>
              <div className="text-base font-semibold mb-1">
                {t(`createInbound.mode${c.key}`)}
              </div>
              <div className="text-sm text-black/60 dark:text-white/60">
                {t(`createInbound.mode${c.key}Hint`)}
              </div>
            </div>
          </div>
        </button>
      ))}
    </div>
  );
}

/* ───────────────────────── Scenario mode ───────────────────────── */

function ScenarioFlow({
  onPick,
  onBack,
  t,
}: {
  onPick: (id: string, protos: ProtoId[]) => void;
  onBack: () => void;
  t: TFunction;
}) {
  const xrayInstalled = useXrayInstalled();
  return (
    <Card title={t("createInbound.scenarioTitle")}>
      <div className="text-xs text-black/50 dark:text-white/50 mb-4">
        {t("createInbound.scenarioHint")}
      </div>
      <div className="grid md:grid-cols-2 gap-3">
        {SCENARIOS.map((s) => {
          // Scenarios built purely on XHTTP (e.g. stealth-max) can't run until
          // xray-core is installed — lock the card so the user doesn't land in
          // a dead-end batch where the only protocol is unselectable.
          const locked = s.protocols.some(requiresXray) && !xrayInstalled;
          return (
          <button
            key={s.id}
            disabled={locked}
            onClick={() => onPick(s.id, s.protocols)}
            className={`text-left rounded-xl border border-black/10 dark:border-white/10 bg-white/[0.02] p-4 transition ${
              locked
                ? "opacity-60 cursor-not-allowed"
                : "hover:bg-emerald-500/5 hover:border-emerald-500/40"
            }`}
          >
            <div className="flex items-start gap-3">
              <div className="text-xl shrink-0">{s.emoji}</div>
              <div className="min-w-0">
                <div className="text-sm font-semibold mb-1">
                  {t(`createInbound.scenarios.${s.id}.title`)}
                </div>
                <div className="text-xs text-black/55 dark:text-white/55 mb-2">
                  {t(`createInbound.scenarios.${s.id}.desc`)}
                </div>
                <div className="flex flex-wrap gap-1">
                  {s.protocols.map((p) => (
                    <span
                      key={p}
                      className="inline-flex rounded-md border border-emerald-500/30 bg-emerald-500/10 px-1.5 py-0.5 text-[10px] font-medium text-emerald-700 dark:text-emerald-300"
                    >
                      {t(`guide.proto.${p}.name`)}
                    </span>
                  ))}
                </div>
                {locked && (
                  <div className="mt-2 text-[11px] text-amber-600 dark:text-amber-400">
                    {t("createInbound.xrayLocked")}
                  </div>
                )}
              </div>
            </div>
          </button>
          );
        })}
      </div>
      <div className="mt-4">
        <Button variant="ghost" onClick={onBack}>
          {t("createInbound.backToModes")}
        </Button>
      </div>
    </Card>
  );
}

/* ───────────────────────── Quick mode ───────────────────────── */

function QuickFlow({
  ports,
  panelPort,
  seedProtos,
  scenarioId,
  host,
  onBack,
  t,
}: {
  ports?: PortsReservedSnapshot;
  panelPort: number;
  seedProtos?: ProtoId[];
  scenarioId?: string | null;
  host: string;
  onBack: () => void;
  t: TFunction;
}) {
  const hostFamily: "v4" | "v6" = host.includes(":") ? "v6" : "v4";
  const defaultsByID: Record<ProtoId, number> = useMemo(() => {
    const m: Record<string, number> = {};
    for (const id of PROTO_IDS) {
      m[id] = computeDefaultPort(id, ports, hostFamily);
    }
    return m as Record<ProtoId, number>;
  }, [ports, hostFamily]);
  // Scenario mode shows ONLY that scenario's protocols (pre-checked) — the
  // other Quick protocols stay hidden so the user sees exactly what the
  // scenario builds. Plain Quick mode (no seed) shows the full Quick set to
  // pick from. This also fixes the stealth-max case where the seeded
  // vless-xhttp-reality wasn't in QUICK_PROTOS, so it counted as "1 selected"
  // with no visible row.
  const displayProtos = useMemo<ProtoId[]>(
    () => (seedProtos && seedProtos.length > 0 ? seedProtos : QUICK_PROTOS),
    [seedProtos],
  );
  // When entered via 场景 mode, pre-check the scenario's protocol list so the
  // operator lands on the Quick grid with the right boxes already ticked.
  // Use computeDefaultPort so seeded ports already skip occupied slots; if
  // `ports` hasn't loaded yet we fall back to the static defaults.
  const [selections, setSelections] = useState<Map<ProtoId, ProtoSelection>>(() => {
    if (!seedProtos || seedProtos.length === 0) return new Map();
    const seedDefaults = Object.fromEntries(
      PROTO_IDS.map((p) => [p, computeDefaultPort(p, ports, hostFamily)]),
    ) as Record<ProtoId, number>;
    const m = new Map<ProtoId, ProtoSelection>();
    for (const id of seedProtos) {
      m.set(id, newSelection(id, seedDefaults));
    }
    return m;
  });
  const [result, setResult] = useState<FunnelResult | null>(null);

  const create = useMutation({
    mutationFn: () => {
      // Mode-tagged bundle name so the subscription list disambiguates Quick /
      // Scenario / Full entries without leaning on a server-UTC timestamp in the
      // label (the "创建于" column already shows created_at in the user's local
      // TZ). Scenario mode falls into QuickFlow with seedProtos + scenarioId,
      // so the scenario branch wins over plain Quick when both apply.
      const bundleName = scenarioId
        ? `EdgeNest ${t(`createInbound.scenarios.${scenarioId}.title`)}`
        : `EdgeNest ${t("createInbound.bundleNameQuick")}`;
      return call<FunnelResult>(
        api.post("/wizard/create-funnel", {
          domain: "",
          clients: [],
          client_email: "wizard@local",
          bundle_name: bundleName,
          host,
          protocols: Array.from(selections.values()).map((s) => ({
            id: s.id,
            cdn: false,
            argo_named: false,
            port: s.port || 0,
            hy2_obfs: s.hy2Obfs,
            hy2_port_hop_start: s.hy2HopStart || 0,
            hy2_port_hop_end: s.hy2HopEnd || 0,
          })),
        }),
      );
    },
    onSuccess: (r) => setResult(r),
  });

  const xrayInstalled = useXrayInstalled();

  function toggle(id: ProtoId) {
    if (requiresXray(id) && !xrayInstalled) return;
    const next = new Map(selections);
    if (next.has(id)) next.delete(id);
    else next.set(id, newSelection(id, defaultsByID));
    setSelections(next);
  }

  function setPort(id: ProtoId, port: number) {
    const cur = selections.get(id);
    if (!cur) return;
    const next = new Map(selections);
    next.set(id, { ...cur, port });
    setSelections(next);
  }

  if (result) {
    return (
      <ResultPanel
        result={result}
        ports={ports}
        panelPort={panelPort}
        protocols={Array.from(selections.values())}
        onAnother={() => {
          setResult(null);
          setSelections(new Map());
        }}
        onBack={onBack}
        t={t}
      />
    );
  }

  const inBatch = Array.from(selections.values())
    .map((s) => s.port || defaultsByID[s.id])
    .filter((p) => p > 0);

  return (
    <Card title={t("createInbound.quickTitle")}>
      <div className="text-xs text-black/50 dark:text-white/50 mb-4">
        {t("createInbound.quickHint")}
      </div>
      <div className="flex items-center gap-2 mb-3">
        <Button
          onClick={() => {
            const ids: ProtoId[] = displayProtos.filter(
              (id) => !(requiresXray(id) && !xrayInstalled),
            );
            // If everything is already selected, treat the button as
            // "clear all" so the same control toggles both directions.
            if (ids.every((id) => selections.has(id))) {
              setSelections(new Map());
              return;
            }
            const next = new Map<ProtoId, ProtoSelection>();
            for (const id of ids) next.set(id, newSelection(id, defaultsByID));
            setSelections(next);
          }}
        >
          {selections.size > 0 &&
          displayProtos
            .filter((id) => !(requiresXray(id) && !xrayInstalled))
            .every((id) => selections.has(id))
            ? t("createInbound.selectNone")
            : t("createInbound.selectAll")}
        </Button>
        {selections.size > 0 && (
          <span className="text-xs text-black/50 dark:text-white/50">
            {t("createInbound.selectedCount", { n: selections.size })}
          </span>
        )}
      </div>
      <div className="space-y-3">
        {displayProtos.map((id) => {
          const sel = selections.get(id);
          const locked = requiresXray(id) && !xrayInstalled;
          return (
            <div
              key={id}
              className={`rounded-lg border p-3 ${
                sel
                  ? "border-emerald-500/60 bg-emerald-500/5"
                  : "border-black/10 dark:border-white/10"
              } ${locked ? "opacity-60" : ""}`}
            >
              <label
                className={`flex items-start gap-3 ${
                  locked ? "cursor-not-allowed" : "cursor-pointer"
                }`}
              >
                <input
                  type="checkbox"
                  checked={!!sel}
                  disabled={locked}
                  onChange={() => toggle(id)}
                  className="mt-1"
                />
                <div className="flex-1">
                  <div className="text-sm font-mono">
                    {t(`guide.proto.${id}.name`)}
                  </div>
                  <div className="text-xs text-black/60 dark:text-white/60 mt-0.5">
                    {t(`guide.proto.${id}.brief`)}
                  </div>
                  {locked && <XrayLockedHint t={t} />}
                  {id === "socks5" && <SocksPlaintextHint t={t} />}
                </div>
              </label>
              {sel && (
                <div className="mt-3 pl-6">
                  <IspRiskStrip id={id} t={t} />
                  <div className="max-w-xs">
                    <div className="text-[11px] text-black/60 dark:text-white/60 mb-1">
                      {t("createInbound.portLabel")}
                    </div>
                    <PortInput
                      value={sel.port}
                      defaultPort={defaultsByID[id]}
                      onChange={(p) => setPort(id, p)}
                      cdn={false}
                      snapshot={ports}
                      inBatch={inBatch}
                      family={hostFamily}
                    />
                    {defaultsByID[id] !== defaultPortFor(id) && (
                      <div className="mt-1 text-[11px] text-amber-600 dark:text-amber-400">
                        {t("createInbound.portShifted", {
                          original: defaultPortFor(id),
                          actual: defaultsByID[id],
                        })}
                      </div>
                    )}
                  </div>
                  {id === "hysteria2" && (
                    <Hy2ObfsAdvancedToggle
                      checked={sel.hy2Obfs}
                      onChange={(v) =>
                        setSelections(
                          new Map(selections).set(id, { ...sel, hy2Obfs: v }),
                        )
                      }
                      t={t}
                    />
                  )}
                  {id === "hysteria2" && (
                    <Hy2PortHopAdvanced
                      start={sel.hy2HopStart}
                      end={sel.hy2HopEnd}
                      onChange={(start, end) =>
                        setSelections(
                          new Map(selections).set(id, {
                            ...sel,
                            hy2HopStart: start,
                            hy2HopEnd: end,
                          }),
                        )
                      }
                      t={t}
                    />
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
      {create.error && <ErrorText>{(create.error as Error).message}</ErrorText>}
      <div className="flex justify-between mt-4">
        <Button variant="ghost" onClick={onBack}>
          {scenarioId
            ? t("createInbound.back")
            : t("createInbound.backToModes")}
        </Button>
        <Button
          variant="primary"
          onClick={() => create.mutate()}
          disabled={selections.size === 0 || create.isPending}
        >
          {create.isPending ? t("common.loading") : t("createInbound.create")}
        </Button>
      </div>
    </Card>
  );
}

/* ───────────────────────── Full mode (4-step wizard) ───────────────────────── */

function FullFlow({
  ports,
  panelPort,
  advanced,
  argoTokenReady,
  existingArgoTag,
  host,
  onBack,
  t,
}: {
  ports?: PortsReservedSnapshot;
  panelPort: number;
  advanced?: AdvancedConfig;
  argoTokenReady: boolean;
  existingArgoTag?: string;
  host: string;
  onBack: () => void;
  t: TFunction;
}) {
  const [step, setStep] = useState<1 | 2 | 3 | 4>(1);
  const [domain, setDomain] = useState("");
  const [acmeEmail, setAcmeEmail] = useState("");
  const [domainState, setDomainState] = useState<ValidateRes | null>(null);
  const [clients, setClients] = useState<Set<ClientId>>(new Set());
  const hostFamily: "v4" | "v6" = host.includes(":") ? "v6" : "v4";
  const defaultsByID: Record<ProtoId, number> = useMemo(() => {
    const m: Record<string, number> = {};
    for (const id of PROTO_IDS) m[id] = computeDefaultPort(id, ports, hostFamily);
    return m as Record<ProtoId, number>;
  }, [ports, hostFamily]);
  const [selections, setSelections] = useState<Map<ProtoId, ProtoSelection>>(new Map());
  const [result, setResult] = useState<FunnelResult | null>(null);

  const validate = useMutation({
    mutationFn: (d: string) =>
      call<ValidateRes>(api.post("/wizard/validate-domain", { domain: d })),
    onSuccess: (d) => setDomainState(d),
  });

  const create = useMutation({
    mutationFn: () => {
      const sels = Array.from(selections.values());
      // Build advanced overrides only when the operator actually toggled
      // anything; nil keeps the existing /advanced row untouched.
      const cdnOn = sels.some((s) => s.cdn);
      const argoNamed = sels.find((s) => s.argoNamed);
      const argoTemp = sels.find((s) => s.argoTemp);
      let overrides: Record<string, unknown> | null = null;
      if (cdnOn || argoNamed || argoTemp) {
        overrides = {};
        if (argoNamed) {
          overrides.argo_mode = "fixed";
          if (argoNamed.argoToken) overrides.argo_token = argoNamed.argoToken;
        } else if (argoTemp) {
          overrides.argo_mode = "temp";
        }
      }
      return call<FunnelResult>(
        api.post("/wizard/create-funnel", {
          domain,
          clients: Array.from(clients),
          client_email: "wizard@local",
          acme_email: acmeEmail.trim(),
          bundle_name: `EdgeNest ${t("createInbound.bundleNameFull")}`,
          host,
          advanced_overrides: overrides,
          protocols: sels.map((s) => ({
            id: s.id,
            cdn: s.cdn,
            argo_named: s.argoNamed || s.argoTemp,
            port: s.port || 0,
            hy2_obfs: s.hy2Obfs,
            hy2_port_hop_start: s.hy2HopStart || 0,
            hy2_port_hop_end: s.hy2HopEnd || 0,
          })),
        }),
      );
    },
    onSuccess: (r) => {
      setResult(r);
      setStep(4);
    },
  });

  if (step === 4 && result) {
    return (
      <ResultPanel
        result={result}
        ports={ports}
        panelPort={panelPort}
        protocols={Array.from(selections.values())}
        onAnother={() => {
          setStep(1);
          setDomain("");
          setAcmeEmail("");
          setDomainState(null);
          setClients(new Set());
          setSelections(new Map());
          setResult(null);
        }}
        onBack={onBack}
        t={t}
      />
    );
  }

  return (
    <div className="space-y-4">
      <FullStepStrip step={step} t={t} />
      {step === 1 && (
        <Step1Domain
          domain={domain}
          setDomain={setDomain}
          acmeEmail={acmeEmail}
          setAcmeEmail={setAcmeEmail}
          domainState={domainState}
          onValidate={() => validate.mutate(domain)}
          onSkip={() => {
            setDomain("");
            setDomainState({ status: "none", domain: "", resolved_ips: [], vps_public_ip: "" });
            setStep(2);
          }}
          onNext={() => setStep(2)}
          onBackToModes={onBack}
          validating={validate.isPending}
          err={validate.error as Error | null}
          t={t}
        />
      )}
      {step === 2 && (
        <Step2Clients
          clients={clients}
          setClients={setClients}
          onBack={() => setStep(1)}
          onNext={() => setStep(3)}
          t={t}
        />
      )}
      {step === 3 && domainState && (
        <Step3Protocols
          domainState={domainState}
          clients={clients}
          selections={selections}
          setSelections={setSelections}
          defaultsByID={defaultsByID}
          ports={ports}
          panelPort={panelPort}
          advanced={advanced}
          argoTokenReady={argoTokenReady}
          existingArgoTag={existingArgoTag}
          hostFamily={hostFamily}
          onBack={() => setStep(2)}
          onCreate={() => create.mutate()}
          creating={create.isPending}
          err={create.error as Error | null}
          t={t}
        />
      )}
    </div>
  );
}

function FullStepStrip({ step, t }: { step: 1 | 2 | 3 | 4; t: TFunction }) {
  const steps: { n: 1 | 2 | 3 | 4; key: string }[] = [
    { n: 1, key: "createInbound.step1Label" },
    { n: 2, key: "createInbound.step2Label" },
    { n: 3, key: "createInbound.step3Label" },
    { n: 4, key: "createInbound.step4Label" },
  ];
  return (
    <div className="flex items-center gap-3 text-xs">
      {steps.map((s, i) => (
        <div key={s.n} className="flex items-center gap-2">
          <div
            className={`flex items-center justify-center w-6 h-6 rounded-full font-medium ${
              step === s.n
                ? "bg-emerald-500 text-black"
                : step > s.n
                ? "bg-emerald-500/30 text-emerald-500"
                : "bg-black/10 dark:bg-white/10 text-black/40 dark:text-white/40"
            }`}
          >
            {step > s.n ? "✓" : s.n}
          </div>
          <span
            className={
              step === s.n
                ? "text-black dark:text-white"
                : step > s.n
                ? "text-black/60 dark:text-white/60"
                : "text-black/40 dark:text-white/40"
            }
          >
            {t(s.key)}
          </span>
          {i < steps.length - 1 && (
            <span className="text-black/20 dark:text-white/20">·</span>
          )}
        </div>
      ))}
    </div>
  );
}

function Step1Domain({
  domain,
  setDomain,
  acmeEmail,
  setAcmeEmail,
  domainState,
  onValidate,
  onSkip,
  onNext,
  onBackToModes,
  validating,
  err,
  t,
}: {
  domain: string;
  setDomain: (s: string) => void;
  acmeEmail: string;
  setAcmeEmail: (s: string) => void;
  domainState: ValidateRes | null;
  onValidate: () => void;
  onSkip: () => void;
  onNext: () => void;
  onBackToModes: () => void;
  validating: boolean;
  err: Error | null;
  t: TFunction;
}) {
  // A usable domain (grey-cloud "ok" / orange-cloud "proxied") lets the wizard
  // issue a real ACME cert, which needs a contact email. Optional — left blank
  // it falls back to the acme_email setting, and absent that the batch quietly
  // stays on the self-signed pair.
  const canACME = domainState?.status === "ok" || domainState?.status === "proxied";
  const [showCdnGuide, setShowCdnGuide] = useState(false);
  return (
    <Card title={t("createInbound.step1Title")}>
      <div className="text-xs text-black/50 dark:text-white/50 mb-3">
        {t("createInbound.step1Hint")}
      </div>
      {/* Key facts in colour — domains/cloud/CDN-port are the三 things that
          decide whether a build does what the user expects. */}
      <div className="mb-3 rounded-lg border border-white/10 bg-white/[0.03] p-3 space-y-1.5 text-xs">
        <div className="font-medium text-black/70 dark:text-white/70">
          {t("createInbound.kfTitle")}
        </div>
        <div className="flex items-start gap-2">
          <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
          <span className="text-emerald-600 dark:text-emerald-300">
            {t("createInbound.kf1")}
          </span>
        </div>
        <div className="flex items-start gap-2">
          <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-orange-500" />
          <span className="text-orange-600 dark:text-orange-300">
            {t("createInbound.kf2")}
          </span>
        </div>
        <div className="flex items-start gap-2">
          <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-blue-500" />
          <span className="text-blue-600 dark:text-blue-300">
            {t("createInbound.kf3")}
          </span>
        </div>
        <div className="flex items-start gap-2">
          <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-purple-500" />
          <span className="text-purple-600 dark:text-purple-300">
            {t("createInbound.kf4")}
          </span>
        </div>
        <button
          type="button"
          onClick={() => setShowCdnGuide(true)}
          className="text-blue-600 dark:text-blue-300 underline underline-offset-2 hover:opacity-80"
        >
          {t("createInbound.cdnGuideBtn")}
        </button>
      </div>
      <Modal
        open={showCdnGuide}
        onClose={() => setShowCdnGuide(false)}
        title={t("createInbound.cdnGuideTitle")}
        footer={
          <Button variant="ghost" onClick={() => setShowCdnGuide(false)}>
            {t("common.close")}
          </Button>
        }
      >
        <div className="space-y-3 text-sm">
          <div className="rounded-md border border-blue-400/40 bg-blue-500/10 px-3 py-2 text-xs text-blue-600 dark:text-blue-300 whitespace-pre-line">
            ⚠ {t("guide.cdn.ports")}
          </div>
          <ol className="list-decimal list-inside space-y-1.5 text-black/80 dark:text-white/80">
            {["t1", "t2", "t3", "t4", "t5", "t6"].map((k) => (
              <li key={k} className="whitespace-pre-line">
                {t(`guide.cdn.${k}`)}
              </li>
            ))}
          </ol>
        </div>
      </Modal>
      <div className="grid md:grid-cols-2 gap-3 items-end">
        <Field label={t("createInbound.domainLabel")}>
          <Input
            value={domain}
            onChange={(e) => setDomain(e.target.value)}
            placeholder="proxy.example.com"
          />
        </Field>
        <div className="flex gap-2">
          <Button onClick={onValidate} disabled={!domain || validating}>
            {validating ? t("common.loading") : t("createInbound.validate")}
          </Button>
          <Button variant="ghost" onClick={onSkip}>
            {t("createInbound.skipDomain")}
          </Button>
        </div>
      </div>
      {err && <ErrorText>{err.message}</ErrorText>}
      {domainState && <DomainVerdict s={domainState} t={t} />}
      {canACME && (
        <div className="mt-3">
          <Field label={t("createInbound.acmeEmailLabel")}>
            <Input
              type="email"
              value={acmeEmail}
              onChange={(e) => setAcmeEmail(e.target.value)}
              placeholder="you@example.com"
            />
          </Field>
          <div className="text-xs text-black/50 dark:text-white/50 mt-1">
            {t("createInbound.acmeEmailHint")}
          </div>
        </div>
      )}
      <div className="mt-4 flex justify-between">
        <Button variant="ghost" onClick={onBackToModes}>
          {t("createInbound.backToModes")}
        </Button>
        {domainState && (
          <Button onClick={onNext} variant="primary">
            {t("createInbound.next")}
          </Button>
        )}
      </div>
    </Card>
  );
}

function DomainVerdict({ s, t }: { s: ValidateRes; t: TFunction }) {
  const styles: Record<DomainStatus, string> = {
    ok: "border-emerald-500/50 bg-emerald-500/10 text-emerald-600 dark:text-emerald-300",
    proxied: "border-orange-500/50 bg-orange-500/10 text-orange-500",
    mismatch: "border-rose-500/50 bg-rose-500/10 text-rose-500",
    none: "border-black/20 dark:border-white/20 bg-black/5 dark:bg-white/5 text-black/60 dark:text-white/60",
  };
  return (
    <div className={`mt-3 rounded-lg border p-3 text-sm ${styles[s.status]}`}>
      <div className="font-medium mb-1">{t(`createInbound.domainStatus.${s.status}`)}</div>
      <div className="text-xs opacity-80">
        {t("createInbound.resolvedIps")}: {s.resolved_ips.length === 0 ? "—" : s.resolved_ips.join(", ")}
      </div>
      <div className="text-xs opacity-80">
        {t("createInbound.vpsIp")}: {s.vps_public_ip || "—"}
      </div>
      {s.status === "proxied" && (
        <div className="mt-2 text-xs opacity-90">{t("createInbound.cdnHintProxied")}</div>
      )}
      {s.status === "ok" && (
        <div className="mt-2 text-xs opacity-90">{t("createInbound.cdnHintGrey")}</div>
      )}
    </div>
  );
}

function Step2Clients({
  clients,
  setClients,
  onBack,
  onNext,
  t,
}: {
  clients: Set<ClientId>;
  setClients: (s: Set<ClientId>) => void;
  onBack: () => void;
  onNext: () => void;
  t: TFunction;
}) {
  function toggle(id: ClientId) {
    const next = new Set(clients);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setClients(next);
  }
  return (
    <Card title={t("createInbound.step2Title")}>
      <div className="text-xs text-black/50 dark:text-white/50 mb-3">
        {t("createInbound.step2Hint")}
      </div>
      <div className="grid sm:grid-cols-2 lg:grid-cols-3 gap-2">
        {CLIENT_IDS.map((id) => {
          const checked = clients.has(id);
          return (
            <label
              key={id}
              className={`flex items-start gap-2 p-3 rounded-lg border cursor-pointer transition ${
                checked
                  ? "border-emerald-500/60 bg-emerald-500/5"
                  : "border-black/10 dark:border-white/10 hover:border-black/30 dark:hover:border-white/30"
              }`}
            >
              <input
                type="checkbox"
                checked={checked}
                onChange={() => toggle(id)}
                className="mt-1"
              />
              <div className="flex-1">
                <div className="text-sm font-medium">{t(`guide.client.${id}`)}</div>
                <div className="text-xs text-black/50 dark:text-white/50">
                  {t(`createInbound.clientDesc.${id}`)}
                </div>
              </div>
            </label>
          );
        })}
      </div>
      <div className="flex justify-between mt-4">
        <Button variant="ghost" onClick={onBack}>
          {t("createInbound.back")}
        </Button>
        <Button variant="primary" onClick={onNext} disabled={clients.size === 0}>
          {t("createInbound.next")}
        </Button>
      </div>
    </Card>
  );
}

function Step3Protocols({
  domainState,
  clients,
  selections,
  setSelections,
  defaultsByID,
  ports,
  panelPort,
  advanced,
  argoTokenReady,
  existingArgoTag,
  hostFamily,
  onBack,
  onCreate,
  creating,
  err,
  t,
}: {
  domainState: ValidateRes;
  clients: Set<ClientId>;
  selections: Map<ProtoId, ProtoSelection>;
  setSelections: (m: Map<ProtoId, ProtoSelection>) => void;
  defaultsByID: Record<ProtoId, number>;
  ports?: PortsReservedSnapshot;
  panelPort: number;
  advanced?: AdvancedConfig;
  argoTokenReady: boolean;
  existingArgoTag?: string;
  hostFamily: "v4" | "v6";
  onBack: () => void;
  onCreate: () => void;
  creating: boolean;
  err: Error | null;
  t: TFunction;
}) {
  // Unified certificate model: every protocol is creatable in every domain
  // state — a missing / mismatched domain just means the TLS rows fall back
  // to the self-signed cert (clients skip verification), exactly like Hy2 /
  // AnyTLS always did. The old domain gate that hid the WS/XHTTP rows is
  // gone; CDN acceleration alone still requires a proxied domain (toggle
  // visibility below).
  const visible = PROTO_IDS;
  const xrayInstalled = useXrayInstalled();

  // Whether the batch runs without a usable domain (self-signed direct
  // mode). Drives the "(自签直连)" protocol naming + the CDN hint.
  const noDomain =
    domainState.status === "mismatch" || domainState.status === "none";

  // Per-protocol soft compatibility warning (by design: warn, don't filter).
  // A client is incompatible when COMPAT_MATRIX has no entry at all for it.
  const incompatFor = (id: ProtoId): ClientId[] =>
    Array.from(clients).filter((c) => COMPAT_MATRIX[id][c] === undefined);

  function toggleProto(id: ProtoId) {
    if (requiresXray(id) && !xrayInstalled) return;
    const next = new Map(selections);
    if (next.has(id)) next.delete(id);
    else {
      const m = PROTO_META[id];
      const cdnDefault = m.cdn === "yes" && domainState.status === "proxied";
      next.set(id, {
        ...newSelection(id, defaultsByID),
        cdn: cdnDefault,
      });
    }
    setSelections(next);
  }

  function patch(id: ProtoId, p: Partial<ProtoSelection>) {
    const cur = selections.get(id);
    if (!cur) return;
    const next = new Map(selections);
    next.set(id, { ...cur, ...p });
    setSelections(next);
  }

  const inBatch = Array.from(selections.values())
    .map((s) => s.port || defaultsByID[s.id])
    .filter((p) => p > 0);

  // Argo singleton across the batch (1a): a node runs ONE tunnel = one origin
  // port = one protocol, so only one selected protocol may ride Argo. This is
  // the id that currently has Argo on (temp or named), if any — every other
  // protocol's Argo radios get locked with a pointer to it. existingArgoTag
  // (an Argo inbound already on the node) locks ALL of them.
  const argoSelectedBy = Array.from(selections.entries()).find(
    ([, s]) => s.argoTemp || s.argoNamed,
  )?.[0];

  return (
    <Card title={t("createInbound.step3Title")}>
      <div className="text-xs text-black/50 dark:text-white/50 mb-3">
        {t("createInbound.step3Hint")}
      </div>
      {clients.size > 0 && (
        <div className="text-xs text-black/60 dark:text-white/60 mb-3">
          {t("createInbound.step3ClientSummary", { count: clients.size })}
        </div>
      )}
      <div className="flex items-center gap-2 mb-3">
        <Button
          onClick={() => {
            const selectable = visible.filter(
              (id) => !(requiresXray(id) && !xrayInstalled),
            );
            if (selectable.every((id) => selections.has(id))) {
              setSelections(new Map());
              return;
            }
            const next = new Map<ProtoId, ProtoSelection>();
            for (const id of selectable) {
              const m = PROTO_META[id];
              const cdnDefault = m.cdn === "yes" && domainState.status === "proxied";
              next.set(id, { ...newSelection(id, defaultsByID), cdn: cdnDefault });
            }
            setSelections(next);
          }}
        >
          {selections.size > 0 &&
          visible
            .filter((id) => !(requiresXray(id) && !xrayInstalled))
            .every((id) => selections.has(id))
            ? t("createInbound.selectNone")
            : t("createInbound.selectAll")}
        </Button>
        {selections.size > 0 && (
          <span className="text-xs text-black/50 dark:text-white/50">
            {t("createInbound.selectedCount", { n: selections.size })}
          </span>
        )}
      </div>
      <div className="space-y-2">
        {visible.map((id) => {
          const m = PROTO_META[id];
          const sel = selections.get(id);
          const checked = !!sel;
          const locked = requiresXray(id) && !xrayInstalled;
          const incompat = incompatFor(id);
          // Without a usable domain the 3 CDN protocols are created as plain
          // self-signed direct inbounds — rename them so the row doesn't
          // promise a CDN it can't ride.
          const nameKey =
            noDomain && m.cdn === "yes"
              ? `guide.proto.${id}.nameDirect`
              : `guide.proto.${id}.name`;
          // Argo lock (1a): this protocol can't ride Argo if the node already
          // has an Argo inbound, or if another protocol in this batch already
          // took it. The owning protocol itself stays editable.
          const argoLockedByExisting = !!existingArgoTag;
          const argoLockedByOther =
            !argoLockedByExisting && !!argoSelectedBy && argoSelectedBy !== id;
          const argoLocked = argoLockedByExisting || argoLockedByOther;
          return (
            <div
              key={id}
              className={`rounded-lg border p-3 ${
                checked
                  ? "border-emerald-500/60 bg-emerald-500/5"
                  : "border-black/10 dark:border-white/10"
              } ${locked ? "opacity-60" : ""}`}
            >
              <label
                className={`flex items-start gap-2 ${
                  locked ? "cursor-not-allowed" : "cursor-pointer"
                }`}
              >
                <input
                  type="checkbox"
                  checked={checked}
                  disabled={locked}
                  onChange={() => toggleProto(id)}
                  className="mt-1"
                />
                <div className="flex-1">
                  <div className="text-sm font-mono">{t(nameKey)}</div>
                  <div className="text-xs text-black/60 dark:text-white/60 mt-0.5">
                    {t(`guide.proto.${id}.scenarios`)}
                  </div>
                  {locked && <XrayLockedHint t={t} />}
                  {id === "socks5" && <SocksPlaintextHint t={t} />}
                  {incompat.length > 0 && (
                    <div className="mt-1 text-[11px] text-amber-600 dark:text-amber-400">
                      ⚠{" "}
                      {t("createInbound.clientIncompatWarn", {
                        total: clients.size,
                        bad: incompat.length,
                        names: incompat.map((c) => t(`guide.client.${c}`)).join("、"),
                      })}
                    </div>
                  )}
                </div>
              </label>
              {sel && (
                <div className="mt-3 pl-6">
                  <IspRiskStrip id={id} t={t} />
                  <div className="grid md:grid-cols-2 gap-3 text-xs">
                  <div>
                    <div className="text-[11px] text-black/60 dark:text-white/60 mb-1">
                      {t("createInbound.portLabel")}
                    </div>
                    <PortInput
                      value={sel.port}
                      defaultPort={defaultsByID[id]}
                      onChange={(p) => patch(id, { port: p })}
                      cdn={m.cdn === "yes" && sel.cdn}
                      snapshot={ports}
                      inBatch={inBatch}
                      family={hostFamily}
                    />
                    {defaultsByID[id] !== defaultPortFor(id) && (
                      <div className="mt-1 text-[11px] text-amber-600 dark:text-amber-400">
                        {t("createInbound.portShifted", {
                          original: defaultPortFor(id),
                          actual: defaultsByID[id],
                        })}
                      </div>
                    )}
                    {id === "hysteria2" && (
                      <Hy2ObfsAdvancedToggle
                        checked={sel.hy2Obfs}
                        onChange={(v) => patch(id, { hy2Obfs: v })}
                        t={t}
                      />
                    )}
                    {id === "hysteria2" && (
                      <Hy2PortHopAdvanced
                        start={sel.hy2HopStart}
                        end={sel.hy2HopEnd}
                        onChange={(start, end) =>
                          patch(id, { hy2HopStart: start, hy2HopEnd: end })
                        }
                        t={t}
                      />
                    )}
                  </div>
                  {m.cdn === "yes" && (
                    <div className="space-y-2">
                      {domainState.status === "proxied" ? (
                        <label className="flex items-start gap-2 cursor-pointer">
                          <input
                            type="checkbox"
                            checked={sel.cdn}
                            onChange={(e) => patch(id, { cdn: e.target.checked })}
                            className="mt-0.5"
                          />
                          <div>
                            <div className="font-medium">
                              {t("createInbound.toggleCdn")}
                            </div>
                            <div className="text-black/50 dark:text-white/50">
                              {t("createInbound.toggleCdnProxiedHint")}
                            </div>
                            {!sel.cdn && (
                              <div className="mt-1 text-amber-700 dark:text-amber-300 text-[11px] bg-amber-400/10 border border-amber-400/20 rounded-lg px-2.5 py-1.5">
                                {t("createInbound.cdnOffProxiedHint")}
                              </div>
                            )}
                          </div>
                        </label>
                      ) : (
                        <div className="text-amber-700 dark:text-amber-300 text-[11px] bg-amber-400/10 border border-amber-400/20 rounded-lg px-2.5 py-1.5">
                          {t("createInbound.cdnNeedsProxied")}
                        </div>
                      )}
                      {m.argo === "yes" && (
                        <div className="rounded border border-black/10 dark:border-white/10 p-2">
                          <div className="font-medium mb-1">
                            {t("createInbound.argoSection")}
                          </div>
                          {argoLocked && (
                            <div className="mb-2 rounded border border-amber-400/40 bg-amber-400/10 px-2 py-1.5 text-[11px] text-amber-700 dark:text-amber-300">
                              {argoLockedByExisting
                                ? t("createInbound.argoLockedExisting", { tag: existingArgoTag })
                                : t("createInbound.argoLockedOther", {
                                    name: t(`guide.proto.${argoSelectedBy}.name`),
                                  })}
                            </div>
                          )}
                          <label className="flex items-start gap-2 cursor-pointer mb-1">
                            <input
                              type="radio"
                              name={`argo-${id}`}
                              checked={!sel.argoNamed && !sel.argoTemp}
                              onChange={() => patch(id, { argoNamed: false, argoTemp: false })}
                              className="mt-0.5"
                            />
                            <span>{t("createInbound.argoNone")}</span>
                          </label>
                          <label
                            className={`flex items-start gap-2 mb-1 ${
                              argoLocked ? "opacity-50 cursor-not-allowed" : "cursor-pointer"
                            }`}
                          >
                            <input
                              type="radio"
                              name={`argo-${id}`}
                              checked={sel.argoTemp}
                              disabled={argoLocked}
                              onChange={() => patch(id, { argoTemp: true, argoNamed: false })}
                              className="mt-0.5"
                            />
                            <div>
                              <div className="font-medium">{t("createInbound.argoTemp")}</div>
                              <div className="text-black/50 dark:text-white/50">
                                {t("createInbound.argoTempHint")}
                              </div>
                            </div>
                          </label>
                          <label
                            className={`flex items-start gap-2 ${
                              argoLocked ? "opacity-50 cursor-not-allowed" : "cursor-pointer"
                            }`}
                          >
                            <input
                              type="radio"
                              name={`argo-${id}`}
                              checked={sel.argoNamed}
                              disabled={argoLocked}
                              onChange={() => patch(id, { argoNamed: true, argoTemp: false })}
                              className="mt-0.5"
                            />
                            <div className="flex-1">
                              <div className="font-medium">{t("createInbound.argoNamed")}</div>
                              <div className="text-black/50 dark:text-white/50 mb-1">
                                {t("createInbound.argoNamedHint")}
                              </div>
                              {sel.argoNamed && (
                                <div>
                                  <input
                                    type="text"
                                    placeholder={t("createInbound.argoTokenPlaceholder")}
                                    value={sel.argoToken}
                                    onChange={(e) => patch(id, { argoToken: e.target.value })}
                                    className="rounded-md bg-black/[0.05] dark:bg-white/[0.04] border border-black/10 dark:border-white/15 px-2 py-1 text-xs w-full"
                                  />
                                  {!sel.argoToken && !argoTokenReady && (
                                    <div className="mt-1 text-amber-600 dark:text-amber-300 text-[11px]">
                                      {t("createInbound.argoTokenMissingBody")}{" "}
                                      <Link to="/inbound?tab=argo" className="underline">
                                        {t("createInbound.argoTokenMissingCta")} →
                                      </Link>
                                    </div>
                                  )}
                                </div>
                              )}
                            </div>
                          </label>
                        </div>
                      )}
                      <AccelStatusNote
                        port={sel.port || defaultsByID[id]}
                        cdnOn={!!sel.cdn}
                        argoOn={!!sel.argoTemp || !!sel.argoNamed}
                        inWizard
                      />
                    </div>
                  )}
                  {m.cdn === "no" && (
                    <div className="pl-2 text-black/50 dark:text-white/50">
                      {t("createInbound.accelNotApplicable")}
                    </div>
                  )}
                  </div>
                </div>
              )}
            </div>
          );
        })}
      </div>
      {err && <ErrorText>{err.message}</ErrorText>}
      <div className="flex justify-between mt-4">
        <Button variant="ghost" onClick={onBack}>
          {t("createInbound.back")}
        </Button>
        <Button
          variant="primary"
          onClick={onCreate}
          disabled={selections.size === 0 || creating}
        >
          {creating ? t("common.loading") : t("createInbound.create")}
        </Button>
      </div>
    </Card>
  );
}

/* ───────────────────────── Shared result panel ───────────────────────── */

// CertStatusBanner reflects how the batch's TLS-cert protocols were certified.
// "none" (no TLS-cert protocol) renders nothing; "acme" is a success badge;
// "self-signed" is neutral unless a cert_error explains an ACME fallback, in
// which case it warns and surfaces the reason + a pointer to the Certs page.
function CertStatusBanner({ result, t }: { result: FunnelResult; t: TFunction }) {
  const mode = result.cert_mode;
  if (!mode || mode === "none") return null;
  if (mode === "acme") {
    return (
      <div className="rounded-lg border border-emerald-500/50 bg-emerald-500/10 p-3 text-sm text-emerald-600 dark:text-emerald-300">
        {t("createInbound.certAcme", { domain: result.cert_domain || "" })}
      </div>
    );
  }
  // self-signed
  if (result.cert_error) {
    return (
      <div className="rounded-lg border border-amber-500/50 bg-amber-500/10 p-3 text-sm text-amber-600 dark:text-amber-300">
        <div className="font-medium mb-1">{t("createInbound.certFellBack")}</div>
        <div className="text-xs opacity-80 font-mono break-all">{result.cert_error}</div>
      </div>
    );
  }
  return (
    <div className="rounded-lg border border-black/20 dark:border-white/20 bg-black/5 dark:bg-white/5 p-3 text-sm text-black/60 dark:text-white/60">
      {t("createInbound.certSelfSigned")}
    </div>
  );
}

// ArgoAutoStart fires the tunnel the moment the result page renders (the
// inbound + argo config already exist), then polls /argo/status for live
// feedback. The first launch downloads cloudflared (~30 MB) and waits up to
// ~30s to capture the trycloudflare hostname, so a synchronous start inside
// create-funnel would hang the wizard — instead we start here and show
// starting → running(hostname) / failed(reason). local_port is omitted; the
// backend binds to the single argo_bound inbound. Recovery (stop/restart) lives
// on the Access-optimization page.
interface ArgoStatusLite {
  state: "idle" | "starting" | "running" | "failed";
  hostname?: string;
  error?: string;
}
function ArgoAutoStart({ t }: { t: TFunction }) {
  const startedRef = useRef(false);
  const start = useMutation({
    mutationFn: () => call<ArgoStatusLite>(api.post("/argo/start", {})),
  });
  const status = useQuery({
    queryKey: ["argo-status"],
    queryFn: () => call<ArgoStatusLite>(api.get("/argo/status")),
    refetchInterval: 2500,
  });
  useEffect(() => {
    if (!startedRef.current) {
      startedRef.current = true;
      start.mutate();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const state = status.data?.state ?? (start.isPending ? "starting" : "idle");
  const hostname = status.data?.hostname;
  const errMsg =
    status.data?.error ||
    (start.error as { response?: { data?: { error?: { message?: string } } } } | null)
      ?.response?.data?.error?.message ||
    (start.error as Error | null)?.message;
  // Don't flash "failed" while the process is still coming up (starting); only a
  // terminal failed state, or a start call that errored before launch, counts.
  const failed =
    state === "failed" ||
    (start.isError && state !== "running" && state !== "starting");

  if (failed) {
    return (
      <div className="rounded-md border border-rose-400/50 bg-rose-500/10 px-3 py-2.5 text-sm text-rose-700 dark:text-rose-200">
        <div className="font-medium">{t("createInbound.argoStartFailed")}</div>
        {errMsg && <div className="mt-1 text-xs opacity-90">{errMsg}</div>}
        <Link to="/inbound?tab=argo" className="underline mt-1 inline-block">
          {t("createInbound.argoResultGateCta")} →
        </Link>
      </div>
    );
  }
  if (state === "running" && hostname) {
    return (
      <div className="rounded-md border border-emerald-400/50 bg-emerald-500/10 px-3 py-2.5 text-sm text-emerald-700 dark:text-emerald-200">
        <div className="font-medium">{t("createInbound.argoStartOk")}</div>
        <code className="mt-1 inline-block rounded bg-black/30 px-2 py-0.5 text-xs">
          {hostname}
        </code>
        <div className="mt-1 text-xs opacity-90">{t("createInbound.argoStartOkHint")}</div>
      </div>
    );
  }
  return (
    <div className="rounded-md border border-amber-400/40 bg-amber-400/10 px-3 py-2.5 text-sm text-amber-700 dark:text-amber-200">
      <div className="font-medium">{t("createInbound.argoStarting")}</div>
      <div className="mt-1 text-xs opacity-90">{t("createInbound.argoStartingHint")}</div>
    </div>
  );
}

function ResultPanel({
  result,
  ports,
  panelPort,
  protocols,
  onAnother,
  onBack,
  t,
}: {
  result: FunnelResult;
  ports?: PortsReservedSnapshot;
  panelPort: number;
  protocols: ProtoSelection[];
  onAnother: () => void;
  onBack: () => void;
  t: TFunction;
}) {
  const [fmt, setFmt] = useState<SubFormat>("v2ray");
  // build the absolute subscription URL using the operator's chosen
  // host (from result.host) so the URL family matches the inbound family.
  // window.location.origin would always echo the panel-access host, which is
  // wrong when the operator picked a different family from the one they
  // logged into the panel with.
  const subscriptionBase = useMemo(() => {
    if (result.host && panelPort > 0) {
      const isV6 = result.host.includes(":");
      const hostSeg = isV6 ? `[${result.host}]` : result.host;
      return `http://${hostSeg}:${panelPort}${result.subscription_url}`;
    }
    return result.subscription_url;
  }, [result.host, result.subscription_url, panelPort]);
  const fullUrl = subUrlForFmt(subscriptionBase, fmt);
  // The nc one-liners in the port-verify checklist must target the SAME IP
  // the operator picked in Step 1 — that's the IP the wizard actually bound
  // the new inbounds to. window.location.hostname is just whatever IP the
  // operator typed into their browser to reach the panel (often the other
  // family on a dual-stack node), so an inbound bound on v6 was getting an
  // nc command pointed at v4 → the checklist would falsely say "port is
  // closed" when the port was actually open on the right family. Use
  // result.host (the operator's Step-1 pick) and fall back only when the
  // backend didn't report one (legacy code path / very old client cache).
  const vpsHost = useMemo(() => {
    if (result.host) {
      return result.host;
    }
    if (typeof window !== "undefined") {
      return window.location.hostname || "your-vps-ip";
    }
    return "your-vps-ip";
  }, [result.host]);
  // Land at the top so the Argo tunnel status (and cert banner) are the first
  // thing seen — the result content is long, and the browser otherwise keeps
  // the wizard's prior scroll position, burying the live status off-screen.
  useEffect(() => {
    window.scrollTo({ top: 0 });
  }, []);
  const selectedFmtLabel = t(
    `inbounds.subFormatDesc.${SUB_FORMATS.find((f) => f.fmt === fmt)?.i18nKey ?? "v2ray"}`,
  );
  return (
    <Card title={t("createInbound.step4Title")}>
      <div className="text-xs text-black/50 dark:text-white/50 mb-3">
        {t("createInbound.step4Hint", { count: SUB_FORMATS.length })}
      </div>
      <div className="space-y-4">
        <CertStatusBanner result={result} t={t} />
        {protocols.some((p) => p.argoTemp || p.argoNamed) && (
          <ArgoAutoStart t={t} />
        )}

        <PortVerifyChecklist
          inbounds={result.inbounds}
          host={vpsHost}
          t={t}
        />

        <div className="grid md:grid-cols-3 gap-4">
          <div className="md:col-span-2 space-y-2">
            <Field label={t("createInbound.subFormat")}>
              <Select value={fmt} onChange={(e) => setFmt(e.target.value as SubFormat)}>
                {SUB_FORMATS.map((f) => (
                  <option key={f.fmt} value={f.fmt}>
                    {t(`inbounds.subFormatShort.${f.i18nKey}`)}
                  </option>
                ))}
              </Select>
            </Field>
            {/* Only the SELECTED format's applicable-clients + notes — the old
                grid that listed all 7 at once duplicated the dropdown. */}
            <div className="rounded-md border border-black/10 dark:border-white/10 bg-black/[0.02] dark:bg-white/[0.03] px-3 py-2 text-xs text-black/70 dark:text-white/70">
              {selectedFmtLabel}
            </div>
            <Field label={t("createInbound.subUrl")}>
              <div className="flex items-center gap-2">
                <div className="flex-1 min-w-0">
                  <Input value={fullUrl} readOnly onFocus={(e) => e.target.select()} />
                </div>
                <CopyButton
                  variant="primary"
                  text={fullUrl}
                  label={t("createInbound.copy")}
                />
              </div>
            </Field>
            <div className="rounded-md border border-sky-500/40 bg-sky-500/10 px-3 py-2 text-xs text-sky-700 dark:text-sky-200">
              {t("createInbound.subInsecureHint")}
            </div>
          </div>
          <div className="flex flex-col items-center gap-1">
            {/* Standard QR: dark modules on a SOLID light background with a
                quiet zone. The old transparent-bg + currentColor render came
                out as a light-on-dark inverted code on the dark theme, which
                scanners (NekoBox etc.) reject — copy-link worked but scan
                didn't. Match the Subscriptions page QR (white box, default
                black-on-white). Confirmed on-device 2026-06-13. */}
            <div className="bg-white rounded-lg p-3">
              <QRCodeSVG value={fullUrl} size={168} level="M" />
            </div>
            <div className="text-xs text-black/50 dark:text-white/50">{fmt}</div>
          </div>
        </div>
        <div className="pt-3 border-t border-black/10 dark:border-white/10 flex justify-between">
          <Button variant="ghost" onClick={onBack}>
            {t("createInbound.back")}
          </Button>
          <Button onClick={onAnother}>{t("createInbound.createAnother")}</Button>
        </div>
      </div>
    </Card>
  );
}

function PortVerifyChecklist({
  inbounds,
  host,
  t,
}: {
  inbounds: FunnelInbound[];
  host: string;
  t: TFunction;
}) {
  const [copiedCmdId, setCopiedCmdId] = useState<number | null>(null);
  // IPv6 hosts need different shaping for the two consumers below:
  //   - nc takes the literal as a bare arg, but some BSD nc builds need
  //     the explicit -6 flag to pick the v6 resolver path. Adding -6 on
  //     a v4 literal would error, so gate it on host shape.
  //   - check-host.net's ?host=... query parses the host:port boundary by
  //     the LAST colon, so a v6 literal must be bracketed: [2607::2]:8443.
  const isV6 = host.includes(":");
  function isUDP(backend: string) {
    return backend === "hysteria2" || backend === "tuic";
  }
  function ncLine(backend: string, port: number) {
    const family = isV6 ? "-6 " : "";
    return isUDP(backend)
      ? `nc ${family}-u -zv ${host} ${port}`
      : `nc ${family}-zv ${host} ${port}`;
  }
  function checkHostUrl(backend: string, port: number) {
    const flavor = isUDP(backend) ? "check-udp" : "check-tcp";
    const hostSeg = isV6 ? `[${host}]` : host;
    return `https://check-host.net/${flavor}?host=${hostSeg}:${port}`;
  }
  return (
    <div className="rounded-lg border border-amber-500/40 bg-amber-500/[0.04] p-3">
      <div className="text-sm font-semibold text-amber-700 dark:text-amber-300 mb-1">
        ⚠️ {t("createInbound.verifyTitle")}
      </div>
      <div className="text-xs text-black/70 dark:text-white/70 mb-3 whitespace-pre-line">
        {t("createInbound.verifyHint")}
      </div>
      <div className="grid sm:grid-cols-2 gap-2 text-xs">
        {inbounds.map((i) => {
          const cmd = ncLine(i.backend, i.port);
          const url = checkHostUrl(i.backend, i.port);
          return (
            <div key={i.id} className="rounded border border-black/10 dark:border-white/10 p-2">
              <div className="font-medium mb-1">
                {i.ui_type}{" "}
                <span className="text-black/50 dark:text-white/50">
                  ({isUDP(i.backend) ? "UDP" : "TCP"} :{i.port})
                </span>
              </div>
              <div className="flex items-center gap-2 mb-1">
                <code className="flex-1 rounded bg-black/40 text-emerald-300 px-2 py-1 overflow-x-auto">
                  {cmd}
                </code>
                <Button
                  variant={copiedCmdId === i.id ? "primary" : "ghost"}
                  onClick={async () => {
                    const ok = await copyText(cmd);
                    if (ok) {
                      setCopiedCmdId(i.id);
                      window.setTimeout(() => setCopiedCmdId(null), 1500);
                    }
                  }}
                >
                  {copiedCmdId === i.id
                    ? t("inbounds.copied")
                    : t("createInbound.copy")}
                </Button>
              </div>
              <a
                href={url}
                target="_blank"
                rel="noreferrer"
                className="text-blue-500 dark:text-blue-300 underline"
              >
                {t("createInbound.openCheckHost")} ↗
              </a>
            </div>
          );
        })}
      </div>
      <div className="mt-3 text-xs text-black/70 dark:text-white/70">
        {t("createInbound.changePortHint")}{" "}
        <Link to="/inbounds" className="underline text-blue-500 dark:text-blue-300">
          {t("createInbound.goToInbounds")} →
        </Link>
      </div>
    </div>
  );
}

/* ───────────────────────── shared ISP-risk + Hy2-obfs UI ───────────────────────── */

// IspRiskStrip renders a colored hint strip beneath a selected protocol card
// when the protocol's PROTO_META.ispRisk is medium or high. low protocols
// render nothing (avoid noise on the common case). Background + border tint
// matches Inbounds.tsx edit-card risk strips for visual consistency.
function IspRiskStrip({ id, t }: { id: ProtoId; t: TFunction }) {
  const risk = PROTO_META[id].ispRisk;
  if (risk === "low") return null;
  const tone =
    risk === "high"
      ? "border-rose-500/40 bg-rose-500/10 text-rose-200"
      : "border-amber-500/40 bg-amber-500/10 text-amber-200";
  const key = risk === "high" ? "createInbound.ispRiskWarn.high" : "createInbound.ispRiskWarn.medium";
  // Per-protocol override (Hy2 has a more specific message about iOS QUIC
  // speedtest behaviour). Falls back to the generic medium / high message.
  const protoKey = `createInbound.ispRiskWarn.${id}`;
  const protoMsg = t(protoKey, { defaultValue: "" });
  const msg = protoMsg || t(key);
  return (
    <div
      className={`mb-3 rounded-md border px-3 py-2 text-xs leading-relaxed ${tone}`}
    >
      {msg}
    </div>
  );
}

// Hy2ObfsAdvancedToggle: a small advanced control that surfaces only on the
// Hysteria2 card (Quick + Full + Scenario landing). Default OFF — see the
// funnel.go Hy2Defaults branch comment for the cross-impl compatibility
// background. When the operator flips it on the warning text turns red and
// names the clients that will silently drop the node.
function Hy2ObfsAdvancedToggle({
  checked,
  onChange,
  t,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  t: TFunction;
}) {
  return (
    <div className="mt-3 max-w-md">
      <Toggle
        checked={checked}
        onChange={onChange}
        label={t("createInbound.advHy2Obfs")}
      />
      <div className="mt-1 text-[11px] text-black/55 dark:text-white/55">
        {t("createInbound.advHy2ObfsHint")}
      </div>
      {checked && (
        <div className="mt-2 rounded-md border border-rose-500/40 bg-rose-500/10 px-2.5 py-1.5 text-[11px] text-rose-200">
          {t("createInbound.advHy2ObfsWarn")}
        </div>
      )}
    </div>
  );
}

// Hy2PortHopAdvanced: optional port-hopping range for the Hysteria2 card.
// Default OFF (both 0). Toggling on seeds a sane default range (20000-40000:
// high ports, above the privileged range, below the Linux ephemeral range
// 32768-60999 is impossible to fully avoid for a wide span, but this start
// keeps the bulk clear) and reveals two port inputs. The server nat-redirects
// the range to the listen port; clients spray across it to dodge UDP QoS.
function Hy2PortHopAdvanced({
  start,
  end,
  onChange,
  t,
}: {
  start: number;
  end: number;
  onChange: (start: number, end: number) => void;
  t: TFunction;
}) {
  const on = start > 0 && end >= start;
  return (
    <div className="mt-3 max-w-md">
      <Toggle
        checked={on}
        onChange={(v) => (v ? onChange(20000, 40000) : onChange(0, 0))}
        label={t("createInbound.advHy2Hop")}
      />
      <div className="mt-1 text-[11px] text-black/55 dark:text-white/55">
        {t("createInbound.advHy2HopHint")}
      </div>
      {on && (
        <div className="mt-2 flex items-center gap-2">
          <Input
            type="number"
            value={start}
            onChange={(e) => onChange(Number(e.target.value), end)}
            className="w-28"
          />
          <span className="text-black/40 dark:text-white/40">–</span>
          <Input
            type="number"
            value={end}
            onChange={(e) => onChange(start, Number(e.target.value))}
            className="w-28"
          />
        </div>
      )}
    </div>
  );
}

/* ───────────────────────── helpers ───────────────────────── */

// computeDefaultPort mirrors pickFreePort() in internal/control/wizard/funnel.go.
// Walks +1 past every port the panel already reserves / has bound / has another
// inbound on, so the form seeds the field with what the backend would have
// picked anyway. Bounded by 100 attempts to match the backend's safety cap.
function computeDefaultPort(
  id: ProtoId,
  ports?: PortsReservedSnapshot,
  family?: "v4" | "v6",
): number {
  const base = defaultPortFor(id);
  if (!ports) return base;
  // per-family occupied list, so v4 SOCKS5:1080 doesn't push the
  // v6 SOCKS5 default up to 1081 — different families on different
  // sockets, picker should treat them independently.
  const familyOccupied = family && ports.occupied_by_family
    ? ports.occupied_by_family[family] ?? []
    : ports.occupied;
  const taken = new Set<number>([
    ...ports.reserved,
    ...familyOccupied,
    ports.panel_port,
  ]);
  let p = base;
  for (let i = 0; i < 100; i++) {
    if (!taken.has(p)) return p;
    p++;
  }
  return base;
}

// defaultPortFor mirrors uiProtoMeta in internal/control/wizard/funnel.go.
// Kept in sync by hand because the wizard backend already validates whatever
// value we send; this table just seeds the input field with the same value
// the backend would have picked for port=0.
function defaultPortFor(id: ProtoId): number {
  switch (id) {
    case "vless-reality":
      return 8443;
    case "hysteria2":
      return 41020;
    case "shadowsocks-2022":
      return 8388;
    case "vmess-ws-cdn":
      return 2053;
    case "trojan-tls":
      return 8444;
    case "vless-ws-cdn":
      return 2083;
    case "vless-xhttp-reality":
      return 8447;
    case "vless-xhttp-tls-cdn":
      return 2096;
    case "tuic-v5":
      return 50000;
    case "anytls":
      return 8445;
    case "socks5":
      return 1080;
  }
}
