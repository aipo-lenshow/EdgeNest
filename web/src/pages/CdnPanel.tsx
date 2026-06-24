import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import CdnCard from "../components/CdnCard";
import { Badge, Button, ErrorText, PageHeader, Spinner } from "../components/ui";

interface AdvancedCDN {
  cdn_enabled: boolean;
  cdn_preferred_ips: string[];
}

// CdnPanel is the CDN-優選IP tab of the client-inbound page. It owns only the
// CDN slice of AdvancedConfig and saves it through PUT /advanced/cdn — a
// granular write that never touches (or is blocked by) the Argo config.
export default function CdnPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ["advanced"],
    queryFn: () => call<AdvancedCDN>(api.get("/advanced")),
  });

  const [enabled, setEnabled] = useState(false);
  const [ips, setIps] = useState<string[]>([]);
  const [err, setErr] = useState("");
  const [okMsg, setOkMsg] = useState("");

  useEffect(() => {
    if (data) {
      setEnabled(!!data.cdn_enabled);
      setIps(data.cdn_preferred_ips ?? []);
    }
  }, [data]);

  const save = useMutation({
    mutationFn: () =>
      call(
        api.put("/advanced/cdn", {
          cdn_enabled: enabled,
          cdn_preferred_ips: ips,
        }),
      ),
    onSuccess: () => {
      setOkMsg(t("advanced.savedMsg"));
      qc.invalidateQueries({ queryKey: ["advanced"] });
    },
    onError: (e: any) => setErr(e?.response?.data?.error?.message ?? e.message),
  });

  return (
    <>
      <PageHeader
        title={t("inbound.cdnTitle")}
        subtitle={t("inbound.cdnSubtitle")}
        action={
          // Badge reflects the SAVED state (data.cdn_enabled), not the in-flight
          // toggle — otherwise flipping the switch would flash the badge on/off
          // before persisting, implying a change that isn't live yet. The amber
          // "unsaved" note covers the gap until Save.
          <span className="flex items-center gap-2">
            {data && enabled !== !!data.cdn_enabled && (
              <span className="text-[11px] text-amber-300">
                {t("advanced.unsavedToggleHint")}
              </span>
            )}
            <Badge
              tone={data?.cdn_enabled ? "success" : "neutral"}
              dot
              solid={!!data?.cdn_enabled}
              size="lg"
            >
              {t("inbound.cdnBadge")}
            </Badge>
          </span>
        }
      />

      <CdnCard
        enabled={enabled}
        onEnabledChange={setEnabled}
        ips={ips}
        onIpsChange={setIps}
      />

      <ErrorText>{err}</ErrorText>
      {okMsg && <div className="mt-3 text-sm text-emerald-400">{okMsg}</div>}

      <div className="mt-5">
        <Button
          variant="primary"
          disabled={save.isPending}
          onClick={() => {
            setErr("");
            setOkMsg("");
            save.mutate();
          }}
        >
          {save.isPending ? (
            <span className="inline-flex items-center gap-2">
              <Spinner /> {t("advanced.btnSaving")}
            </span>
          ) : (
            t("inbound.cdnSave")
          )}
        </Button>
      </div>
    </>
  );
}
