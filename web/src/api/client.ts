import axios from "axios";

// Uniform API envelope: { success, data, error }.
export interface ApiEnvelope<T> {
  success: boolean;
  data: T | null;
  error: { code: string; message: string } | null;
}

const TOKEN_KEY = "edgenest_token";
export const getToken = () => localStorage.getItem(TOKEN_KEY);
export const setToken = (t: string) => localStorage.setItem(TOKEN_KEY, t);
export const clearToken = () => localStorage.removeItem(TOKEN_KEY);

export const api = axios.create({ baseURL: "/api/v1" });

api.interceptors.request.use((cfg) => {
  const t = getToken();
  if (t) cfg.headers.Authorization = `Bearer ${t}`;
  return cfg;
});

// Drop stale tokens on 401 so the Protected guard can route back to /login.
// Without this, a JWT signed by a prior install's secret silently fails every
// /me probe, the guard sees data=undefined, and the panel renders blank.
//
// Also lifts the backend's envelope `error.message` onto err.message so React
// Query / useMutation surface the actual reason instead of axios's generic
// "Request failed with status code 400" — the latter is useless to the
// operator (no clue about port collision, duplicate SOCKS5, missing domain,
// etc.) and was the root cause behind opaque wizard failure dialogs.
api.interceptors.response.use(
  (res) => res,
  (err) => {
    if (err?.response?.status === 401) {
      clearToken();
      if (typeof window !== "undefined" && !window.location.pathname.endsWith("/login")) {
        window.location.assign("/login");
      }
    }
    const backendMsg = err?.response?.data?.error?.message;
    if (typeof backendMsg === "string" && backendMsg.length > 0) {
      err.message = backendMsg;
    }
    // Surface the envelope error code too so callers can localize specific
    // failures (e.g. TELEGRAM_NEED_START) instead of echoing the raw message.
    const backendCode = err?.response?.data?.error?.code;
    if (typeof backendCode === "string" && backendCode.length > 0) {
      err.code = backendCode;
    }
    return Promise.reject(err);
  },
);

// Unwrap the envelope; throw on error so callers can try/catch.
export async function call<T>(p: Promise<{ data: ApiEnvelope<T> }>): Promise<T> {
  const res = await p;
  if (!res.data.success || res.data.data === null) {
    throw new Error(res.data.error?.message ?? "request failed");
  }
  return res.data.data;
}
