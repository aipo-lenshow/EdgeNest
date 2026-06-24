import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import Layout from "../components/Layout";
import {
  Button,
  ConfirmDialog,
  ErrorText,
  Field,
  PageHeader,
  Select,
  Toggle,
  fmtTime,
} from "../components/ui";

interface AuditRow {
  id: number;
  actor: string;
  action: string;
  resource: string;
  ip: string;
  meta: string;
  created_at: number;
}

const LIMIT_OPTIONS = [50, 100, 200, 500, 1000];

// AuditPage shows the operation history. Standalone or embedded inside the
// Monitor page's "audit" tab (parent supplies the page header).
export default function AuditPage({ embedded = false }: { embedded?: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [action, setAction] = useState("");
  const [actor, setActor] = useState("");
  const [limit, setLimit] = useState(200);
  const [confirmClear, setConfirmClear] = useState(false);
  const [flash, setFlash] = useState<{ kind: "ok" | "err"; text: string } | null>(
    null,
  );

  // The action filter offers the known human-readable labels rather than raw
  // dotted keys; sort by display label so the dropdown reads naturally.
  const actionMap = t("audit.actions", { returnObjects: true }) as Record<
    string,
    string
  >;
  const actionOptions = useMemo(
    () =>
      Object.entries(actionMap).sort((a, b) =>
        a[1].localeCompare(b[1]),
      ),
    [actionMap],
  );
  const labelFor = (a: string) => actionMap[a] ?? a;
  const actorLabel = (a: string) => (a === "system" ? t("audit.actorSystem") : a);

  const { data = [], refetch, isFetching } = useQuery({
    queryKey: ["audit", action, actor, limit],
    queryFn: () => {
      const params = new URLSearchParams();
      if (action) params.set("action", action);
      if (actor) params.set("actor", actor);
      params.set("limit", String(limit));
      return call<AuditRow[]>(api.get(`/audit?${params.toString()}`));
    },
  });

  const actors = useQuery({
    queryKey: ["audit-actors"],
    queryFn: () => call<{ actors: string[] }>(api.get("/audit/actors")),
  });

  const config = useQuery({
    queryKey: ["audit-config"],
    queryFn: () => call<{ enabled: boolean }>(api.get("/audit/config")),
  });

  const toggle = useMutation({
    mutationFn: (enabled: boolean) =>
      call(api.put("/audit/config", { enabled })),
    onSuccess: (_d, enabled) => {
      setFlash({
        kind: "ok",
        text: enabled ? t("audit.enabledSaved") : t("audit.disabledSaved"),
      });
      qc.invalidateQueries({ queryKey: ["audit-config"] });
      refetch();
    },
    onError: (e: Error) => setFlash({ kind: "err", text: e.message }),
  });

  const clear = useMutation({
    mutationFn: () => call<{ cleared: number }>(api.post("/audit/clear", {})),
    onSuccess: (d) => {
      setFlash({ kind: "ok", text: t("audit.cleared", { n: d.cleared ?? 0 }) });
      refetch();
      actors.refetch();
    },
    onError: (e: Error) => setFlash({ kind: "err", text: e.message }),
  });

  const enabled = config.data?.enabled ?? true;

  const content = (
    <>
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <div className="grid gap-1">
          <Toggle
            checked={enabled}
            onChange={(v) => toggle.mutate(v)}
            disabled={config.isLoading || toggle.isPending}
            pendingLabel={t("common.saving")}
            label={t("audit.enableLabel")}
          />
          <p className="text-xs text-white/50 max-w-xl">{t("audit.enableHint")}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button onClick={() => refetch()} disabled={isFetching}>
            {isFetching ? t("audit.refreshing") : t("audit.refresh")}
          </Button>
          <Button
            variant="default"
            disabled={clear.isPending}
            onClick={() => setConfirmClear(true)}
          >
            {t("audit.clearBtn")}
          </Button>
        </div>
      </div>

      {flash?.kind === "ok" && (
        <p className="mb-3 text-sm text-emerald-300">{flash.text}</p>
      )}
      {flash?.kind === "err" && (
        <div className="mb-3">
          <ErrorText>{flash.text}</ErrorText>
        </div>
      )}

      <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 mb-4">
        <Field label={t("audit.filterAction")}>
          <Select value={action} onChange={(e) => setAction(e.target.value)}>
            <option value="">{t("audit.filterAll")}</option>
            {actionOptions.map(([key, label]) => (
              <option key={key} value={key}>
                {label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("audit.filterActor")}>
          <Select value={actor} onChange={(e) => setActor(e.target.value)}>
            <option value="">{t("audit.filterAll")}</option>
            {(actors.data?.actors ?? []).map((a) => (
              <option key={a} value={a}>
                {actorLabel(a)}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("audit.filterLimit")}>
          <Select
            value={String(limit)}
            onChange={(e) => setLimit(Number(e.target.value))}
          >
            {LIMIT_OPTIONS.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </Select>
        </Field>
      </div>

      <div className="rounded-2xl border border-white/10 bg-white/[0.03] overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-white/5 text-white/50 text-xs uppercase">
            <tr>
              <th className="px-3 py-2 text-left w-44">{t("audit.when")}</th>
              <th className="px-3 py-2 text-left w-24">{t("audit.actor")}</th>
              <th className="px-3 py-2 text-left w-48">{t("audit.action")}</th>
              <th className="px-3 py-2 text-left">{t("audit.resource")}</th>
              <th className="px-3 py-2 text-left w-32">{t("audit.ip")}</th>
              <th className="px-3 py-2 text-left">{t("audit.meta")}</th>
            </tr>
          </thead>
          <tbody>
            {data.length === 0 && (
              <tr>
                <td
                  colSpan={6}
                  className="px-3 py-8 text-center text-white/40 text-sm"
                >
                  {t("audit.empty")}
                </td>
              </tr>
            )}
            {data.map((r) => (
              <tr key={r.id} className="border-t border-white/5 align-top">
                <td className="px-3 py-2 text-white/60 text-xs">
                  {fmtTime(r.created_at)}
                </td>
                <td className="px-3 py-2 text-xs">{actorLabel(r.actor)}</td>
                <td className="px-3 py-2 text-xs">{labelFor(r.action)}</td>
                <td className="px-3 py-2 font-mono text-xs text-white/60">
                  {r.resource}
                </td>
                <td className="px-3 py-2 font-mono text-xs text-white/60">
                  {r.ip}
                </td>
                <td className="px-3 py-2 font-mono text-xs text-white/50 break-all max-w-md">
                  {r.meta}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <ConfirmDialog
        open={confirmClear}
        title={t("audit.clearConfirmTitle")}
        body={t("audit.clearConfirmBody")}
        variant="danger"
        confirmLabel={t("audit.clearBtn")}
        cancelLabel={t("routes.btnCancel")}
        onConfirm={() => {
          setConfirmClear(false);
          clear.mutate();
        }}
        onCancel={() => setConfirmClear(false)}
      />
    </>
  );

  if (embedded) return content;

  return (
    <Layout>
      <PageHeader title={t("audit.title")} subtitle={t("audit.subtitle")} />
      {content}
    </Layout>
  );
}
