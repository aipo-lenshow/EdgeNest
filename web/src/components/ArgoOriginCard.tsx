import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import { Button, Card, ErrorText, Field, Select } from "./ui";

interface Candidate {
  id: number;
  tag: string;
  port: number;
  enabled: boolean;
  argo: boolean;
  edge: boolean;
  users: number;
}

// ArgoOriginCard picks which inbound the tunnel feeds: the dedicated Argo
// inbound, or the CDN inbound itself (one inbound, both entry paths, every CDN
// user also gets a tunnel node).
export default function ArgoOriginCard({ argoDomain }: { argoDomain: string }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const origin = useQuery({
    queryKey: ["argo-origin"],
    queryFn: () => call<{ candidates: Candidate[]; mode: string }>(api.get("/argo/origin")),
  });
  const list = origin.data?.candidates ?? [];
  const current = list.find((c) => c.argo);
  const [pick, setPick] = useState<number>(0);
  const [manual, setManual] = useState(false);
  const [msg, setMsg] = useState("");
  useEffect(() => {
    if (current) setPick(current.id);
  }, [current?.id]);

  const target = list.find((c) => c.id === pick);
  const change = useMutation({
    mutationFn: (ingressDone: boolean) =>
      call<{ moved_users: number; ingress: string }>(
        api.put("/argo/origin", { inbound_id: pick, ingress_done: ingressDone }),
      ),
    onSuccess: (r) => {
      setManual(false);
      setMsg(
        t("advanced.argoOriginDone", { tag: target?.tag ?? "", count: r.moved_users }) +
          (r.ingress === "auto" ? " " + t("advanced.argoOriginIngressAuto") : ""),
      );
      for (const k of ["argo-origin", "inbounds", "users", "argo-status"]) {
        qc.invalidateQueries({ queryKey: [k] });
      }
    },
    onError: (e: any) => {
      if (e?.response?.data?.error?.code === "ARGO_INGRESS_MANUAL") {
        setManual(true);
        return;
      }
      setMsg("");
    },
  });

  // Only worth showing when there is a real choice to make.
  if (list.length < 2 && !list.some((c) => c.edge)) return null;

  const label = (c: Candidate) =>
    `${c.tag} · ${c.edge ? t("advanced.argoOriginKindCdn") : t("advanced.argoOriginKindArgo")} · ${t("advanced.argoOriginUsers", { count: c.users })}${c.enabled ? "" : " · " + t("advanced.argoOriginParked")}`;

  return (
    <Card title={t("advanced.argoOriginTitle")}>
      <div className="grid gap-3">
        <p className="text-xs text-white/60">{t("advanced.argoOriginHint")}</p>
        <Field label={t("advanced.argoOriginField")}>
          <Select
            value={pick || ""}
            onChange={(e) => {
              setPick(Number(e.target.value));
              setManual(false);
              setMsg("");
            }}
          >
            {!current && <option value="">{t("advanced.argoOriginNone")}</option>}
            {list.map((c) => (
              <option key={c.id} value={c.id}>
                {label(c)}
              </option>
            ))}
          </Select>
        </Field>
        {target && current?.id !== target.id && (
          <p className="text-xs text-white/60">
            {target.edge ? t("advanced.argoOriginToCdn") : t("advanced.argoOriginToArgo")}
          </p>
        )}
        {manual && target && (
          <div className="rounded-md border border-amber-400/30 bg-amber-400/10 p-3 text-xs text-amber-200/90 grid gap-2">
            <div>
              {t("advanced.argoOriginManual", {
                domain: argoDomain || "—",
                url: `http://localhost:${target.port}`,
              })}
            </div>
            <div>
              <Button variant="primary" disabled={change.isPending} onClick={() => change.mutate(true)}>
                {t("advanced.argoOriginManualDone")}
              </Button>
            </div>
          </div>
        )}
        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="primary"
            disabled={!target || current?.id === target.id || change.isPending || manual}
            onClick={() => change.mutate(false)}
          >
            {change.isPending ? t("common.saving") : t("advanced.argoOriginApply")}
          </Button>
          {msg && <span className="text-sm text-emerald-300">{msg}</span>}
        </div>
        {change.isError && !manual && (
          <ErrorText>{(change.error as any)?.response?.data?.error?.message ?? String(change.error)}</ErrorText>
        )}
      </div>
    </Card>
  );
}
