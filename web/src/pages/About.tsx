import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Layout from "../components/Layout";
import { Button, Card, ConfirmDialog, PageHeader } from "../components/ui";
import { BrandLogo, GithubIcon } from "../components/icons";
import { api, call } from "../api/client";
import { APP_VERSION_LABEL } from "../lib/version";

const GITHUB_URL = "https://github.com/aipo-lenshow/EdgeNest";

interface HealthInfo {
  version: string;
  latest_version: string;
  update_available: boolean;
}

interface UpgradeStatus {
  state: string; // idle | running | success | rolledback | manual
  from?: string;
  to?: string;
}

interface FeatureItem {
  t: string;
  d: string;
}

// Acknowledged upstream projects EdgeNest builds on. Names are proper nouns; the
// short role text is localized. Verified against go.mod + web/package.json.
const CREDITS: { name: string; roleKey: string; url: string }[] = [
  { name: "sing-box", roleKey: "about.creditSingbox", url: "https://github.com/SagerNet/sing-box" },
  { name: "Xray-core", roleKey: "about.creditXray", url: "https://github.com/XTLS/Xray-core" },
  { name: "uTLS", roleKey: "about.creditUtls", url: "https://github.com/refraction-networking/utls" },
  { name: "wireguard-go", roleKey: "about.creditWireguard", url: "https://git.zx2c4.com/wireguard-go/" },
  { name: "lego", roleKey: "about.creditLego", url: "https://github.com/go-acme/lego" },
  { name: "cloudflared", roleKey: "about.creditCloudflared", url: "https://github.com/cloudflare/cloudflared" },
];

export default function AboutPage() {
  const { t } = useTranslation();

  // Health lives outside /api/v1; surface the cached "newer version available"
  // hint from the periodic update check (public release tag only).
  const { data: health } = useQuery({
    queryKey: ["health"],
    queryFn: () => call<HealthInfo>(api.get("/health", { baseURL: "/api" })),
  });
  // Feature lists are localized arrays of { t: label, d: description }.
  const coreItems = t("about.coreItems", { returnObjects: true }) as FeatureItem[];
  const edgeItems = t("about.edgeItems", { returnObjects: true }) as FeatureItem[];

  const updateBadge =
    health?.update_available && health.latest_version ? (
      <a
        href={`${GITHUB_URL}/releases/latest`}
        target="_blank"
        rel="noreferrer noopener"
        className="rounded-full bg-amber-500/15 px-2 py-0.5 text-xs font-medium text-amber-300 hover:bg-amber-500/25"
      >
        {t("about.updateAvailable", { version: health.latest_version })}
      </a>
    ) : null;

  return (
    <Layout>
      <PageHeader title={t("about.title")} subtitle={t("about.subtitle")} />

      <div className="grid gap-6">
        <UpgradeSection health={health} />

        <Card title={t("about.title")}>
          <div className="grid gap-4">
            <div className="flex items-center gap-4">
              <BrandLogo className="h-16 w-16 shrink-0" />
              <div className="flex items-baseline gap-2 flex-wrap">
                <span className="text-2xl font-semibold">EdgeNest</span>
                <span className="text-xs uppercase tracking-wide text-white/40">Lite</span>
                <span className="text-xs text-white/40">{APP_VERSION_LABEL}</span>
                {updateBadge}
              </div>
              <div className="ml-auto shrink-0">
                <CheckNowButton />
              </div>
            </div>

            {/* Tagline + description in an accented block so the project pitch
                stands out from plain body text. */}
            <div className="rounded-xl border-l-2 border-emerald-400/60 bg-emerald-500/[0.06] px-4 py-3">
              <p className="text-sm font-medium text-emerald-200">{t("about.tagline")}</p>
              <p className="mt-1.5 text-sm text-emerald-100/70 leading-relaxed">
                {t("about.description")}
              </p>
            </div>
          </div>
        </Card>

        <Card title={t("about.coreTitle")}>
          <p className="text-xs text-white/50 mb-3">{t("about.coreIntro")}</p>
          <FeatureList items={coreItems} />
        </Card>

        <Card title={t("about.edgeTitle")}>
          <p className="text-xs text-white/50 mb-3">{t("about.edgeIntro")}</p>
          <FeatureList items={edgeItems} accent />
        </Card>

        <Card title={t("about.projectInfo")}>
          <dl className="grid grid-cols-1 sm:grid-cols-2 gap-y-3 gap-x-6 text-sm">
            <Row label={t("about.infoAuthor")} value="AiPo" />
            <Row label={t("about.infoLicense")} value="AGPL-3.0" />
            <Row label={t("about.infoVersion")} value={`${APP_VERSION_LABEL} · Lite`} />
            <div>
              <dt className="text-xs uppercase tracking-wide text-white/50">
                {t("about.infoRepo")}
              </dt>
              <dd className="mt-0.5">
                <a
                  href={GITHUB_URL}
                  target="_blank"
                  rel="noreferrer noopener"
                  aria-label="GitHub"
                  title={GITHUB_URL}
                  className="inline-flex text-white/60 hover:text-white transition"
                >
                  <GithubIcon className="h-5 w-5" />
                </a>
              </dd>
            </div>
          </dl>
        </Card>

        <Card title={t("about.ackTitle")}>
          <p className="text-xs text-white/50 mb-3">{t("about.ackIntro")}</p>
          <ul className="grid gap-2 text-sm">
            {CREDITS.map((c) => (
              <li key={c.name} className="flex flex-wrap items-baseline gap-x-2">
                <a
                  href={c.url}
                  target="_blank"
                  rel="noreferrer noopener"
                  className="font-medium text-sky-300 hover:text-sky-200"
                >
                  {c.name}
                </a>
                <span className="text-white/50">— {t(c.roleKey)}</span>
              </li>
            ))}
          </ul>
        </Card>
      </div>
    </Layout>
  );
}

// CheckNowButton forces a live GitHub latest-release lookup (GET /version/check
// → updatecheck.StatusLive) instead of waiting up to 6h for the passive cache to
// refresh — the reason a panel that started before a release was published would
// otherwise never surface the update. On success it invalidates the cached
// health query, so the version badge, the UpgradeSection, and the sidebar dot all
// re-render from the fresh result.
function CheckNowButton() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [upToDate, setUpToDate] = useState(false);

  const check = useMutation({
    mutationFn: () => call<HealthInfo>(api.get("/version/check")),
    onSuccess: (data) => {
      void qc.invalidateQueries({ queryKey: ["health"] });
      setUpToDate(!data.update_available);
    },
  });

  return (
    <div className="flex items-center gap-2">
      <Button
        variant="ghost"
        onClick={() => {
          setUpToDate(false);
          check.mutate();
        }}
        disabled={check.isPending}
      >
        {check.isPending ? t("about.checkChecking") : t("about.checkNow")}
      </Button>
      {upToDate && !check.isPending && (
        <span className="text-xs text-white/50">{t("about.checkLatest")}</span>
      )}
      {check.isError && (
        <span className="text-xs text-rose-300">{t("about.checkError")}</span>
      )}
    </div>
  );
}

// UpgradeSection shows a one-click "upgrade to latest stable" card when the
// periodic check found a newer release. The upgrade runs detached on the server
// (it outlives the panel restart it triggers); we poll /upgrade/status — which
// fails transiently while the panel restarts, then returns the final state.
function UpgradeSection({ health }: { health?: HealthInfo }) {
  const { t } = useTranslation();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [started, setStarted] = useState(false);

  const status = useQuery({
    queryKey: ["upgrade-status"],
    queryFn: () => call<UpgradeStatus>(api.get("/upgrade/status")),
    enabled: started,
    refetchInterval: started ? 4000 : false,
    retry: false,
  });

  const upgrade = useMutation({
    mutationFn: () => call<{ started: boolean }>(api.post("/upgrade", {})),
    onSuccess: () => setStarted(true),
  });

  const state = status.data?.state ?? "";
  // The version label is baked into the JS bundle at build time, so an in-place
  // upgrade leaves the running page showing the OLD version even though the
  // service is now new. Once the upgrade reaches "success" the new service has
  // health-gated up, so a full reload pulls the new bundle (and the new version
  // label) automatically — the operator no longer has to refresh by hand. The
  // small delay lets the success message render first.
  useEffect(() => {
    if (state !== "success") return;
    const id = setTimeout(() => window.location.reload(), 2500);
    return () => clearTimeout(id);
  }, [state]);

  if (!health?.update_available || !health.latest_version) return null;

  const done = state === "success" || state === "rolledback" || state === "manual";
  if (done) {
    const cls =
      state === "success"
        ? "border-emerald-400/60 bg-emerald-500/[0.06] text-emerald-100"
        : "border-amber-400/60 bg-amber-500/[0.06] text-amber-100";
    const msg =
      state === "success"
        ? t("about.upgradeDone")
        : state === "rolledback"
          ? t("about.upgradeFailed")
          : t("about.upgradeManual");
    return (
      <Card title={t("about.upgradeTitle")}>
        <div className={`rounded-xl border-l-2 px-4 py-3 text-sm ${cls}`}>{msg}</div>
      </Card>
    );
  }

  return (
    <Card title={t("about.upgradeTitle")}>
      <div className="flex flex-wrap items-center gap-3">
        {started ? (
          <p className="text-sm text-amber-200">{t("about.upgradeRunning")}</p>
        ) : (
          <>
            <Button variant="primary" onClick={() => setConfirmOpen(true)} disabled={upgrade.isPending}>
              {t("about.upgradeButton", { version: health.latest_version })}
            </Button>
            {upgrade.isError && (
              <span className="text-sm text-rose-300">
                {t("about.upgradeError", { error: (upgrade.error as Error).message })}
              </span>
            )}
          </>
        )}
      </div>
      <ConfirmDialog
        open={confirmOpen}
        title={t("about.upgradeConfirmTitle")}
        body={t("about.upgradeConfirmBody", { version: health.latest_version })}
        variant="default"
        confirmLabel={t("about.upgradeConfirmBtn")}
        cancelLabel={t("about.upgradeCancel")}
        busy={upgrade.isPending}
        onConfirm={() => {
          setConfirmOpen(false);
          upgrade.mutate();
        }}
        onCancel={() => setConfirmOpen(false)}
      />
    </Card>
  );
}

function FeatureList({ items, accent }: { items: FeatureItem[]; accent?: boolean }) {
  return (
    <ul className="grid gap-3 sm:grid-cols-2">
      {items.map((it) => (
        <li
          key={it.t}
          className="rounded-lg border border-white/5 bg-white/[0.02] px-3 py-2.5"
        >
          <p
            className={`text-sm font-medium ${
              accent ? "text-emerald-200" : "text-white/90"
            }`}
          >
            {it.t}
          </p>
          <p className="mt-1 text-xs leading-relaxed text-white/60">{it.d}</p>
        </li>
      ))}
    </ul>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-white/50">{label}</dt>
      <dd className="text-white/90 mt-0.5">{value}</dd>
    </div>
  );
}
