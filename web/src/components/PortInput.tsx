// PortInput renders the right control for a wizard protocol port, given the
// system's reserved-port snapshot + the set of ports already selected in the
// current batch.
//
// Two modes:
//   - cdn=true  → <select> whose options are CFHTTPSWhitelist minus reserved
//                 ports minus ports already claimed elsewhere in this batch.
//                 The operator can never pick a port Cloudflare would refuse
//                 to proxy.
//   - cdn=false → free <input type=number>, min=1024 max=65535, with a soft
//                 informational nudge if the operator happens to land on a
//                 CF whitelist port (won't hurt, but means a future CDN
//                 protocol can't reuse it).
//
// Both modes echo a tiny help line under the field so the operator always sees
// the default + the active range / option set.

import { useTranslation } from "react-i18next";

export interface PortsReservedSnapshot {
  reserved: number[];
  panel_port: number;
  cf_https_whitelist: number[];
  occupied: number[]; // legacy: cross-family total
  occupied_by_family?: { v4: number[]; v6: number[] }; // per-family
  socks_taken: boolean;
  min_allowed: number;
  max_allowed: number;
}

export interface PortInputProps {
  value: number;
  defaultPort: number;
  onChange: (next: number) => void;
  cdn: boolean;
  snapshot?: PortsReservedSnapshot;
  /** Ports the operator has already picked elsewhere in this batch. */
  inBatch: number[];
  /** Family of the chosen host ("v4" / "v6"). Used to filter occupied by
   *  family so v4 SOCKS5:1080 + v6 SOCKS5:1080 both look free in their
   *  respective family's picker. Defaults to "v4" for back-compat. */
  family?: "v4" | "v6";
  /** Disable the field entirely. */
  disabled?: boolean;
}

const FALLBACK_CFHTTPS = [443, 2053, 2083, 2087, 2096, 8443];

export default function PortInput({
  value,
  defaultPort,
  onChange,
  cdn,
  snapshot,
  inBatch,
  family,
  disabled,
}: PortInputProps) {
  const { t } = useTranslation();
  const reserved = snapshot?.reserved ?? [];
  // filter occupied by chosen host family so v4 inbounds don't make
  // v6 ports look taken (and vice versa). Falls back to cross-family
  // occupied when the backend's family-aware payload isn't available
  // (older panel API).
  const occupied = family && snapshot?.occupied_by_family
    ? snapshot.occupied_by_family[family] ?? []
    : snapshot?.occupied ?? [];
  const whitelist = snapshot?.cf_https_whitelist ?? FALLBACK_CFHTTPS;
  const min = snapshot?.min_allowed ?? 1024;
  const max = snapshot?.max_allowed ?? 65535;
  const inBatchOthers = inBatch.filter((p) => p !== value);

  if (cdn) {
    const blocked = new Set<number>([
      ...reserved,
      ...occupied,
      ...inBatchOthers,
    ]);
    // The currently-selected value is always offered so the <select> renders
    // a stable label even when the snapshot says it's "occupied" (it is —
    // by this very inbound).
    const options = whitelist.filter((p) => p === value || !blocked.has(p));
    return (
      <div>
        <select
          className="rounded-md bg-black/[0.05] dark:bg-white/[0.04] border border-black/10 dark:border-white/15 px-2 py-1 text-sm w-full"
          value={value || defaultPort}
          onChange={(e) => onChange(Number(e.target.value))}
          disabled={disabled}
        >
          {options.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
        <div className="text-[11px] text-black/50 dark:text-white/50 mt-1">
          {t("port.cdnHelp", {
            defaultPort,
            whitelist: whitelist.join(" / "),
          })}
          {value === 443 && (
            <div className="mt-0.5 text-amber-600 dark:text-amber-300">
              {t("port.warn443")}
            </div>
          )}
        </div>
      </div>
    );
  }

  const isReserved = reserved.includes(value) || occupied.filter((p) => p !== value).includes(value);
  const isWhitelisted = whitelist.includes(value);
  return (
    <div>
      <input
        type="number"
        className="rounded-md bg-black/[0.05] dark:bg-white/[0.04] border border-black/10 dark:border-white/15 px-2 py-1 text-sm w-full"
        value={value || ""}
        placeholder={String(defaultPort)}
        min={min}
        max={max}
        onChange={(e) => {
          const n = Number(e.target.value);
          onChange(Number.isFinite(n) ? n : defaultPort);
        }}
        disabled={disabled}
      />
      <div className="text-[11px] text-black/50 dark:text-white/50 mt-1">
        {t("port.freeHelp", { defaultPort, min, max })}
      </div>
      {value > 0 && value < min && (
        <div className="text-[11px] text-rose-500 mt-0.5">
          {t("port.errMin", { min })}
        </div>
      )}
      {isReserved && (
        <div className="text-[11px] text-rose-500 mt-0.5">
          {t("port.errReserved", { reserved: [...new Set(reserved)].join(", ") })}
        </div>
      )}
      {isWhitelisted && value !== defaultPort && (
        <div className="text-[11px] text-blue-500 dark:text-blue-300 mt-0.5">
          {t("port.infoCFOnNonCDN", { port: value })}
        </div>
      )}
    </div>
  );
}
