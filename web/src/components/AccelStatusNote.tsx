import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";

// Cloudflare's proxyable HTTPS ports — mirrors system.CFHTTPSWhitelist (Go)
// and PortInput's FALLBACK_CFHTTPS. A cdn_mode inbound off this list can't be
// fronted by CF.
const CF_HTTPS = [443, 2053, 2083, 2087, 2096, 8443];

interface AdvancedState {
  cdn_enabled: boolean;
  cdn_preferred_ips: string[];
}
interface ArgoState {
  state: "idle" | "starting" | "running" | "failed";
}

// AccelStatusNote surfaces the silent-failure conditions for per-inbound CDN /
// Argo acceleration: opting in only takes effect when the global infrastructure
// is in place (CF-valid port, CDN pool, running tunnel). Without these the
// subscription quietly falls back to the direct VPS IP, so the operator builds
// something that doesn't do what they expect. We tell them, with a deep link.
//
// Shared by the inbound create modal and the wizard so both creation paths warn
// identically. cdnOn/argoOn are only ever true for accel-eligible protocols
// (the toggles render only there), so we trust them rather than re-checking the
// protocol id — the wizard and the backend use different id spellings.
export default function AccelStatusNote({
  port,
  cdnOn,
  argoOn,
  inWizard = false,
}: {
  port: number;
  cdnOn: boolean;
  argoOn: boolean;
  // In the create wizard the inbound doesn't exist yet, so a "go start the
  // tunnel now" deep link is a trap — it loses wizard progress and lands on a
  // page that says "create an inbound first". In that context we show an
  // informational note with no nav link; the result page carries the real CTA.
  inWizard?: boolean;
}) {
  const { t } = useTranslation();
  const { data: adv } = useQuery({
    queryKey: ["advanced"],
    queryFn: () => call<AdvancedState>(api.get("/advanced")),
    enabled: cdnOn,
  });
  const { data: argo } = useQuery({
    queryKey: ["argo-status"],
    queryFn: () => call<ArgoState>(api.get("/argo/status")),
    enabled: argoOn,
    refetchInterval: argoOn ? 4000 : false,
  });

  const notes: {
    tone: "red" | "amber";
    text: string;
    link?: boolean;
    to?: string;
  }[] = [];

  if (cdnOn) {
    if (!CF_HTTPS.includes(port)) {
      notes.push({
        tone: "red",
        text: t("accel.cdnPortBad", { port, ports: CF_HTTPS.join(" / ") }),
        to: "/inbound?tab=cdn",
      });
    } else if (adv && !adv.cdn_enabled) {
      notes.push({ tone: "amber", text: t("accel.cdnGlobalOff"), to: "/inbound?tab=cdn" });
    } else if (adv && (adv.cdn_preferred_ips?.length ?? 0) === 0) {
      notes.push({ tone: "amber", text: t("accel.cdnPoolEmpty"), to: "/inbound?tab=cdn" });
    }
  }
  if (argoOn && (inWizard || (argo && argo.state !== "running"))) {
    if (inWizard) {
      // Mid-wizard: the inbound isn't created yet. Inform, don't deep-link.
      notes.push({ tone: "amber", text: t("accel.argoWizardNote"), link: false });
    } else {
      const known = ["idle", "starting", "running", "failed"];
      const stateLabel = t(
        `accel.tunnelState.${known.includes(argo!.state) ? argo!.state : "unknown"}`,
      );
      notes.push({
        tone: "amber",
        text: t("accel.argoTunnelDown", { state: stateLabel }),
        to: "/inbound?tab=argo",
      });
    }
  }

  if (notes.length === 0) return null;

  return (
    <div className="col-span-2 space-y-2">
      {notes.map((n, i) => (
        <div
          key={i}
          className={
            "flex flex-wrap items-center gap-2 rounded-md border px-3 py-2 text-[11px] " +
            (n.tone === "red"
              ? "border-red-500/40 bg-red-500/10 text-red-300"
              : "border-amber-500/40 bg-amber-500/10 text-amber-300")
          }
        >
          <span className="grow">{n.text}</span>
          {n.link !== false && (
            <Link
              to={n.to ?? "/inbound?tab=cdn"}
              className="underline underline-offset-2 hover:opacity-80 whitespace-nowrap"
            >
              {t("accel.relayLink")}
            </Link>
          )}
        </div>
      ))}
    </div>
  );
}
