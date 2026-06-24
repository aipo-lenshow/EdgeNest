// Subscription URL helpers — every subscription token can be fetched in
// several body formats. The server picks one from `?fmt=` or the request
// User-Agent (see internal/control/api/subscription_handlers.go pickSubFormat).
//
// The panel surfaces every entry in SUB_FORMATS explicitly so operators can
// hand the right URL to each client family without relying on UA-sniff
// guesswork (which fails when a client uses a generic curl-style UA).
//
// Adding a new client family: append an entry below + matching i18n key
// under `inbounds.subFormat.<key>`. UI strings that mention the format count
// read `SUB_FORMATS.length` at render time via {{count}} interpolation, so
// no hardcoded count needs updating elsewhere.

export type SubFormat =
  | "v2ray"
  | "clash"
  | "stash"
  | "singbox"
  | "qx"
  | "surge"
  | "loon";

export interface SubFormatEntry {
  // ?fmt= value the server understands
  fmt: SubFormat;
  // i18n key under `inbounds.subFormat.<key>`
  i18nKey: string;
}

// Order matches the panel's display sequence: V2RayN-style (broadest reach)
// first, then per-vendor formats grouped by ecosystem. Stash sits next to
// Clash because it forks the Clash YAML schema but with a strict validator
// that rejects Mihomo's `password:` key on Hysteria2 nodes.
export const SUB_FORMATS: SubFormatEntry[] = [
  { fmt: "v2ray", i18nKey: "v2ray" },
  { fmt: "clash", i18nKey: "clash" },
  { fmt: "stash", i18nKey: "stash" },
  { fmt: "singbox", i18nKey: "singbox" },
  { fmt: "qx", i18nKey: "qx" },
  { fmt: "surge", i18nKey: "surge" },
  { fmt: "loon", i18nKey: "loon" },
];

// Returns the absolute subscription URL for a given fmt. `base` is either an
// absolute `http://…/sub/<token>` or a path-only `/sub/<token>` (which we
// resolve against window.location.origin).
export function subUrlForFmt(base: string, fmt: SubFormat): string {
  const abs = base.startsWith("http")
    ? base
    : `${window.location.origin}${base}`;
  // PublicSubscription accepts fmt as a query param. v2ray is the default
  // when no fmt is given, but we set it explicitly for parity with the others.
  const sep = abs.includes("?") ? "&" : "?";
  return `${abs}${sep}fmt=${fmt}`;
}
