import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import { Button, ErrorText } from "./ui";

interface Candidate {
  domain: string;
  ip: string;
  latency_ms: number;
}

interface NeighborsView {
  result: { server_ip: string; scanned_at: number; candidates: Candidate[] };
  fallback: string;
}

function errCode(e: unknown): string {
  const ax = e as { response?: { data?: { error?: { code?: string } } } };
  return ax?.response?.data?.error?.code ?? "";
}

// RealityTargetPicker offers sites in the server's own network as the Reality
// disguise. A server claiming to be a global brand while sitting in an
// ordinary data centre is easy to spot; a neighbour on the same /24 matches
// the address owner. `value` is the current SNI; picking calls onPick.
export default function RealityTargetPicker({
  value,
  onPick,
  editing = false,
}: {
  value: string;
  onPick: (domain: string) => void;
  editing?: boolean;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["reality-neighbors"],
    queryFn: () => call<NeighborsView>(api.get("/reality/neighbors")),
  });
  const scan = useMutation({
    mutationFn: () =>
      call<NeighborsView>(api.post("/reality/neighbors/scan", {}, { timeout: 200_000 })),
    onSuccess: (data) => qc.setQueryData(["reality-neighbors"], data),
  });

  const result = q.data?.result;
  const candidates = result?.candidates ?? [];
  const scanned = (result?.scanned_at ?? 0) > 0;
  const isNeighbor = candidates.some((c) => c.domain === value);
  const scanErr =
    scan.isError &&
    (errCode(scan.error) === "NO_IPV4"
      ? t("inbounds.realityNoIPv4")
      : errCode(scan.error) === "SCAN_BUSY"
        ? t("inbounds.realityScanBusy")
        : t("inbounds.realityScanFailed"));

  return (
    <div className="col-span-2 rounded-md border border-white/10 bg-white/5 p-3 text-sm">
      <div className="font-medium text-white/90">{t("inbounds.realityPickTitle")}</div>
      <p className="mt-1 text-xs leading-relaxed text-white/50">{t("inbounds.realityPickHint")}</p>

      {value && candidates.length > 0 && !isNeighbor && (
        <p className="mt-2 text-xs text-amber-300">
          {t("inbounds.realityNotNeighbor", { sni: value })}
        </p>
      )}

      {candidates.length > 0 ? (
        <ul className="mt-2 space-y-1">
          {candidates.map((c) => (
            <li key={c.domain} className="flex items-center justify-between gap-2">
              <span className="font-mono text-xs text-white/80 break-all">
                {c.domain}
                <span className="ml-2 text-white/40">{t("inbounds.realityLatency", { ms: c.latency_ms })}</span>
              </span>
              {c.domain === value ? (
                <span className="text-xs text-emerald-300">{t("inbounds.realityCurrent")}</span>
              ) : (
                <Button type="button" variant="ghost" onClick={() => onPick(c.domain)}>
                  {t("inbounds.realityUse")}
                </Button>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p className="mt-2 text-xs text-white/50">
          {scanned
            ? t("inbounds.realityNeighborsEmpty", { fallback: q.data?.fallback ?? "" })
            : t("inbounds.realityNotScanned")}
        </p>
      )}

      <div className="mt-2 flex items-center gap-3">
        <Button type="button" variant="ghost" onClick={() => scan.mutate()} disabled={scan.isPending}>
          {scan.isPending ? t("inbounds.realityScanning") : t("inbounds.realityScan")}
        </Button>
        {scanErr && <ErrorText>{scanErr}</ErrorText>}
      </div>
      {editing && <p className="mt-2 text-xs text-white/40">{t("inbounds.realityEditNote")}</p>}
    </div>
  );
}
