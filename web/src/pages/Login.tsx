import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api, call, setToken } from "../api/client";

// First-run default: the install banner tells the operator the bootstrap
// admin is "EdgeNest". On a fresh box that's the only username they have,
// so pre-filling it saves a copy-paste round-trip to the SSH window.
// We persist whatever the user last logged in with so the default stops
// after they renamed the admin or added more users; "edgenest_last_user"
// becomes the source of truth from then on.
const LAST_USER_KEY = "edgenest_last_user";

function rememberedUsername(): string {
  try {
    return localStorage.getItem(LAST_USER_KEY) || "EdgeNest";
  } catch {
    return "EdgeNest";
  }
}

interface LoginResult {
  token?: string;
  must_change_password?: boolean;
  totp_required?: boolean;
}

export default function Login() {
  const nav = useNavigate();
  const { t } = useTranslation();
  const [username, setUsername] = useState(rememberedUsername);
  const [password, setPassword] = useState("");
  // Two-factor: once the password checks out for a 2FA admin, the server asks
  // for a code; we flip to the code step instead of erroring.
  const [needTotp, setNeedTotp] = useState(false);
  const [totp, setTotp] = useState("");
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(false);

  async function submit() {
    setErr("");
    setLoading(true);
    try {
      const data = await call<LoginResult>(
        api.post("/login", {
          username,
          password,
          totp_code: needTotp ? totp.trim() : undefined,
        }),
      );
      if (data.totp_required && !data.token) {
        setNeedTotp(true);
        return;
      }
      if (data.token) {
        setToken(data.token);
        try {
          localStorage.setItem(LAST_USER_KEY, username);
        } catch {}
        nav("/");
      }
    } catch (e: any) {
      // Map the server's error code to a localized message — the raw envelope
      // message is English-only, so showing it leaks English on a non-EN panel.
      const code = e?.response?.data?.error?.code as string | undefined;
      const byCode: Record<string, string> = {
        INVALID_CREDENTIALS: "auth.errInvalidCredentials",
        INVALID_2FA: "auth.errInvalid2fa",
        RATE_LIMITED: "auth.errRateLimited",
      };
      setErr(t(byCode[code ?? ""] ?? "auth.errGeneric"));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center px-4">
      <div className="w-full max-w-sm rounded-2xl border border-white/10 bg-white/5 p-8">
        <h1 className="text-2xl font-semibold mb-1">EdgeNest</h1>
        <p className="text-sm text-white/50 mb-6">{t("auth.signInSubtitle")}</p>
        {!needTotp ? (
          <>
            <label className="block text-xs text-white/60 mb-1">{t("auth.username")}</label>
            <input
              className="w-full mb-4 rounded-lg bg-black/30 border border-white/10 px-3 py-2 outline-none focus:border-white/30"
              autoComplete="username"
              placeholder={t("auth.usernamePlaceholder")}
              value={username} onChange={(e) => setUsername(e.target.value)} />
            <label className="block text-xs text-white/60 mb-1">{t("auth.password")}</label>
            <input type="password"
              className="w-full mb-4 rounded-lg bg-black/30 border border-white/10 px-3 py-2 outline-none focus:border-white/30"
              autoComplete="current-password"
              value={password} onChange={(e) => setPassword(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && submit()} />
          </>
        ) : (
          <>
            <label className="block text-xs text-white/60 mb-1">{t("auth.totpLabel")}</label>
            <input
              className="w-full mb-2 rounded-lg bg-black/30 border border-white/10 px-3 py-2 outline-none focus:border-white/30 tracking-[0.3em] font-mono"
              autoFocus
              inputMode="numeric"
              autoComplete="one-time-code"
              placeholder="123456"
              value={totp}
              onChange={(e) => setTotp(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && submit()} />
            <p className="text-xs text-white/40 mb-4">{t("auth.totpHint")}</p>
          </>
        )}
        {err && <div className="text-sm text-red-400 mb-3">{err}</div>}
        <button disabled={loading} onClick={submit}
          className="w-full rounded-lg bg-emerald-500/90 hover:bg-emerald-500 text-black font-medium py-2 disabled:opacity-50">
          {loading ? t("auth.signingIn") : needTotp ? t("auth.totpVerify") : t("auth.signIn")}
        </button>
        {needTotp && (
          <button
            onClick={() => { setNeedTotp(false); setTotp(""); setErr(""); }}
            className="w-full mt-2 text-xs text-white/40 hover:text-white/70">
            {t("auth.totpBack")}
          </button>
        )}
      </div>
    </div>
  );
}
