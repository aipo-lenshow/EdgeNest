import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { Link, useSearchParams } from "react-router-dom";
import { api, call } from "../api/client";
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  ErrorText,
  Field,
  Input,
  PageHeader,
  Toggle,
} from "../components/ui";

interface Warp {
  enabled: boolean;
  public_key: string;
  address4: string;
  address6: string;
  reserved: number[];
  endpoint: string;
  updated_at: number;
  // private_key is write-only — never returned by GET.
}

export default function WarpPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [searchParams] = useSearchParams();
  const suggestedPreset = searchParams.get("preset"); // deep-linked from Unlock
  const { data, isLoading } = useQuery({
    queryKey: ["warp"],
    queryFn: () => call<Warp>(api.get("/warp")),
  });

  const [enabled, setEnabled] = useState(false);
  const [privateKey, setPrivateKey] = useState("");
  const [publicKey, setPublicKey] = useState("");
  const [address4, setAddress4] = useState("");
  const [address6, setAddress6] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [reserved, setReserved] = useState("");
  const [err, setErr] = useState("");
  const [okMsg, setOkMsg] = useState("");
  const [confirmClearOpen, setConfirmClearOpen] = useState(false);
  const [showManual, setShowManual] = useState(false);
  const [showWhat, setShowWhat] = useState(false);
  const [expandedPreset, setExpandedPreset] = useState<string | null>(null);

  useEffect(() => {
    if (data) {
      setEnabled(!!data.enabled);
      setPublicKey(data.public_key ?? "");
      setAddress4(data.address4 ?? "");
      setAddress6(data.address6 ?? "");
      setEndpoint(data.endpoint ?? "");
      setReserved((data.reserved ?? []).join(","));
    }
  }, [data]);

  const save = useMutation({
    mutationFn: () => {
      const body: any = {
        enabled,
        public_key: publicKey,
        address4,
        address6,
        endpoint,
        reserved: reserved
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean)
          .map(Number)
          .filter((n) => Number.isFinite(n)),
      };
      // Empty private_key on edit = preserve existing (write-only semantics).
      if (privateKey.trim() !== "") body.private_key = privateKey.trim();
      return call(api.put("/warp", body));
    },
    onSuccess: () => {
      setOkMsg(t("warp.savedMsg"));
      setPrivateKey("");
      qc.invalidateQueries({ queryKey: ["warp"] });
    },
    onError: (e: any) =>
      setErr(e?.response?.data?.error?.message ?? e.message),
  });

  const del = useMutation({
    mutationFn: () => call(api.delete("/warp")),
    onSuccess: () => {
      setOkMsg(t("warp.clearedMsg"));
      qc.invalidateQueries({ queryKey: ["warp"] });
    },
  });

  const register = useMutation({
    mutationFn: () => call<Warp>(api.post("/warp/register")),
    onSuccess: (fresh) => {
      // Cloudflare returns the full config sans private_key (write-only
      // semantics still apply). The newly minted private key is already in
      // the DB; leaving the form field empty signals "preserve existing"
      // on the next Save.
      setPublicKey(fresh.public_key ?? "");
      setAddress4(fresh.address4 ?? "");
      setAddress6(fresh.address6 ?? "");
      setEndpoint(fresh.endpoint ?? "");
      setReserved((fresh.reserved ?? []).join(","));
      setEnabled(false); // operator must opt-in after reviewing
      setOkMsg(t("warp.registeredMsg"));
      qc.invalidateQueries({ queryKey: ["warp"] });
    },
    onError: (e: any) =>
      setErr(e?.response?.data?.error?.message ?? e.message),
  });

  const presets = useQuery({
    queryKey: ["route-presets"],
    queryFn: () =>
      call<{ key: string; name: string; domains: string[]; recommend?: string }[]>(
        api.get("/routes/presets"),
      ),
    // This card always routes through WARP, so only surface the categories that
    // make sense via WARP (geo-restricted services). China-direct / ad-block
    // categories live on the Routes page, where the outbound is selectable.
    select: (all) => all.filter((p) => !p.recommend || p.recommend === "warp"),
  });

  // Current route rules — used to show each preset's applied status (how many of
  // its domains already route through WARP) so the operator doesn't have to
  // click into Routes to find out.
  const routes = useQuery({
    queryKey: ["routes"],
    queryFn: () =>
      call<{ type: string; value: string; outbound: string }[]>(
        api.get("/routes"),
      ),
  });
  const warpSuffixes = new Set(
    (routes.data ?? [])
      .filter((r) => r.type === "domain_suffix" && r.outbound === "warp")
      .map((r) => r.value),
  );
  const appliedCount = (domains: string[]) =>
    domains.filter((d) => warpSuffixes.has(d)).length;

  const applyPreset = useMutation({
    mutationFn: (group: string) =>
      call<{ added: number; skipped: number }>(
        api.post("/routes/presets/apply", { group, outbound: "warp" }),
      ),
    onSuccess: (res) => {
      setErr("");
      setOkMsg(
        t("warp.presetApplied", { added: res.added, skipped: res.skipped }),
      );
      qc.invalidateQueries({ queryKey: ["routes"] });
    },
    onError: (e: any) =>
      setErr(e?.response?.data?.error?.message ?? e.message),
  });

  // Localised label for a preset group; falls back to the server-provided name.
  const presetLabel = (key: string, fallback: string) => {
    const k = `warp.preset_${key}`;
    const v = t(k);
    return v === k ? fallback : v;
  };

  if (isLoading) return <div className="text-white/60">{t("warp.loading")}</div>;

  // Registered = the server holds a WARP config (public key / address filled by
  // /warp/register). Drives the guided flow: register → enable → apply preset.
  const registered = !!(data && (data.public_key || data.address4));

  return (
    <>
      <PageHeader
        title={t("warp.pageTitle")}
        subtitle={t("warp.pageSubtitle")}
        action={
          data?.enabled ? (
            <Badge tone="success" dot solid size="lg">
              {t("warp.badgeEnabled")}
            </Badge>
          ) : (
            <Badge tone="neutral" dot size="lg">
              {t("warp.badgeDisabled")}
            </Badge>
          )
        }
      />

      {/* Step 1/2: status + the single next action (register, then enable). */}
      <Card title={t("warp.cardSetup")}>
        <div className="space-y-4">
          <div
            className={
              "flex items-start gap-2.5 rounded-md border p-3 text-sm " +
              (data?.enabled
                ? "border-emerald-400/50 bg-emerald-500/20 text-emerald-300"
                : registered
                  ? "border-amber-400/50 bg-amber-500/20 text-amber-300"
                  : "border-white/15 bg-white/10 text-white/70")
            }
          >
            <span
              className={
                "mt-1 h-2.5 w-2.5 shrink-0 rounded-full " +
                (data?.enabled
                  ? "animate-pulse bg-emerald-400"
                  : registered
                    ? "bg-amber-400"
                    : "bg-white/40")
              }
            />
            <span>
              {data?.enabled
                ? t("warp.statusEnabled")
                : registered
                  ? t("warp.statusRegisteredOff")
                  : t("warp.statusUnregistered")}
            </span>
          </div>

          {!registered ? (
            <Button
              variant="primary"
              disabled={register.isPending}
              onClick={() => {
                setErr("");
                setOkMsg("");
                register.mutate();
              }}
            >
              {register.isPending ? t("warp.btnRegistering") : t("warp.btnRegisterBig")}
            </Button>
          ) : (
            <div className="flex flex-wrap items-center gap-4">
              <Toggle
                checked={enabled}
                onChange={setEnabled}
                label={t("warp.enableLabel")}
              />
              <Button
                variant="primary"
                disabled={save.isPending}
                onClick={() => {
                  setErr("");
                  setOkMsg("");
                  save.mutate();
                }}
              >
                {save.isPending ? t("warp.btnSaving") : t("warp.btnSave")}
              </Button>
              <Button
                variant="default"
                disabled={register.isPending}
                onClick={() => {
                  setErr("");
                  setOkMsg("");
                  register.mutate();
                }}
              >
                {register.isPending ? t("warp.btnRegistering") : t("warp.btnReRegister")}
              </Button>
            </div>
          )}

          <ErrorText>{err}</ErrorText>
          {okMsg && !applyPreset.isSuccess && (
            <div className="text-sm text-emerald-400">{okMsg}</div>
          )}

          <button
            type="button"
            className="text-xs text-black/55 dark:text-white/50 hover:text-black dark:hover:text-white"
            onClick={() => setShowWhat((v) => !v)}
          >
            {showWhat ? "▾ " : "▸ "}
            {t("warp.whatIsToggle")}
          </button>
          {showWhat && (
            <div className="rounded-md bg-black/20 p-3 text-xs leading-relaxed whitespace-pre-line text-white/70">
              {t("warp.whatIsBody")}
            </div>
          )}
        </div>
      </Card>


      <div className="mt-4">
        <button
          type="button"
          className="text-sm text-black/60 dark:text-white/60 hover:text-black dark:hover:text-white"
          onClick={() => setShowManual((v) => !v)}
        >
          {showManual ? "▾ " : "▸ "}
          {t("warp.toggleManual")}
        </button>
      </div>

      {showManual && (
      <Card title={t("warp.cardConfiguration")}>
        <div className="space-y-4">
          <div className="rounded-md border border-white/10 bg-white/5 p-3 text-xs leading-relaxed text-white/60">
            {t("warp.manualHint")}
          </div>
          <div className="grid grid-cols-2 gap-4">
            <Field
              label={t("warp.fieldPrivateKey")}
              hint={t("warp.fieldPrivateKeyHint")}
            >
              <Input
                type="password"
                value={privateKey}
                onChange={(e) => setPrivateKey(e.target.value)}
                placeholder={data?.enabled ? t("warp.unchangedPlaceholder") : ""}
              />
            </Field>
            <Field label={t("warp.fieldPublicKey")}>
              <Input
                value={publicKey}
                onChange={(e) => setPublicKey(e.target.value)}
              />
            </Field>
            <Field label={t("warp.fieldAddress4")}>
              <Input
                value={address4}
                onChange={(e) => setAddress4(e.target.value)}
                placeholder={t("warp.autoIssuedPlaceholder")}
              />
            </Field>
            <Field label={t("warp.fieldAddress6")}>
              <Input
                value={address6}
                onChange={(e) => setAddress6(e.target.value)}
                placeholder={t("warp.autoIssuedPlaceholder")}
              />
            </Field>
            <Field
              label={t("warp.fieldEndpoint")}
              hint={t("warp.fieldEndpointHint")}
            >
              <Input
                value={endpoint}
                onChange={(e) => setEndpoint(e.target.value)}
              />
            </Field>
            <Field label={t("warp.fieldReserved")} hint={t("warp.fieldReservedHint")}>
              <Input
                value={reserved}
                onChange={(e) => setReserved(e.target.value)}
              />
            </Field>
          </div>

          <div className="flex flex-wrap gap-2">
            <Button
              variant="primary"
              disabled={save.isPending}
              onClick={() => {
                setErr("");
                setOkMsg("");
                save.mutate();
              }}
            >
              {save.isPending ? t("warp.btnSaving") : t("warp.btnSave")}
            </Button>
            <Button
              variant="danger"
              disabled={del.isPending}
              onClick={() => setConfirmClearOpen(true)}
            >
              {t("warp.btnClear")}
            </Button>
          </div>
          <div className="text-xs text-white/40">
            {t("warp.registerHint")}
          </div>
        </div>
      </Card>
      )}

      <div className="mt-4 text-xs text-white/40">
        <Trans
          i18nKey="warp.tipFooter"
          components={{ c: <code /> }}
        />
      </div>

      <ConfirmDialog
        open={confirmClearOpen}
        title={t("warp.btnClear")}
        body={t("warp.confirmClear")}
        confirmLabel={t("common.confirm")}
        cancelLabel={t("common.cancel")}
        variant="danger"
        busy={del.isPending}
        onCancel={() => setConfirmClearOpen(false)}
        onConfirm={() => {
          del.mutate(undefined, {
            onSettled: () => setConfirmClearOpen(false),
          });
        }}
      />
    </>
  );
}
