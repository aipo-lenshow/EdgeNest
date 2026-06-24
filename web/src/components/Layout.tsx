import React, { useEffect } from "react";
import { Link, NavLink, useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api, call, clearToken } from "../api/client";
import { BrandLogo, GithubIcon } from "./icons";
import { setTzCache } from "../lib/datetime";
import { APP_VERSION_LABEL } from "../lib/version";

interface Me {
  username: string;
  must_change_password: boolean;
  wizard_done: boolean;
  run_mode: string;
}

// Sidebar copyright footer — static project identity.
const COPYRIGHT_YEAR = "2026";
const GITHUB_URL = "https://github.com/aipo-lenshow/EdgeNest";

export default function Layout({ children }: { children: React.ReactNode }) {
  const { t } = useTranslation();
  const nav = useNavigate();
  const { data: me } = useQuery({
    queryKey: ["me"],
    queryFn: () => call<Me>(api.get("/me")),
    refetchOnWindowFocus: false,
  });

  // Adopt the panel-wide display timezone so every timestamp (via fmtTime)
  // renders consistently regardless of which browser the operator uses. Empty
  // display_tz means "follow server", so fall back to the detected server zone.
  const { data: sysInfo } = useQuery({
    queryKey: ["system-info"],
    queryFn: () =>
      call<{ server_tz?: string; display_tz?: string }>(api.get("/system/info")),
    refetchOnWindowFocus: false,
  });
  useEffect(() => {
    if (sysInfo) setTzCache(sysInfo.display_tz || sysInfo.server_tz || "");
  }, [sysInfo]);

  const runMode = me
    ? me.run_mode === "platform"
      ? t("layout.runMode.platform")
      : t("layout.runMode.standalone")
    : "";

  function logout() {
    clearToken();
    nav("/login");
  }

  const navItems: { to: string; key: string }[] = [
    { to: "/", key: "nav.dashboard" },
    { to: "/guide", key: "nav.guide" },
    { to: "/create-inbound", key: "nav.createInbound" },
    { to: "/connections", key: "nav.connections" },
    { to: "/certs", key: "nav.certs" },
    { to: "/inbound", key: "nav.inbound" },
    { to: "/outbound", key: "nav.outbound" },
    { to: "/firewall", key: "nav.firewall" },
    { to: "/stats", key: "nav.monitor" },
    { to: "/settings", key: "nav.settings" },
    { to: "/about", key: "nav.about" },
  ];

  return (
    <div className="min-h-screen flex bg-[var(--app-bg,transparent)]">
      {/* sticky + h-screen keeps the sidebar (and its bottom logout/copyright)
          pinned to the viewport while the main content scrolls. The nav itself
          scrolls internally if it ever outgrows the screen. */}
      <aside className="w-56 shrink-0 border-r border-black/10 dark:border-white/10 bg-black/[0.04] dark:bg-black/20 p-4 flex flex-col sticky top-0 h-screen">
        <Link to="/" className="block mb-6 px-2 shrink-0">
          <div className="flex items-center gap-2">
            <BrandLogo className="h-6 w-6 shrink-0" />
            <span className="text-lg font-semibold">EdgeNest</span>
            <span className="text-[10px] uppercase tracking-wide text-black/40 dark:text-white/40">
              Lite
            </span>
          </div>
          <div className="mt-1 text-[11px] text-black/40 dark:text-white/40">
            {runMode ? `${runMode} · ` : ""}
            {APP_VERSION_LABEL}
          </div>
        </Link>
        <nav className="flex-1 min-h-0 overflow-y-auto flex flex-col gap-0.5 text-sm">
          {navItems.map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              end={n.to === "/"}
              className={({ isActive }) =>
                `px-3 py-1.5 rounded-md transition ${
                  isActive
                    ? "bg-black/10 dark:bg-white/10 text-black dark:text-white"
                    : "text-black/60 dark:text-white/60 hover:text-black dark:hover:text-white hover:bg-black/5 dark:hover:bg-white/5"
                }`
              }
            >
              {t(n.key)}
            </NavLink>
          ))}
        </nav>
        <div className="shrink-0 mt-4 pt-4 border-t border-black/10 dark:border-white/10 text-xs">
          {/* Language / theme switchers moved to the Settings "Appearance" tab
              (batch D / M5). */}
          {me && (
            <div className="px-2 mb-1 text-black/40 dark:text-white/40 truncate">
              {t("layout.loggedInAs", { name: me.username })}
            </div>
          )}
          <button
            onClick={logout}
            className="w-full text-left px-2 py-1 text-black/50 dark:text-white/50 hover:text-black dark:hover:text-white"
          >
            {t("nav.logout")}
          </button>
          <div className="px-2 mt-2 pt-2 border-t border-black/5 dark:border-white/5 text-[10px] leading-relaxed text-black/35 dark:text-white/35 flex items-center justify-between gap-2">
            <span>© {COPYRIGHT_YEAR} AiPo · AGPL-3.0</span>
            <a
              href={GITHUB_URL}
              target="_blank"
              rel="noreferrer noopener"
              aria-label="GitHub"
              title={GITHUB_URL}
              className="shrink-0 hover:text-black/70 dark:hover:text-white/70"
            >
              <GithubIcon className="h-4 w-4" />
            </a>
          </div>
        </div>
      </aside>
      <main className="flex-1 p-8 overflow-x-auto">
        <div className="max-w-6xl mx-auto">{children}</div>
      </main>
    </div>
  );
}
