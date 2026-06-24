// Protocol Guide — sidebar item 1.
//
// Static educational page introducing every inbound protocol EdgeNest can
// serve. The page is purely informational: no API calls, no mutation. The
// goal is "in under a minute the operator decides which protocol to
// provision in the Wizard". Layout:
//   1. Main table — 11 protocols × 8 columns (name / popularity / OS /
//      brief / domain / cdn / argo / detail button).
//   2. Legend strip — symbol meanings (✓ ✗ △ n/a).
//   3. Three concept cards — CDN, Argo, WARP, each with a full Cloudflare
//      tutorial inline (no external links — operators reading on a phone in
//      a low-trust network shouldn't have to leave the panel).
//   4. Per-protocol detail modal opened by the "详情" buttons.
//
// All display strings come from i18n (zh+en mirrored). The compat matrices
// and accel flags live in lib/protocolMeta.ts.

import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";

import Layout from "../components/Layout";
import { Badge, Button, Card, Modal, PageHeader } from "../components/ui";
import TopologyDiagram, { type TopologyKind } from "../components/TopologyDiagram";
import {
  CLIENT_IDS,
  COMPAT_MATRIX,
  OS_LIST,
  PROTO_IDS,
  PROTO_META,
  clientsForProto,
  osSupported,
  type ProtoId,
} from "../lib/protocolMeta";

const TIER_STARS: Record<string, string> = {
  main5: "★★★★★",
  main4: "★★★★",
  advanced: "★★★",
  experimental: "★★",
  tool: "★",
};

// Engine-grouped transport spec, merged in from the former /protocols page.
// Pure reference: which protocols each engine serves + their transport.
interface SpecProto {
  name: string;
  transport: string;
  notes: string;
}
const SPEC_SINGBOX: SpecProto[] = [
  { name: "VLESS-Reality", transport: "TCP", notes: "XTLS Vision + Reality handshake, no real cert needed" },
  { name: "VLESS-WS", transport: "TCP", notes: "WebSocket transport, optional TLS, CDN-friendly" },
  { name: "VMess-WS", transport: "TCP", notes: "Legacy V2Ray protocol over WebSocket" },
  { name: "Trojan", transport: "TCP/TLS", notes: "TLS pass-through, looks like HTTPS" },
  { name: "Hysteria2", transport: "UDP/QUIC", notes: "Brutal congestion control, optional salamander obfs" },
  { name: "TUIC v5", transport: "UDP/QUIC", notes: "Multiplexed UDP with BBR congestion control" },
  { name: "Shadowsocks-2022", transport: "TCP/UDP", notes: "AEAD-2022 ciphers (blake3-aes-128/256-gcm, chacha20)" },
  { name: "AnyTLS", transport: "TCP/TLS", notes: "Native sing-box v1.12+ inbound; xray mainline has no AnyTLS" },
  { name: "SOCKS5", transport: "TCP", notes: "Plain SOCKS5 — for LAN use, no encryption" },
];
const SPEC_XRAY: SpecProto[] = [
  { name: "VLESS-XHTTP-Reality", transport: "TCP", notes: "XHTTP transport + Reality handshake, no real cert" },
  { name: "VLESS-XHTTP-TLS", transport: "TCP", notes: "XHTTP transport over real TLS cert" },
  { name: "VLESS-XHTTP-CDN", transport: "TCP", notes: "XHTTP transport, no encryption — for CDN edge offload" },
];

const PROTO_TO_TOPOLOGY: Record<ProtoId, TopologyKind> = {
  "vless-reality": "vless-reality",
  hysteria2: "hysteria2",
  "shadowsocks-2022": "shadowsocks-2022",
  "vmess-ws-cdn": "vmess-ws-cdn",
  "trojan-tls": "trojan-tls",
  "vless-ws-cdn": "vless-ws-cdn",
  "vless-xhttp-reality": "vless-xhttp-reality",
  "vless-xhttp-tls-cdn": "vless-xhttp-tls-cdn",
  "tuic-v5": "tuic-v5",
  anytls: "anytls",
  socks5: "socks5",
};

export default function ProtocolGuidePage() {
  const { t } = useTranslation();
  const [openDetail, setOpenDetail] = useState<ProtoId | null>(null);
  return (
    <Layout>
      <PageHeader title={t("guide.title")} subtitle={t("guide.subtitle")} />
      <div className="grid gap-6">
        <Card>
          <MainTable onDetail={setOpenDetail} t={t} />
          <Legend t={t} />
        </Card>
        <Card title={t("guide.specTitle")}>
          <p className="text-xs text-black/50 dark:text-white/50 mb-4">
            {t("protocols.subtitle")}
          </p>
          <div className="grid gap-5">
            <SpecGroup title={t("protocols.engineSingbox")} list={SPEC_SINGBOX} t={t} />
            <SpecGroup
              title={t("protocols.engineXray")}
              list={SPEC_XRAY}
              badge={<Badge tone="warn">optional</Badge>}
              t={t}
            />
          </div>
        </Card>
        <ConceptCard
          tone="cdn"
          title={t("guide.cdn.title")}
          desc={t("guide.cdn.desc")}
          prereq={t("guide.cdn.prereq")}
          supported={t("guide.cdn.supported")}
          ports={t("guide.cdn.ports")}
          tutorialTitle={t("guide.cdn.tutorialTitle")}
          tutorialSteps={[
            t("guide.cdn.t1"),
            t("guide.cdn.t2"),
            t("guide.cdn.t3"),
            t("guide.cdn.t4"),
            t("guide.cdn.t5"),
            t("guide.cdn.t6"),
          ]}
          topology="cdn"
          t={t}
        />
        <ConceptCard
          tone="argo"
          title={t("guide.argo.title")}
          desc={t("guide.argo.desc")}
          prereq={t("guide.argo.prereq")}
          supported={t("guide.argo.supported")}
          tutorialTitle={t("guide.argo.tutorialTitle")}
          tutorialSteps={[
            t("guide.argo.t1"),
            t("guide.argo.t2"),
            t("guide.argo.t3"),
            t("guide.argo.t4"),
            t("guide.argo.t5"),
            t("guide.argo.t6"),
            t("guide.argo.t7"),
          ]}
          topology="argo"
          t={t}
        />
        <ConceptCard
          tone="warp"
          title={t("guide.warp.title")}
          desc={t("guide.warp.desc")}
          prereq={t("guide.warp.typical")}
          supported={t("guide.warp.note")}
          tutorialTitle={t("guide.warp.tutorialTitle")}
          tutorialSteps={[
            t("guide.warp.t1"),
            t("guide.warp.t2"),
            t("guide.warp.t3"),
            t("guide.warp.t4"),
          ]}
          topology="warp"
          t={t}
        />
      </div>
      {openDetail && (
        <DetailModal id={openDetail} onClose={() => setOpenDetail(null)} t={t} />
      )}
    </Layout>
  );
}

function MainTable({
  onDetail,
  t,
}: {
  onDetail: (id: ProtoId) => void;
  t: TFunction;
}) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm border-collapse">
        <thead>
          <tr className="text-left text-black/60 dark:text-white/60 text-xs uppercase tracking-wide border-b border-black/10 dark:border-white/10">
            <th className="py-2 pr-3">{t("guide.col.proto")}</th>
            <th className="py-2 pr-3">{t("guide.col.popularity")}</th>
            <th className="py-2 pr-3">{t("guide.col.brief")}</th>
            <th className="py-2 pr-3 text-center">{t("guide.col.domain")}</th>
            <th className="py-2 pr-3 text-center">CDN</th>
            <th className="py-2 pr-3 text-center">Argo</th>
            <th className="py-2 pr-3 text-center">{t("guide.col.ispRisk")}</th>
            <th className="py-2 pr-3 text-center">{t("guide.col.detail")}</th>
          </tr>
        </thead>
        <tbody>
          {PROTO_IDS.map((id) => {
            const m = PROTO_META[id];
            return (
              <tr
                key={id}
                className="border-b border-black/5 dark:border-white/5 align-top"
              >
                <td className="py-2 pr-3 font-mono text-black/90 dark:text-white/90">
                  {t(`guide.proto.${id}.name`)}
                </td>
                <td className="py-2 pr-3 whitespace-nowrap">
                  <span className="text-amber-500">{TIER_STARS[m.tier]}</span>
                  <span className="ml-1 text-black/60 dark:text-white/60">
                    {t(`guide.tier.${m.tier}`)}
                  </span>
                </td>
                <td className="py-2 pr-3 text-black/80 dark:text-white/80">
                  {t(`guide.proto.${id}.brief`)}
                </td>
                <td className="py-2 pr-3 text-center">{domainSymbol(m.domain)}</td>
                <td className="py-2 pr-3 text-center">{accelSymbol(m.cdn)}</td>
                <td className="py-2 pr-3 text-center">{accelSymbol(m.argo)}</td>
                <td className="py-2 pr-3 text-center">
                  <span title={t(`guide.ispRiskHint.${m.ispRisk}`) as string}>
                    {ispRiskSymbol(m.ispRisk)}
                  </span>
                </td>
                <td className="py-2 pr-3 text-center">
                  <Button variant="ghost" onClick={() => onDetail(id)}>
                    {t("guide.col.detail")}
                  </Button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function SpecGroup({
  title,
  list,
  badge,
  t,
}: {
  title: string;
  list: SpecProto[];
  badge?: React.ReactNode;
  t: TFunction;
}) {
  return (
    <div>
      <div className="flex items-center gap-2 mb-2">
        <h3 className="text-sm font-semibold text-black/80 dark:text-white/80">{title}</h3>
        {badge}
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-black/50 dark:text-white/50 text-xs uppercase tracking-wide border-b border-black/10 dark:border-white/10">
              <th className="py-2 pr-4">Name</th>
              <th className="py-2 pr-4">{t("protocols.transport")}</th>
              <th className="py-2 pr-4">{t("protocols.notes")}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-black/5 dark:divide-white/5">
            {list.map((p) => (
              <tr key={p.name}>
                <td className="py-2 pr-4 font-mono text-black/90 dark:text-white/90 whitespace-nowrap">
                  {p.name}
                </td>
                <td className="py-2 pr-4 text-black/70 dark:text-white/70 whitespace-nowrap">
                  {p.transport}
                </td>
                <td className="py-2 pr-4 text-black/60 dark:text-white/60">{p.notes}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function Legend({ t }: { t: TFunction }) {
  return (
    <div className="mt-4 text-xs text-black/60 dark:text-white/60 space-y-1 border-t border-black/10 dark:border-white/10 pt-3">
      <div>
        <span className="text-emerald-500 font-bold mr-1">✓</span>
        {t("guide.legend.checkDomain")}
      </div>
      <div>
        <span className="text-black/40 dark:text-white/40 font-bold mr-1">✗</span>
        {t("guide.legend.noDomain")}
      </div>
      <div>
        <span className="text-amber-500 font-bold mr-1">△</span>
        {t("guide.legend.advisoryDomain")}
      </div>
      <div>
        <span className="text-black/40 dark:text-white/40 mr-1">n/a</span>
        {t("guide.legend.na")}
      </div>
      <div>
        <span className="mr-1">🟢</span>
        {t("guide.legend.ispRiskLow")}
      </div>
      <div>
        <span className="mr-1">🟡</span>
        {t("guide.legend.ispRiskMedium")}
      </div>
      <div>
        <span className="mr-1">🔴</span>
        {t("guide.legend.ispRiskHigh")}
      </div>
      <div className="pt-1 text-black/50 dark:text-white/50">
        {t("guide.popularityAsOf")}
      </div>
    </div>
  );
}

function ispRiskSymbol(r: "low" | "medium" | "high") {
  switch (r) {
    case "high":
      return <span>🔴</span>;
    case "medium":
      return <span>🟡</span>;
    default:
      return <span>🟢</span>;
  }
}

function domainSymbol(d: "none" | "required" | "advisory") {
  switch (d) {
    case "required":
      return <span className="text-emerald-500 font-bold">✓</span>;
    case "advisory":
      return <span className="text-amber-500 font-bold">△</span>;
    default:
      return <span className="text-black/40 dark:text-white/40 font-bold">✗</span>;
  }
}

function accelSymbol(a: "yes" | "no" | "na") {
  switch (a) {
    case "yes":
      return <span className="text-emerald-500 font-bold">✓</span>;
    case "na":
      return <span className="text-black/40 dark:text-white/40">n/a</span>;
    default:
      return <span className="text-black/40 dark:text-white/40 font-bold">✗</span>;
  }
}

function DetailModal({
  id,
  onClose,
  t,
}: {
  id: ProtoId;
  onClose: () => void;
  t: TFunction;
}) {
  const clients = clientsForProto(id);
  const unsupportedNote = t(`guide.proto.${id}.compatNote`);
  return (
    <Modal
      open
      onClose={onClose}
      title={t(`guide.proto.${id}.name`)}
      size="xl"
      footer={null}
    >
      <div className="space-y-4">
        <Section title={t("guide.detail.principle")}>
          <p className="text-sm leading-relaxed text-black/80 dark:text-white/80 whitespace-pre-line">
            {t(`guide.proto.${id}.principle`)}
          </p>
        </Section>
        <Section title={t("guide.detail.topology")}>
          <TopologyDiagram kind={PROTO_TO_TOPOLOGY[id]} t={t} />
        </Section>
        <Section title={t("guide.detail.matrix")}>
          <CompatTable id={id} clients={clients} t={t} />
          {unsupportedNote && unsupportedNote !== `guide.proto.${id}.compatNote` && (
            <p className="mt-2 text-xs text-black/50 dark:text-white/50">
              {unsupportedNote}
            </p>
          )}
        </Section>
        <div className="grid md:grid-cols-3 gap-3">
          <BulletBox title={t("guide.detail.pros")} text={t(`guide.proto.${id}.pros`)} tone="ok" />
          <BulletBox title={t("guide.detail.cons")} text={t(`guide.proto.${id}.cons`)} tone="warn" />
          <BulletBox title={t("guide.detail.scenarios")} text={t(`guide.proto.${id}.scenarios`)} tone="info" />
        </div>
      </div>
    </Modal>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="text-xs uppercase tracking-wide text-black/50 dark:text-white/50 mb-1">
        {title}
      </div>
      {children}
    </div>
  );
}

function CompatTable({
  id,
  clients,
  t,
}: {
  id: ProtoId;
  clients: ReturnType<typeof clientsForProto>;
  t: TFunction;
}) {
  return (
    <div className="overflow-x-auto">
      <table className="text-xs border-collapse">
        <thead>
          <tr>
            <th className="text-left py-1 pr-3 text-black/60 dark:text-white/60"></th>
            {OS_LIST.map((os) => (
              <th key={os} className="px-2 py-1 text-black/60 dark:text-white/60">
                {t(`guide.os.${os}`)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {clients.map((c) => {
            const compat = COMPAT_MATRIX[id][c];
            return (
              <tr key={c} className="border-t border-black/5 dark:border-white/5">
                <td className="py-1 pr-3 text-black/80 dark:text-white/80">
                  {t(`guide.client.${c}`)}
                </td>
                {OS_LIST.map((os) => (
                  <td key={os} className="px-2 py-1 text-center">
                    {osSupported(compat, os) ? (
                      <span className="text-emerald-500">✓</span>
                    ) : (
                      <span className="text-black/30 dark:text-white/30">—</span>
                    )}
                  </td>
                ))}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function BulletBox({ title, text, tone }: { title: string; text: string; tone: "ok" | "warn" | "info" }) {
  const colors = {
    ok: "border-emerald-500/40 bg-emerald-500/5",
    warn: "border-amber-500/40 bg-amber-500/5",
    info: "border-blue-500/40 bg-blue-500/5",
  } as const;
  return (
    <div className={`rounded-lg border ${colors[tone]} p-3`}>
      <div className="text-xs font-semibold mb-1 text-black/80 dark:text-white/80">{title}</div>
      <ul className="text-xs space-y-0.5 text-black/70 dark:text-white/70 list-disc list-inside">
        {text.split("·").map((s, i) => s.trim()).filter(Boolean).map((s, i) => (
          <li key={i}>{s}</li>
        ))}
      </ul>
    </div>
  );
}

function ConceptCard({
  tone,
  title,
  desc,
  prereq,
  supported,
  ports,
  tutorialTitle,
  tutorialSteps,
  topology,
  t,
}: {
  tone: "cdn" | "argo" | "warp";
  title: string;
  desc: string;
  prereq: string;
  supported: string;
  // ports: optional highlighted callout for the port constraint (CDN only) —
  // drawn in colour so it stands out from the muted body text.
  ports?: string;
  tutorialTitle: string;
  tutorialSteps: string[];
  topology: TopologyKind;
  t: TFunction;
}) {
  const toneClasses: Record<typeof tone, string> = {
    cdn: "border-blue-500/40 bg-blue-500/[0.04]",
    argo: "border-orange-500/40 bg-orange-500/[0.04]",
    warp: "border-emerald-500/40 bg-emerald-500/[0.04]",
  };
  return (
    <div className={`rounded-lg border ${toneClasses[tone]} p-4`}>
      <h3 className="text-base font-semibold mb-2 text-black/90 dark:text-white/90">{title}</h3>
      <div className="grid md:grid-cols-2 gap-4">
        <div className="space-y-3 text-sm">
          <Para label={t("guide.detail.whatIs")} text={desc} />
          <Para label={t("guide.detail.prereq")} text={prereq} />
          <Para label={t("guide.detail.supported")} text={supported} />
          {ports && (
            <div className="rounded-md border border-blue-400/40 bg-blue-500/10 px-3 py-2 text-xs text-blue-300 whitespace-pre-line">
              ⚠ {ports}
            </div>
          )}
        </div>
        <TopologyDiagram kind={topology} t={t} />
      </div>
      <div className="mt-4">
        <div className="text-xs uppercase tracking-wide text-black/50 dark:text-white/50 mb-2">
          {tutorialTitle}
        </div>
        <ol className="text-sm text-black/80 dark:text-white/80 space-y-1 list-decimal list-inside">
          {tutorialSteps.map((s, i) => (
            <li key={i} className="whitespace-pre-line">
              {s}
            </li>
          ))}
        </ol>
      </div>
    </div>
  );
}

function Para({ label, text }: { label: string; text: string }) {
  return (
    <div>
      <div className="text-xs uppercase tracking-wide text-black/50 dark:text-white/50 mb-0.5">
        {label}
      </div>
      <p className="text-sm text-black/80 dark:text-white/80 whitespace-pre-line">{text}</p>
    </div>
  );
}
