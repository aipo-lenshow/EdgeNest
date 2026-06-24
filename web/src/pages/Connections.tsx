import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import Layout from "../components/Layout";
import { PageHeader } from "../components/ui";
import InboundsPage from "./Inbounds";
import SubscriptionsPage from "./Subscriptions";
import MultiUserPanel from "./MultiUser";

type TabKey = "inbounds" | "subscriptions" | "users";

const TABS: { key: TabKey; labelKey: string }[] = [
  { key: "inbounds", labelKey: "connections.tabInbounds" },
  { key: "subscriptions", labelKey: "connections.tabSubscriptions" },
  { key: "users", labelKey: "connections.tabUsers" },
];

// Connections unifies the inbound listeners, their bundled subscriptions, and
// the user-centric multi-user view onto one page. The two legacy routes
// (/inbounds, /subscriptions) redirect here with ?tab= so old bookmarks land
// on the right tab.
export default function ConnectionsPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();

  const requested = searchParams.get("tab") as TabKey | null;
  const active: TabKey =
    requested && TABS.some((tb) => tb.key === requested) ? requested : "inbounds";

  const selectTab = (key: TabKey) => {
    const next = new URLSearchParams(searchParams);
    next.set("tab", key);
    setSearchParams(next, { replace: true });
  };

  return (
    <Layout>
      <PageHeader
        title={t("connections.pageTitle")}
        subtitle={t("connections.pageSubtitle")}
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

      {active === "inbounds" && <InboundsPage embedded />}
      {active === "subscriptions" && <SubscriptionsPage embedded />}
      {active === "users" && <MultiUserPanel />}
    </Layout>
  );
}
