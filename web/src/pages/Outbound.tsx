import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import Layout from "../components/Layout";
import { PageHeader } from "../components/ui";
import RoutesPanel from "./Routes";
import UnlockPanel from "./Unlock";
import WarpPanel from "./Warp";

type TabKey = "routes" | "detect" | "warp";

const TABS: { key: TabKey; labelKey: string }[] = [
  { key: "routes", labelKey: "outbound.tabRoutes" },
  { key: "detect", labelKey: "outbound.tabDetect" },
  { key: "warp", labelKey: "outbound.tabWarp" },
];

// Outbound is the "VPS → internet" optimization page: how this server reaches
// the outside. Routing decides which egress each destination takes, Detect
// diagnoses whether direct / WARP can unlock a service, and WARP is the egress
// relay — one workflow (diagnose → enable WARP → route to it) on one page. Tab
// is driven by ?tab=; a ?preset= deep-link from Detect lands on the WARP tab.
export default function OutboundPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();

  const requested = searchParams.get("tab") as TabKey | null;
  const active: TabKey =
    requested && TABS.some((x) => x.key === requested) ? requested : "routes";

  const selectTab = (key: TabKey) => {
    const next = new URLSearchParams(searchParams);
    next.set("tab", key);
    // Drop a deep-link preset hint when leaving the WARP tab.
    if (key !== "warp") next.delete("preset");
    setSearchParams(next, { replace: true });
  };

  return (
    <Layout>
      <PageHeader title={t("outbound.pageTitle")} subtitle={t("outbound.pageSubtitle")} />

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

      {active === "routes" && <RoutesPanel />}
      {active === "detect" && <UnlockPanel />}
      {active === "warp" && <WarpPanel />}
    </Layout>
  );
}
