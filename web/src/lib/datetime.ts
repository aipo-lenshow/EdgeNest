// Timezone-aware timestamp formatting. All timestamps are stored as unix epoch
// (server-side truth is UTC); the panel renders them in the operator's chosen
// display timezone. The chosen zone is mirrored to localStorage so the pure
// fmtTime/fmtDate helpers can read it synchronously at render time — Layout
// syncs it from the backend `display_tz` setting (falling back to the detected
// server timezone) on every authed page load.

const TZ_KEY = "edgenest_tz";

// getTz returns the IANA zone fmtTime should use, or "" to fall back to the
// browser's own zone (Intl default).
export function getTz(): string {
  try {
    return localStorage.getItem(TZ_KEY) || "";
  } catch {
    return "";
  }
}

// setTzCache mirrors the effective display zone for the formatters. Pass "" to
// clear (revert to browser default).
export function setTzCache(tz: string) {
  try {
    if (tz) localStorage.setItem(TZ_KEY, tz);
    else localStorage.removeItem(TZ_KEY);
  } catch {
    // localStorage unavailable — formatters fall back to browser zone.
  }
}

function opts(extra?: Intl.DateTimeFormatOptions): Intl.DateTimeFormatOptions | undefined {
  const tz = getTz();
  if (!tz) return extra;
  return { ...extra, timeZone: tz };
}

// fmtTime renders a unix-seconds timestamp as a full local date+time in the
// chosen zone. Returns "—" for a zero/falsy value.
export function fmtTime(unix: number): string {
  if (!unix) return "—";
  const d = new Date(unix * 1000);
  try {
    return d.toLocaleString(undefined, opts());
  } catch {
    // Invalid stored zone — never let a bad setting break the UI.
    return d.toLocaleString();
  }
}

// fmtDate renders a unix-seconds timestamp as a date only, in the chosen zone.
export function fmtDate(unix: number): string {
  if (!unix) return "—";
  const d = new Date(unix * 1000);
  try {
    return d.toLocaleDateString(undefined, opts());
  } catch {
    return d.toLocaleDateString();
  }
}

// listTimezones returns a curated set of "classic" zones spanning the globe,
// ordered west-to-east. Deliberately NOT the full ~400-entry IANA list — that
// was overwhelming. The current UTC offset (DST-aware) is shown in the label.
export function listTimezones(): string[] {
  return [
    "Pacific/Midway",
    "Pacific/Honolulu",
    "America/Anchorage",
    "America/Los_Angeles",
    "America/Denver",
    "America/Chicago",
    "America/New_York",
    "America/Halifax",
    "America/Sao_Paulo",
    "Atlantic/Azores",
    "UTC",
    "Europe/London",
    "Europe/Paris",
    "Europe/Athens",
    "Europe/Moscow",
    "Asia/Dubai",
    "Asia/Karachi",
    "Asia/Kolkata",
    "Asia/Dhaka",
    "Asia/Bangkok",
    "Asia/Shanghai",
    "Asia/Singapore",
    "Asia/Tokyo",
    "Australia/Sydney",
    "Pacific/Auckland",
  ];
}

// tzOffset returns the current "UTC±HH:MM" string for a zone (DST-aware).
export function tzOffset(zone: string): string {
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      timeZoneName: "longOffset",
    }).formatToParts(new Date());
    const raw = parts.find((p) => p.type === "timeZoneName")?.value || "";
    const norm = raw.replace("GMT", "UTC");
    return norm === "UTC" || norm === "" ? "UTC+00:00" : norm;
  } catch {
    return "";
  }
}

// tzLabel renders a zone as "(UTC+08:00) Asia/Shanghai" for the picker.
export function tzLabel(zone: string): string {
  const off = tzOffset(zone);
  return off ? `(${off}) ${zone}` : zone;
}
