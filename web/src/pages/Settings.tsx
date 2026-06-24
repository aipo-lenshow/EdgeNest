// Settings page. Centralizes the four v0.02 admin knobs that previously could
// only be tweaked via direct DB edits:
//   1. subscription host (the value embedded in client links)
//   2. panel path prefix (URL obscurity)
//   3. admin username (rotate the default `EdgeNest`)
//   4. daily notification bot (Telegram)

import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { QRCodeSVG } from "qrcode.react";
import Layout from "../components/Layout";
import {
  Button,
  Card,
  ConfirmDialog,
  ErrorText,
  Field,
  Input,
  Modal,
  PageHeader,
  Select,
  TextArea,
  Toggle,
} from "../components/ui";
import { api, call, clearToken } from "../api/client";
import CopyButton from "../components/CopyButton";
import { setLang, currentLang, SUPPORTED_LANGS, LANG_NAMES, type Lang } from "../i18n/i18n";
import { readStoredTheme, setTheme, type Theme } from "../lib/theme";
import { setTzCache, listTimezones, tzLabel } from "../lib/datetime";

interface SettingsResponse {
  host: string;
  panel_path: string;
  notify: {
    enabled: boolean;
    daily_hour: number;
    daily_minute: number;
    telegram_chat_id: string;
    telegram_token_set: boolean;
    bot_enabled: boolean;
    alerts_enabled: boolean;
    update_check_enabled: boolean;
    bot_admin_chat_ids: string[];
  };
}

function useFlash() {
  const [msg, setMsg] = useState<{ kind: "ok" | "err"; text: string } | null>(
    null,
  );
  return {
    msg,
    ok: (text: string) => setMsg({ kind: "ok", text }),
    err: (text: string) => setMsg({ kind: "err", text }),
    clear: () => setMsg(null),
  };
}

type TabKey = "general" | "account" | "notify" | "privacy" | "backup";

const TABS: { key: TabKey; labelKey: string }[] = [
  { key: "general", labelKey: "settings.tabGeneral" },
  { key: "account", labelKey: "settings.tabAccount" },
  { key: "notify", labelKey: "settings.tabNotify" },
  { key: "privacy", labelKey: "settings.tabPrivacy" },
  { key: "backup", labelKey: "settings.tabBackup" },
];

export default function SettingsPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [searchParams, setSearchParams] = useSearchParams();
  const settings = useQuery({
    queryKey: ["settings"],
    queryFn: () => call<SettingsResponse>(api.get("/settings")),
  });

  const requested = searchParams.get("tab") as TabKey | null;
  const active: TabKey =
    requested && TABS.some((tb) => tb.key === requested) ? requested : "general";

  const selectTab = (key: TabKey) => {
    const next = new URLSearchParams(searchParams);
    next.set("tab", key);
    setSearchParams(next, { replace: true });
  };

  const invalidate = () => qc.invalidateQueries({ queryKey: ["settings"] });

  return (
    <Layout>
      <PageHeader title={t("settings.title")} subtitle={t("settings.subtitle")} />

      <div className="mb-5 flex flex-wrap gap-1 border-b border-white/10">
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

      {settings.isLoading && <div className="text-white/50">{t("common.loading")}</div>}
      {settings.data && (
        <div className="grid gap-6">
          {active === "general" && (
            <>
              <AccessAddressCard panelPath={settings.data.panel_path} />
              <PanelPathCard data={settings.data} onSaved={invalidate} />
              <AppearanceCard />
            </>
          )}
          {active === "account" && (
            <>
              <UsernameCard />
              <PasswordCard />
              <TwoFACard />
            </>
          )}
          {active === "notify" && <NotifyCard data={settings.data} onSaved={invalidate} />}
          {active === "privacy" && <LogPrivacyCard />}
          {active === "backup" && <BackupCard />}
        </div>
      )}
    </Layout>
  );
}

// AppearanceCard hosts the interface language + theme switchers, moved out of
// the sidebar footer (batch D / M5). Both are browser-local and apply instantly,
// so there's no save button — picking an option takes effect immediately.
function AppearanceCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const lang = currentLang();
  const [theme, setThemeState] = useState<Theme>(readStoredTheme());
  useEffect(() => {
    setTheme(theme);
  }, [theme]);

  // Timezone is a panel-wide setting (the bot/notify clock + frontend
  // rendering all read it). server_tz is the detected host zone, shown as the
  // "follow server" default; display_tz is the operator's override ("" = follow).
  const sys = useQuery({
    queryKey: ["system-info"],
    queryFn: () => call<SystemInfoResponse>(api.get("/system/info")),
  });
  const serverTz = sys.data?.server_tz ?? "UTC";
  const displayTz = sys.data?.display_tz ?? "";
  const zones = listTimezones();

  const saveTz = useMutation({
    mutationFn: (tz: string) => call(api.put("/settings/timezone", { timezone: tz })),
    onSuccess: (_d, tz) => {
      // Mirror the effective zone so fmtTime/fmtDate pick it up on the next
      // render, and re-sync system-info (Layout reads display_tz from it).
      setTzCache(tz || serverTz);
      qc.invalidateQueries({ queryKey: ["system-info"] });
    },
  });

  // Persist the language panel-wide too (default_lang), so server-side
  // presentation that can't see this browser's localStorage — the Telegram bot
  // replies, index.html first paint — follows the operator's choice. Best
  // effort: the in-browser switch (setLang) is what the user sees immediately.
  const saveLang = useMutation({
    mutationFn: (l: Lang) => call(api.put("/settings/language", { lang: l })),
  });
  const changeLang = (l: Lang) => {
    setLang(l);
    saveLang.mutate(l);
  };

  return (
    <Card title={t("settings.appearanceSection")}>
      <div className="grid gap-3">
        <p className="text-xs text-white/50">{t("settings.appearanceHint")}</p>
        <Field label={t("settings.appearanceLanguage")}>
          <Select value={lang} onChange={(e) => changeLang(e.target.value as Lang)} aria-label="language">
            {SUPPORTED_LANGS.map((l) => (
              <option key={l} value={l}>{LANG_NAMES[l]}</option>
            ))}
          </Select>
        </Field>
        <div className="grid gap-3 md:grid-cols-2">
          <Field label={t("settings.appearanceTheme")}>
            <Select value={theme} onChange={(e) => setThemeState(e.target.value as Theme)} aria-label="theme">
              <option value="light">{t("layout.theme.light")}</option>
              <option value="dark">{t("layout.theme.dark")}</option>
              <option value="auto">{t("layout.theme.auto")}</option>
            </Select>
          </Field>
          <Field
            label={t("settings.appearanceTimezone")}
            hint={t("settings.appearanceTimezoneHint", { tz: serverTz })}
          >
            <Select
              value={displayTz}
              disabled={sys.isLoading || saveTz.isPending}
              onChange={(e) => saveTz.mutate(e.target.value)}
              aria-label="timezone"
            >
              <option value="">{t("settings.appearanceTzFollowServer", { tz: serverTz })}</option>
              {zones.map((z) => (
                <option key={z} value={z}>{tzLabel(z)}</option>
              ))}
            </Select>
          </Field>
        </div>
      </div>
    </Card>
  );
}

// TwoFACard manages TOTP two-factor auth: enroll (scan QR → confirm code →
// stash recovery codes) or disable (password re-auth). Reads current state from
// /me so the card reflects whether 2FA is already on.
function TwoFACard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const me = useQuery({
    queryKey: ["me"],
    queryFn: () => call<{ totp_enabled: boolean }>(api.get("/me")),
  });
  const enabled = me.data?.totp_enabled ?? false;

  // Enrollment state.
  const [setup, setSetup] = useState<{ secret: string; uri: string } | null>(null);
  const [code, setCode] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [disablePw, setDisablePw] = useState("");
  const flash = useFlash();

  const begin = useMutation({
    mutationFn: () => call<{ secret: string; uri: string }>(api.post("/2fa/setup", {})),
    onSuccess: (d) => { setSetup(d); setCode(""); flash.clear(); },
    onError: (e: Error) => flash.err(e.message),
  });
  const enable = useMutation({
    mutationFn: () => call<{ recovery_codes: string[] }>(api.post("/2fa/enable", { code: code.trim() })),
    onSuccess: (d) => {
      setCodes(d.recovery_codes); setSetup(null); setCode("");
      qc.invalidateQueries({ queryKey: ["me"] });
      flash.ok(t("settings.twofaEnabled"));
    },
    onError: (e: Error) => flash.err(e.message),
  });
  const disable = useMutation({
    mutationFn: () => call(api.post("/2fa/disable", { password: disablePw })),
    onSuccess: () => {
      setDisablePw(""); setCodes(null);
      qc.invalidateQueries({ queryKey: ["me"] });
      flash.ok(t("settings.twofaDisabled"));
    },
    onError: (e: Error) => flash.err(e.message),
  });

  return (
    <Card title={t("settings.twofaSection")}>
      <div className="grid gap-3">
        <p className="text-xs text-white/50">{t("settings.twofaIntro")}</p>

        {/* Recovery codes — shown once, right after enabling or regenerating. */}
        {codes && (
          <div className="rounded-xl border border-amber-500/30 bg-amber-500/[0.06] p-3">
            <p className="text-xs font-medium text-amber-200 mb-2">{t("settings.twofaCodesTitle")}</p>
            <div className="grid grid-cols-2 gap-1 font-mono text-sm text-white/90">
              {codes.map((c) => <span key={c}>{c}</span>)}
            </div>
            <Button variant="ghost" className="mt-2" onClick={() => navigator.clipboard?.writeText(codes.join("\n"))}>
              {t("settings.twofaCopyCodes")}
            </Button>
          </div>
        )}

        {!enabled && !setup && (
          <div className="flex items-center gap-3">
            <Button variant="primary" disabled={begin.isPending} onClick={() => begin.mutate()}>
              {t("settings.twofaEnableBtn")}
            </Button>
          </div>
        )}

        {!enabled && setup && (
          <div className="grid gap-3 md:grid-cols-[auto,1fr] items-start">
            <div className="rounded-lg bg-white p-3 w-fit">
              <QRCodeSVG value={setup.uri} size={160} level="M" />
            </div>
            <div className="grid gap-2">
              <p className="text-xs text-white/60">{t("settings.twofaScanHint")}</p>
              <Field label={t("settings.twofaSecretLabel")}>
                <Input readOnly value={setup.secret} className="font-mono" />
              </Field>
              <Field label={t("settings.twofaCodeLabel")} hint={t("settings.twofaCodeHint")}>
                <Input
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  inputMode="numeric"
                  placeholder="123456"
                  className="font-mono tracking-widest"
                />
              </Field>
              <div className="flex items-center gap-2">
                <Button variant="primary" disabled={enable.isPending || code.trim().length < 6} onClick={() => enable.mutate()}>
                  {t("settings.twofaConfirmBtn")}
                </Button>
                <Button variant="ghost" onClick={() => { setSetup(null); setCode(""); }}>
                  {t("routes.btnCancel")}
                </Button>
              </div>
            </div>
          </div>
        )}

        {enabled && (
          <div className="grid gap-2 md:grid-cols-2">
            <div className="flex items-center gap-2 text-sm text-emerald-300">
              ✓ {t("settings.twofaOn")}
            </div>
            <div className="flex items-end gap-2">
              <Field label={t("settings.twofaDisablePwLabel")}>
                <Input type="password" value={disablePw} onChange={(e) => setDisablePw(e.target.value)} autoComplete="current-password" />
              </Field>
              <Button variant="danger" disabled={disable.isPending || !disablePw} onClick={() => disable.mutate()}>
                {t("settings.twofaDisableBtn")}
              </Button>
            </div>
          </div>
        )}

        <div className="flex items-center gap-3">
          {flash.msg?.kind === "ok" && <span className="text-emerald-300 text-sm">{flash.msg.text}</span>}
          {flash.msg?.kind === "err" && <ErrorText>{flash.msg.text}</ErrorText>}
        </div>
      </div>
    </Card>
  );
}

// BackupCard downloads the full panel backup (DB + certs, optionally encrypted)
// or restores one. Restore stages the upload and restarts the panel, so the UI
// warns + confirms — and prompts for the passphrase when the file is encrypted.
function BackupCard() {
  const { t } = useTranslation();
  const fileRef = useRef<HTMLInputElement>(null);
  const flash = useFlash();

  // Backup-side state: optional encryption + a confirmed passphrase.
  const [encrypt, setEncrypt] = useState(false);
  const [pw1, setPw1] = useState("");
  const [pw2, setPw2] = useState("");
  const pwMismatch = encrypt && pw2.length > 0 && pw1 !== pw2;
  const canDownload = !encrypt || (pw1.length > 0 && pw1 === pw2);

  // Restore-side state: chosen file, whether it's encrypted, its passphrase.
  const [pending, setPending] = useState<File | null>(null);
  const [restoreEnc, setRestoreEnc] = useState(false);
  const [restorePw, setRestorePw] = useState("");
  const [restoreOpen, setRestoreOpen] = useState(false);

  const download = useMutation({
    mutationFn: async () => {
      // POST so the passphrase travels in the body, not a logged URL. The
      // response is a binary attachment, so bypass the JSON envelope.
      const resp = await api.post(
        "/system/backup",
        { encrypt, password: encrypt ? pw1 : "" },
        { responseType: "blob" },
      );
      const cd = (resp.headers["content-disposition"] as string) || "";
      const m = /filename="?([^";]+)"?/.exec(cd);
      const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, "");
      const fallback = `edgenest-backup-${stamp}.tar.gz${encrypt ? ".enc" : ""}`;
      const url = URL.createObjectURL(resp.data as Blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = m?.[1] || fallback;
      a.click();
      URL.revokeObjectURL(url);
    },
    onSuccess: () => flash.ok(t("settings.backupDownloaded")),
    onError: (e: Error) => flash.err(e.message),
  });

  const restore = useMutation({
    mutationFn: async () => {
      const fd = new FormData();
      fd.append("backup", pending!);
      if (restoreEnc) fd.append("password", restorePw);
      return call(api.post("/system/restore", fd, { headers: { "Content-Type": "multipart/form-data" } }));
    },
    onSuccess: () => {
      flash.ok(t("settings.restoreOk"));
      // The panel restarts; clear the token and bounce to login after a beat.
      setTimeout(() => { clearToken(); window.location.href = "/login"; }, 4000);
    },
    onError: (e: Error) => flash.err(e.message),
  });

  // Sniff the first bytes to know whether the chosen backup is encrypted, so we
  // can show the passphrase field before firing the restore.
  async function pickFile(f: File | null) {
    setRestorePw("");
    setPending(f);
    if (!f) { setRestoreEnc(false); setRestoreOpen(false); return; }
    const head = new TextDecoder().decode(await f.slice(0, 12).arrayBuffer());
    setRestoreEnc(head === "EDGENESTENC1");
    setRestoreOpen(true);
  }

  function closeRestore() {
    setRestoreOpen(false);
    setPending(null);
    setRestorePw("");
    if (fileRef.current) fileRef.current.value = "";
  }

  return (
    <Card title={t("settings.backupSection")}>
      <div className="grid gap-3">
        <p className="text-xs text-white/50">{t("settings.backupIntro")}</p>
        <p className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
          {t("settings.backupSensitiveWarn")}
        </p>

        <Toggle checked={encrypt} onChange={setEncrypt} label={t("settings.backupEncrypt")} />
        {encrypt && (
          <div className="grid gap-2 sm:grid-cols-2">
            <Field label={t("settings.backupPw")} hint={t("settings.backupPwHint")}>
              <Input type="password" value={pw1} autoComplete="new-password"
                onChange={(e) => setPw1(e.target.value)} />
            </Field>
            <Field label={t("settings.backupPwConfirm")}
              hint={pwMismatch ? <span className="text-red-400">{t("settings.backupPwMismatch")}</span> : undefined}>
              <Input type="password" value={pw2} autoComplete="new-password"
                onChange={(e) => setPw2(e.target.value)} />
            </Field>
          </div>
        )}

        <div className="flex flex-wrap items-center gap-3">
          <Button variant="primary" disabled={download.isPending || !canDownload} onClick={() => download.mutate()}>
            {t("settings.backupDownloadBtn")}
          </Button>
          <input
            ref={fileRef}
            type="file"
            accept=".db,.gz,.enc,.tar,application/gzip,application/octet-stream,application/x-sqlite3"
            className="hidden"
            onChange={(e) => pickFile(e.target.files?.[0] ?? null)}
          />
          <Button variant="default" onClick={() => fileRef.current?.click()}>
            {t("settings.backupChooseFile")}
          </Button>
          {pending && <span className="text-xs text-white/60">{pending.name}</span>}
        </div>
        <div className="flex items-center gap-3">
          {flash.msg?.kind === "ok" && <span className="text-emerald-300 text-sm">{flash.msg.text}</span>}
          {flash.msg?.kind === "err" && <ErrorText>{flash.msg.text}</ErrorText>}
        </div>
      </div>

      <Modal
        open={restoreOpen && !restore.isSuccess}
        onClose={() => { if (!restore.isPending) closeRestore(); }}
        title={t("settings.restoreConfirmTitle")}
        footer={
          <>
            <Button variant="default" disabled={restore.isPending} onClick={closeRestore}>
              {t("routes.btnCancel")}
            </Button>
            <Button
              variant="danger"
              disabled={restore.isPending || (restoreEnc && restorePw.length === 0)}
              onClick={() => restore.mutate()}
            >
              {restore.isPending ? "…" : t("settings.restoreConfirmBtn")}
            </Button>
          </>
        }
      >
        <div className="grid gap-3 text-sm">
          <p className="text-white/70">{t("settings.restoreConfirmBody", { name: pending?.name ?? "" })}</p>
          {restoreEnc && (
            <Field label={t("settings.restorePw")} hint={t("settings.restorePwHint")}>
              <Input type="password" value={restorePw} autoFocus autoComplete="off"
                onChange={(e) => setRestorePw(e.target.value)} />
            </Field>
          )}
        </div>
      </Modal>
    </Card>
  );
}

// fmtBytes renders a byte count as B / KB / MB for the log-size readout.
function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// LogPrivacyCard exposes two self-host privacy knobs over the engine logs:
//   1. "don't log client IP" — masks every IP in the sing-box/xray log stream
//      before it hits disk (handled in internal/logredact; no config change).
//   2. "clear logs" — truncates sing-box.log / xray.log in place, no restart.
function LogPrivacyCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const flash = useFlash();
  const [confirmClear, setConfirmClear] = useState(false);

  const adv = useQuery({
    queryKey: ["advanced"],
    queryFn: () => call<{ redact_client_ip: boolean }>(api.get("/advanced")),
  });
  const size = useQuery({
    queryKey: ["logs-size"],
    queryFn: () => call<{ total_bytes: number }>(api.get("/logs/size")),
  });

  const redact = useMutation({
    mutationFn: (v: boolean) =>
      call(api.put("/advanced/logprivacy", { redact_client_ip: v })),
    onSuccess: (_d, v) => {
      flash.ok(v ? t("settings.logRedactOnSaved") : t("settings.logRedactOffSaved"));
      qc.invalidateQueries({ queryKey: ["advanced"] });
    },
    onError: (e: Error) => flash.err(e.message),
  });

  const clear = useMutation({
    mutationFn: () =>
      call<{ cleared_bytes: number }>(api.post("/logs/clear", {})),
    onSuccess: (d) => {
      flash.ok(t("settings.logCleared", { kb: Math.round((d.cleared_bytes ?? 0) / 1024) }));
      qc.invalidateQueries({ queryKey: ["logs-size"] });
    },
    onError: (e: Error) => flash.err(e.message),
  });

  const on = adv.data?.redact_client_ip ?? false;

  return (
    <Card title={t("settings.logPrivacySection")}>
      <div className="grid gap-3">
        {/* "Don't log client IP" — hint sits above the switch so it reads as
            "turning this on does X", consistent with the static switch label. */}
        <p className="text-xs text-white/50">{t("settings.logRedactHint")}</p>
        <Toggle
          checked={on}
          onChange={(v) => redact.mutate(v)}
          disabled={adv.isLoading || redact.isPending}
          pendingLabel={t("common.saving")}
          label={t("settings.logRedactLabel")}
        />
        {flash.msg?.kind === "ok" && <span className="text-sm text-emerald-300">{flash.msg.text}</span>}
        {flash.msg?.kind === "err" && <ErrorText>{flash.msg.text}</ErrorText>}

        <div className="my-1 h-px bg-white/10" />

        <p className="text-xs text-white/50">{t("settings.logClearIntro")}</p>
        <div className="flex flex-wrap items-center gap-3">
          <Button variant="default" disabled={size.isFetching} onClick={() => size.refetch()}>
            {t("settings.logSizeRefresh")}
          </Button>
          <Button variant="default" disabled={clear.isPending} onClick={() => setConfirmClear(true)}>
            {t("settings.logClearBtn")}
          </Button>
          <span className="text-xs text-white/40">
            {t("settings.logSizeLabel")}: {size.isFetching ? "…" : size.data ? fmtBytes(size.data.total_bytes) : "—"}
          </span>
        </div>
      </div>

      <ConfirmDialog
        open={confirmClear}
        title={t("settings.logClearConfirmTitle")}
        body={t("settings.logClearConfirmBody")}
        variant="danger"
        confirmLabel={t("settings.logClearBtn")}
        cancelLabel={t("routes.btnCancel")}
        onConfirm={() => { setConfirmClear(false); clear.mutate(); }}
        onCancel={() => setConfirmClear(false)}
      />
    </Card>
  );
}

interface SystemInfoResponse {
  panel_port: number;
  server_tz?: string;
  display_tz?: string;
  network_capability?: {
    ipv4_addr?: string;
    ipv4_addrs?: string[];
    ipv6_addr?: string;
    ipv6_addrs?: string[];
  };
}

// AccessAddressCard replaces the old free-text "host" field. The panel binds
// all interfaces (LISTEN *:port), so it's reachable on every public address
// install.sh probed into network.json — we just read those back and render a
// ready-to-open URL per address. No free-text host: a single typed domain
// can't express the dual-stack (v4 + v6) reality, and editing it never
// regenerated the self-signed cert SAN anyway. The share_host setting (used
// only as the subscription fallback) is left untouched on the backend.
function AccessAddressCard({ panelPath }: { panelPath: string }) {
  const { t } = useTranslation();
  const sys = useQuery({
    queryKey: ["system-info"],
    queryFn: () => call<SystemInfoResponse>(api.get("/system/info")),
  });

  const cap = sys.data?.network_capability;
  const port = sys.data?.panel_port ?? 0;

  // Collect every distinct public address, preferring the full *_addrs lists
  // and falling back to the singular field for legacy network.json shapes.
  const v4 = cap?.ipv4_addrs?.length ? cap.ipv4_addrs : cap?.ipv4_addr ? [cap.ipv4_addr] : [];
  const v6 = cap?.ipv6_addrs?.length ? cap.ipv6_addrs : cap?.ipv6_addr ? [cap.ipv6_addr] : [];
  const rows = [
    ...v4.filter(Boolean).map((ip) => ({ ip, family: "IPv4" as const })),
    ...v6.filter(Boolean).map((ip) => ({ ip, family: "IPv6" as const })),
  ];

  // Build the panel URL. IPv6 literals must be bracketed in an authority.
  // Protocol follows however the operator currently reaches the panel.
  const urlFor = (ip: string, family: "IPv4" | "IPv6") => {
    const proto = window.location.protocol; // "http:" / "https:"
    const authority = family === "IPv6" ? `[${ip}]` : ip;
    const portPart = port ? `:${port}` : "";
    return `${proto}//${authority}${portPart}${panelPath}`;
  };

  return (
    <Card title={t("settings.accessSection")}>
      <div className="grid gap-3">
        <p className="text-xs text-white/50">{t("settings.accessHint")}</p>
        {sys.isLoading && <div className="text-white/40 text-sm">{t("common.loading")}</div>}
        {sys.data && rows.length === 0 && (
          <p className="text-sm text-amber-300">{t("settings.accessNone")}</p>
        )}
        {rows.length > 0 && (
          <div className="grid gap-2">
            {rows.map(({ ip, family }) => {
              const url = urlFor(ip, family);
              return (
                <div
                  key={`${family}-${ip}`}
                  className="flex flex-wrap items-center gap-3 rounded-xl border border-white/10 bg-white/[0.03] px-3 py-2"
                >
                  <span className="shrink-0 rounded-md border border-white/15 px-1.5 py-0.5 text-[10px] font-medium text-white/60">
                    {family}
                  </span>
                  <code className="min-w-0 flex-1 break-all font-mono text-sm text-white/90">{url}</code>
                  <a
                    href={url}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="shrink-0 text-sm text-emerald-300 hover:text-emerald-200"
                  >
                    {t("settings.accessOpen")}
                  </a>
                  <CopyButton text={url} />
                </div>
              );
            })}
          </div>
        )}
      </div>
    </Card>
  );
}

function PanelPathCard({
  data,
  onSaved,
}: {
  data: SettingsResponse;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [path, setPath] = useState(data.panel_path);
  const flash = useFlash();
  const save = useMutation({
    mutationFn: (newPath: string) =>
      call<{ panel_path: string }>(
        api.put("/settings/panel-path", { panel_path: newPath }),
      ),
    onSuccess: (resp) => {
      setPath(resp.panel_path);
      flash.ok(t("settings.panelPathSaved"));
      onSaved();
    },
    onError: (e: Error) => flash.err(e.message),
  });
  return (
    <Card title={t("settings.panelPathSection")}>
      <div className="grid gap-3">
        <Field label={t("settings.panelPathLabel")} hint={t("settings.panelPathHint")}>
          <Input value={path} onChange={(e) => setPath(e.target.value)} placeholder="/ENPanel-XXXXXXXX" />
        </Field>
        <div className="flex items-center gap-3">
          <Button variant="primary" disabled={save.isPending} onClick={() => save.mutate(path)}>
            {t("common.save")}
          </Button>
          <Button
            variant="ghost"
            disabled={save.isPending}
            onClick={() => save.mutate("")}
          >
            {t("settings.panelPathRegenerate")}
          </Button>
          {flash.msg?.kind === "ok" && <span className="text-emerald-300 text-sm">{flash.msg.text}</span>}
          {flash.msg?.kind === "err" && <ErrorText>{flash.msg.text}</ErrorText>}
        </div>
      </div>
    </Card>
  );
}

function UsernameCard() {
  const { t } = useTranslation();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const flash = useFlash();
  const mut = useMutation({
    mutationFn: () =>
      call<{ must_reauth: boolean }>(
        api.put("/admin/username", { new_username: username, password }),
      ),
    onSuccess: () => {
      flash.ok(t("settings.usernameSaved"));
      clearToken();
      setTimeout(() => {
        window.location.href = "/login";
      }, 1200);
    },
    onError: (e: Error) => flash.err(e.message),
  });
  return (
    <Card title={t("settings.usernameSection")}>
      <div className="grid gap-3 md:grid-cols-2">
        <Field label={t("settings.usernameLabel")} hint={t("settings.usernameHint")}>
          <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
        </Field>
        <Field label={t("settings.usernamePasswordLabel")}>
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
          />
        </Field>
      </div>
      <div className="flex items-center gap-3 mt-3">
        <Button
          variant="primary"
          disabled={mut.isPending || !username || !password}
          onClick={() => mut.mutate()}
        >
          {t("common.update")}
        </Button>
        {flash.msg?.kind === "ok" && <span className="text-emerald-300 text-sm">{flash.msg.text}</span>}
        {flash.msg?.kind === "err" && <ErrorText>{flash.msg.text}</ErrorText>}
      </div>
    </Card>
  );
}

function PasswordCard() {
  const { t } = useTranslation();
  const [oldPw, setOldPw] = useState("");
  const [newPw, setNewPw] = useState("");
  const [confirmPw, setConfirmPw] = useState("");
  const flash = useFlash();
  const mut = useMutation({
    mutationFn: () =>
      call(api.post("/password", { old_password: oldPw, new_password: newPw })),
    onSuccess: () => {
      flash.ok(t("settings.passwordSaved"));
      setOldPw("");
      setNewPw("");
      setConfirmPw("");
    },
    onError: (e: Error) => flash.err(e.message),
  });
  const mismatch = confirmPw !== "" && newPw !== confirmPw;
  return (
    <Card title={t("settings.passwordSection")}>
      <div className="grid gap-3 md:grid-cols-3">
        <Field label={t("settings.passwordCurrent")}>
          <Input
            type="password"
            value={oldPw}
            onChange={(e) => setOldPw(e.target.value)}
            autoComplete="current-password"
          />
        </Field>
        <Field label={t("settings.passwordNew")} hint={t("settings.passwordNewHint")}>
          <Input
            type="password"
            value={newPw}
            onChange={(e) => setNewPw(e.target.value)}
            autoComplete="new-password"
          />
        </Field>
        <Field
          label={t("settings.passwordConfirm")}
          hint={mismatch ? t("settings.passwordMismatch") : undefined}
        >
          <Input
            type="password"
            value={confirmPw}
            onChange={(e) => setConfirmPw(e.target.value)}
            autoComplete="new-password"
          />
        </Field>
      </div>
      <div className="flex items-center gap-3 mt-3">
        <Button
          variant="primary"
          disabled={
            mut.isPending || !oldPw || newPw.length < 8 || newPw !== confirmPw
          }
          onClick={() => mut.mutate()}
        >
          {t("common.save")}
        </Button>
        {flash.msg?.kind === "ok" && <span className="text-emerald-300 text-sm">{flash.msg.text}</span>}
        {flash.msg?.kind === "err" && <ErrorText>{flash.msg.text}</ErrorText>}
      </div>
    </Card>
  );
}

function NotifyCard({
  data,
  onSaved,
}: {
  data: SettingsResponse;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const n = data.notify;
  const [enabled, setEnabled] = useState(n.enabled);
  const [hour, setHour] = useState(n.daily_hour);
  const [minute, setMinute] = useState(n.daily_minute ?? 0);
  const [telegramToken, setTelegramToken] = useState("");
  const [telegramChat, setTelegramChat] = useState(n.telegram_chat_id);
  const [botEnabled, setBotEnabled] = useState(n.bot_enabled);
  const [alertsEnabled, setAlertsEnabled] = useState(n.alerts_enabled);
  const [updateCheck, setUpdateCheck] = useState(n.update_check_enabled);
  const [adminIds, setAdminIds] = useState((n.bot_admin_chat_ids ?? []).join("\n"));
  const [showCmdRef, setShowCmdRef] = useState(false);
  const flash = useFlash();
  const testFlash = useFlash();

  const save = useMutation({
    mutationFn: () =>
      call(
        api.put("/settings/notify", {
          enabled,
          daily_hour: hour,
          daily_minute: minute,
          telegram_token: telegramToken === "" ? undefined : telegramToken,
          telegram_chat_id: telegramChat,
          bot_enabled: botEnabled,
          alerts_enabled: alertsEnabled,
          update_check_enabled: updateCheck,
          bot_admin_chat_ids: adminIds
            .split(/[\n,]/)
            .map((s) => s.trim())
            .filter(Boolean),
        }),
      ),
    onSuccess: () => {
      flash.ok(t("settings.notifySaved"));
      setTelegramToken("");
      onSaved();
    },
    onError: (e: Error) => flash.err(e.message),
  });

  const test = useMutation({
    mutationFn: () => call(api.post("/settings/notify/test", { channel: "telegram" })),
    onSuccess: () => testFlash.ok(t("settings.notifyTestOk")),
    onError: (e: Error & { code?: string }) =>
      testFlash.err(e.code === "TELEGRAM_NEED_START" ? t("settings.notifyErrNeedStart") : e.message),
  });

  return (
    <Card title={t("settings.notifySection")}>
      <div className="grid gap-4">
        <div className="flex items-center gap-6">
          <Toggle checked={enabled} onChange={setEnabled} label={t("settings.notifyEnable")} />
          <Field label={t("settings.notifyHour")}>
            <div className="flex items-center gap-1.5">
              <Select value={hour} onChange={(e) => setHour(parseInt(e.target.value, 10))}>
                {Array.from({ length: 24 }, (_, i) => (
                  <option key={i} value={i}>{String(i).padStart(2, "0")}</option>
                ))}
              </Select>
              <span className="text-sm opacity-70">:</span>
              <Select value={minute} onChange={(e) => setMinute(parseInt(e.target.value, 10))}>
                {Array.from({ length: 60 }, (_, i) => (
                  <option key={i} value={i}>{String(i).padStart(2, "0")}</option>
                ))}
              </Select>
            </div>
          </Field>
        </div>

        <div className="border-t border-white/10 pt-4">
          <div className="text-sm font-semibold mb-2">{t("settings.notifyTelegramTitle")}</div>
          <div className="grid gap-3 md:grid-cols-2">
            <Field
              label={t("settings.notifyTelegramToken")}
              hint={n.telegram_token_set ? t("settings.notifyTokenStored") : t("settings.notifyTelegramTokenHint")}
            >
              <Input value={telegramToken} onChange={(e) => setTelegramToken(e.target.value)} placeholder="123456:ABC-..." />
            </Field>
            <Field label={t("settings.notifyTelegramChat")} hint={t("settings.notifyTelegramChatHint")}>
              <Input value={telegramChat} onChange={(e) => setTelegramChat(e.target.value)} placeholder="123456789" />
            </Field>
          </div>
        </div>

        <div className="border-t border-white/10 pt-4">
          <div className="flex items-center justify-between mb-2">
            <div className="text-sm font-semibold">{t("settings.botSection")}</div>
            <Button variant="ghost" onClick={() => setShowCmdRef(true)}>
              {t("settings.botCmdRefBtn")}
            </Button>
          </div>
          <p className="text-xs text-white/50 mb-2">{t("settings.botHint")}</p>
          <div className="grid gap-3">
            <Toggle checked={botEnabled} onChange={setBotEnabled} label={t("settings.botEnable")} />
            <Toggle checked={alertsEnabled} onChange={setAlertsEnabled} label={t("settings.alertsEnable")} />
            <p className="text-xs text-white/50 -mt-1">{t("settings.alertsHint")}</p>
            <Toggle checked={updateCheck} onChange={setUpdateCheck} label={t("settings.updateCheckEnable")} />
            <p className="text-xs text-white/50 -mt-1">{t("settings.updateCheckHint")}</p>
            <Field label={t("settings.botAdminIds")} hint={t("settings.botAdminIdsHint")}>
              <TextArea
                rows={3}
                value={adminIds}
                onChange={(e) => setAdminIds(e.target.value)}
                placeholder={n.telegram_chat_id || "123456789"}
              />
            </Field>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-3 pt-2">
          <Button variant="primary" disabled={save.isPending} onClick={() => save.mutate()}>
            {t("common.save")}
          </Button>
          <Button variant="ghost" disabled={test.isPending} onClick={() => test.mutate()}>
            {t("settings.notifyTestTelegram")}
          </Button>
          {flash.msg?.kind === "ok" && <span className="text-emerald-300 text-sm">{flash.msg.text}</span>}
          {flash.msg?.kind === "err" && <ErrorText>{flash.msg.text}</ErrorText>}
          {testFlash.msg?.kind === "ok" && <span className="text-emerald-300 text-sm">{testFlash.msg.text}</span>}
          {testFlash.msg?.kind === "err" && <ErrorText>{testFlash.msg.text}</ErrorText>}
        </div>
      </div>

      <Modal
        open={showCmdRef}
        onClose={() => setShowCmdRef(false)}
        title={t("settings.botCmdRefTitle")}
        size="lg"
        footer={
          <Button variant="primary" onClick={() => setShowCmdRef(false)}>
            {t("common.close")}
          </Button>
        }
      >
        <p className="text-xs text-white/50 mb-3">{t("settings.botCmdRefIntro")}</p>
        {(t("settings.botCmdRefGroups", { returnObjects: true }) as CmdRefGroup[]).map(
          (g, gi) => (
            <div key={gi} className="mb-4 last:mb-0">
              <div className="text-sm font-semibold mb-1.5">{g.title}</div>
              <div className="grid gap-1.5">
                {g.items.map((it, ii) => (
                  <div key={ii} className="grid gap-0.5">
                    <code className="text-xs text-emerald-300">{it.cmd}</code>
                    <span className="text-xs text-white/60">{it.desc}</span>
                  </div>
                ))}
              </div>
            </div>
          ),
        )}
      </Modal>
    </Card>
  );
}

interface CmdRefGroup {
  title: string;
  items: { cmd: string; desc: string }[];
}
