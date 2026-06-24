import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import Layout from "../components/Layout";
import { PageHeader } from "../components/ui";
import StatsPage from "./Stats";
import AuditPage from "./Audit";

type TabKey = "traffic" | "audit";

const TABS: { key: TabKey; labelKey: string }[] = [
  { key: "traffic", labelKey: "monitor.tabTraffic" },
  { key: "audit", labelKey: "monitor.tabAudit" },
];

// Monitor unifies the per-user traffic/quota overview and the audit log onto one
// page. The legacy /audit route redirects here with ?tab=audit so old bookmarks
// land on the right tab.
export default function MonitorPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();

  const requested = searchParams.get("tab") as TabKey | null;
  const active: TabKey =
    requested && TABS.some((tb) => tb.key === requested) ? requested : "traffic";

  const selectTab = (key: TabKey) => {
    const next = new URLSearchParams(searchParams);
    next.set("tab", key);
    setSearchParams(next, { replace: true });
  };

  return (
    <Layout>
      <PageHeader
        title={t("monitor.pageTitle")}
        subtitle={t("monitor.pageSubtitle")}
      />

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

      {active === "traffic" && <StatsPage embedded />}
      {active === "audit" && <AuditPage embedded />}
    </Layout>
  );
}
