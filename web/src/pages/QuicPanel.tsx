import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import { Badge, Button, Card, ErrorText, PageHeader, Spinner, Toggle } from "../components/ui";

interface AdvancedQUIC {
  block_quic: boolean;
}

// QuicPanel is the QUIC-hardening tab of the client-inbound page. Blocking
// forwarded QUIC/STUN forces a client's browser HTTP/3 traffic back onto the
// TCP path that CDN / Argo ride — so it belongs on the inbound side, protecting
// those two acceleration channels rather than being a generic firewall rule.
export default function QuicPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ["advanced"],
    queryFn: () => call<AdvancedQUIC>(api.get("/advanced")),
  });

  const [blockQuic, setBlockQuic] = useState(false);
  const [err, setErr] = useState("");
  const [okMsg, setOkMsg] = useState("");

  useEffect(() => {
    if (data) setBlockQuic(!!data.block_quic);
  }, [data]);

  const save = useMutation({
    mutationFn: () => call(api.put("/advanced/quic", { block_quic: blockQuic })),
    onSuccess: () => {
      setOkMsg(t("advanced.savedMsg"));
      qc.invalidateQueries({ queryKey: ["advanced"] });
    },
    onError: (e: any) => setErr(e?.response?.data?.error?.message ?? e.message),
  });

  return (
    <>
      <PageHeader
        title={t("inbound.quicTitle")}
        subtitle={t("inbound.quicSubtitle")}
        action={
          // Badge reflects the SAVED state, not the in-flight toggle — see
          // CdnPanel for the rationale (no on/off flash before persisting).
          <span className="flex items-center gap-2">
            {data && blockQuic !== !!data.block_quic && (
              <span className="text-[11px] text-amber-300">
                {t("advanced.unsavedToggleHint")}
              </span>
            )}
            <Badge
              tone={data?.block_quic ? "success" : "neutral"}
              dot
              solid={!!data?.block_quic}
              size="lg"
            >
              {t("inbound.quicBadge")}
            </Badge>
          </span>
        }
      />

      <Card title={t("advanced.cardHardening")}>
        <div className="space-y-3">
          <Toggle
            checked={blockQuic}
            onChange={setBlockQuic}
            label={t("advanced.blockQuicLabel")}
          />
          <div className="text-xs text-white/40">{t("advanced.blockQuicHint")}</div>
        </div>
      </Card>

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
            t("inbound.quicSave")
          )}
        </Button>
      </div>
    </>
  );
}
