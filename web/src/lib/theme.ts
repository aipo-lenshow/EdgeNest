// Theme toggle. We support three modes: light / dark / auto. The default skin
// is dark (preserves v0.01 look); `auto` follows the OS preference via the
// prefers-color-scheme media query.

export type Theme = "light" | "dark" | "auto";
export const THEME_STORAGE_KEY = "edgenest_theme";

export function readStoredTheme(): Theme {
  try {
    const v = localStorage.getItem(THEME_STORAGE_KEY);
    if (v === "light" || v === "dark" || v === "auto") return v;
  } catch {
    // localStorage unavailable.
  }
  return "dark";
}

export function setTheme(theme: Theme) {
  try {
    localStorage.setItem(THEME_STORAGE_KEY, theme);
  } catch {
    // ignore
  }
  applyTheme(theme);
}

export function applyTheme(theme: Theme) {
  const root = document.documentElement;
  const effective = resolveTheme(theme);
  if (effective === "dark") root.classList.add("dark");
  else root.classList.remove("dark");
}

function resolveTheme(theme: Theme): "light" | "dark" {
  if (theme === "auto") {
    return window.matchMedia &&
      window.matchMedia("(prefers-color-scheme: dark)").matches
      ? "dark"
      : "light";
  }
  return theme;
}

// Call once at startup, then again whenever the OS preference flips while the
// user is on `auto`. Returns a teardown to unbind the media listener.
export function initTheme(): () => void {
  applyTheme(readStoredTheme());
  if (!window.matchMedia) return () => {};
  const mq = window.matchMedia("(prefers-color-scheme: dark)");
  const handler = () => {
    if (readStoredTheme() === "auto") applyTheme("auto");
  };
  mq.addEventListener?.("change", handler);
  return () => mq.removeEventListener?.("change", handler);
}
