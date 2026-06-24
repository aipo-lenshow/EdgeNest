import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import Layout from "../components/Layout";
import { PageHeader } from "../components/ui";
import ArgoPanel from "./ArgoPanel";
import CdnPanel from "./CdnPanel";
import QuicPanel from "./QuicPanel";

type TabKey = "cdn" | "argo" | "quic";

const TABS: { key: TabKey; labelKey: string }[] = [
  { key: "cdn", labelKey: "inbound.tabCdn" },
  { key: "argo", labelKey: "inbound.tabArgo" },
  { key: "quic", labelKey: "inbound.tabQuic" },
];

// Inbound is the "client → VPS" optimization page: how clients reach this
// server. Each tab (CDN preferred-IP, Argo tunnel, QUIC hardening) owns its own
// slice of the advanced config and saves independently, so one never blocks
// another. Tab is driven by ?tab= for deep-linkable feature pages.
export default function InboundPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();

  const requested = searchParams.get("tab") as TabKey | null;
  const active: TabKey =
    requested && TABS.some((x) => x.key === requested) ? requested : "cdn";

  const selectTab = (key: TabKey) => {
    const next = new URLSearchParams(searchParams);
    next.set("tab", key);
    setSearchParams(next, { replace: true });
  };

  return (
    <Layout>
      <PageHeader title={t("inbound.pageTitle")} subtitle={t("inbound.pageSubtitle")} />

      <div className="mb-5 flex gap-1 border-b border-white/10">
        {TABS.map((tab) => (
          <button
            key={tab.key}
            type="button"
            onClick={() => selectTab(tab.key)}
            className={
              "px-4 py-2 text-sm -mb-px border-b-2 transition " +
              (active === tab.key
                ? "border-emerald-400 text-white"
                : "border-transparent text-white/50 hover:text-white/80")
            }
          >
            {t(tab.labelKey)}
          </button>
        ))}
      </div>

      {active === "cdn" && <CdnPanel />}
      {active === "argo" && <ArgoPanel />}
      {active === "quic" && <QuicPanel />}
    </Layout>
  );
}
