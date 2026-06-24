import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, call } from "../api/client";
import {
  Button,
  Badge,
  Card,
  Modal,
  ConfirmDialog,
  Field,
  Input,
  Toggle,
  ErrorText,
  fmtBytes,
  fmtTime,
} from "../components/ui";
import {
  QuotaExpiryFields,
  quotaToBytes,
  bytesToQuota,
  type QuotaUnit,
} from "../components/QuotaExpiryFields";

export interface UserRow {
  email: string;
  inbound_tags: string[];
  inbound_ids: number[];
  inbound_count: number;
  traffic_up: number;
  traffic_down: number;
  quota_bytes: number;
  quota_used_pct: number;
  expiry_at: number;
  enabled: boolean;
  over_quota: boolean;
  sub_id: number;
}

interface InboundLite {
  id: number;
  tag: string;
  type: string;
  enabled: boolean;
}


// MultiUserPanel is the user-centric view inside the Connections page. A "user"
// is the shared Client.Email across inbounds; quota/expiry are enforced per
// user (soft quota — see help note). Lives as a panel (Connections.tsx renders
// it under the 多用户 tab; no Layout/PageHeader of its own).
export default function MultiUserPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [enforceMsg, setEnforceMsg] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<UserRow | null>(null);
  const [deleting, setDeleting] = useState<UserRow | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ["users"],
    queryFn: () => call<{ users: UserRow[] }>(api.get("/users")),
  });
  const users = data?.users ?? [];

  const enforce = useMutation({
    mutationFn: () => call<{ disabled?: { email: string }[] }>(api.post("/quota/enforce")),
    onSuccess: (res) => {
      // `disabled` is per-client (one user × N inbounds = N rows); report the
      // distinct user count, which is what the operator thinks in.
      const users = new Set((res?.disabled ?? []).map((d) => d.email)).size;
      setEnforceMsg(t("multiUser.enforceDone", { n: users }));
      qc.invalidateQueries({ queryKey: ["users"] });
    },
    onError: (e: any) => setEnforceMsg(e?.response?.data?.error?.message ?? e.message),
  });

  const del = useMutation({
    mutationFn: (email: string) => call(api.delete(`/users/${encodeURIComponent(email)}`)),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["users"] });
      setDeleting(null);
    },
  });

  const now = Math.floor(Date.now() / 1000);

  return (
    <div>
      <details className="mb-4 rounded-xl border border-white/10 bg-white/[0.03]">
        <summary className="cursor-pointer select-none px-4 py-3 text-sm font-medium text-white/80">
          {t("multiUser.helpTitle")}
        </summary>
        <div className="px-4 pb-4 text-sm text-white/70 space-y-2 border-t border-white/5 pt-3">
          <p>{t("multiUser.helpIntro")}</p>
          <p className="text-amber-200/90">{t("multiUser.helpSoftQuota")}</p>
        </div>
      </details>

      <div className="flex items-center gap-3 mb-3">
        <Button variant="primary" onClick={() => setCreating(true)}>
          {t("multiUser.btnNew")}
        </Button>
        <Button
          onClick={() => {
            setEnforceMsg(null);
            enforce.mutate();
          }}
          disabled={enforce.isPending}
        >
          {enforce.isPending ? t("multiUser.enforcing") : t("multiUser.btnEnforce")}
        </Button>
        {enforceMsg && <span className="text-xs text-white/60">{enforceMsg}</span>}
      </div>

      {isLoading && <p className="text-white/50">{t("common.loading")}</p>}
      {!isLoading && users.length === 0 && (
        <Card>
          <p className="text-white/60">{t("multiUser.empty")}</p>
        </Card>
      )}

      {users.length > 0 && (
        <div className="overflow-x-auto rounded-xl border border-white/10">
          <table className="w-full text-sm">
            <thead className="bg-white/5 text-white/50 text-xs uppercase">
              <tr>
                <th className="px-3 py-2 text-left">{t("multiUser.colUser")}</th>
                <th className="px-3 py-2 text-left w-24">{t("multiUser.colInbounds")}</th>
                <th className="px-3 py-2 text-left w-48">{t("multiUser.colUsage")}</th>
                <th className="px-3 py-2 text-left w-44">{t("multiUser.colExpiry")}</th>
                <th className="px-3 py-2 text-left w-20">{t("multiUser.colState")}</th>
                <th className="px-3 py-2 text-right w-40">{t("multiUser.colActions")}</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => {
                const expired = u.expiry_at > 0 && u.expiry_at < now;
                return (
                  <tr key={u.email} className="border-t border-white/5">
                    <td className="px-3 py-2 font-mono text-white/85">{u.email}</td>
                    <td className="px-3 py-2 text-white/60">{u.inbound_count}</td>
                    <td className="px-3 py-2 text-white/70">
                      {fmtBytes(u.traffic_up + u.traffic_down)}
                      {u.quota_bytes > 0 ? (
                        <span className={u.over_quota ? "text-rose-300" : "text-white/40"}>
                          {" / "}
                          {fmtBytes(u.quota_bytes)} ({Math.floor(u.quota_used_pct)}%)
                        </span>
                      ) : (
                        <span className="text-white/40"> / {t("multiUser.unlimited")}</span>
                      )}
                    </td>
                    <td className="px-3 py-2 text-white/70">
                      {u.expiry_at ? fmtTime(u.expiry_at) : t("multiUser.never")}
                    </td>
                    <td className="px-3 py-2">
                      {!u.enabled ? (
                        <Badge tone="danger">{t("multiUser.stateDisabled")}</Badge>
                      ) : expired ? (
                        <Badge tone="warn">{t("multiUser.stateExpired")}</Badge>
                      ) : u.over_quota ? (
                        <Badge tone="warn">{t("multiUser.stateOverQuota")}</Badge>
                      ) : (
                        <Badge tone="success">{t("multiUser.stateActive")}</Badge>
                      )}
                    </td>
                    <td className="px-3 py-2 text-right">
                      <div className="inline-flex gap-1">
                        <Button onClick={() => setEditing(u)}>{t("multiUser.btnEdit")}</Button>
                        <Button variant="danger" onClick={() => setDeleting(u)}>
                          {t("multiUser.btnDelete")}
                        </Button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {creating && (
        <CreateUserModal
          onClose={() => setCreating(false)}
          onSaved={() => {
            setCreating(false);
            qc.invalidateQueries({ queryKey: ["users"] });
            qc.invalidateQueries({ queryKey: ["subscriptions"] });
          }}
        />
      )}
      {editing && (
        <EditUserModal
          user={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            qc.invalidateQueries({ queryKey: ["users"] });
          }}
        />
      )}
      <ConfirmDialog
        open={deleting !== null}
        title={t("multiUser.btnDelete")}
        body={t("multiUser.confirmDelete", { email: deleting?.email ?? "" })}
        confirmLabel={t("common.confirm")}
        cancelLabel={t("common.cancel")}
        variant="danger"
        busy={del.isPending}
        onCancel={() => setDeleting(null)}
        onConfirm={() => deleting && del.mutate(deleting.email)}
      />
    </div>
  );
}

function CreateUserModal({
  onClose,
  onSaved,
}: {
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [email, setEmail] = useState("");
  const [quotaVal, setQuotaVal] = useState("");
  const [unit, setUnit] = useState<QuotaUnit>("G");
  const [expiryDays, setExpiryDays] = useState("0");
  const [selected, setSelected] = useState<Set<number> | null>(null); // null = all
  const [err, setErr] = useState("");
  const [done, setDone] = useState<{ tags: string[]; skipped: string[] } | null>(null);

  const { data: inboundsData } = useQuery({
    queryKey: ["inbounds"],
    queryFn: () => call<InboundLite[]>(api.get("/inbounds")),
  });
  // SS-2022 / SOCKS can't host extra users — don't even list them.
  const inbounds = (inboundsData ?? []).filter(
    (i) => i.type !== "shadowsocks" && i.type !== "socks",
  );

  const create = useMutation({
    mutationFn: () =>
      call<{ inbound_tags: string[]; skipped: string[] }>(
        api.post("/users", {
          email: email.trim() || undefined,
          quota_bytes: quotaToBytes(quotaVal, unit),
          expiry_days: parseInt(expiryDays || "0", 10),
          inbound_ids: selected ? Array.from(selected) : inbounds.map((i) => i.id),
        }),
      ),
    onSuccess: (res) => setDone({ tags: res.inbound_tags ?? [], skipped: res.skipped ?? [] }),
    onError: (e: any) => setErr(e?.response?.data?.error?.message ?? e.message),
  });

  const toggle = (id: number) => {
    setSelected((prev) => {
      const base = prev ?? new Set(inbounds.map((i) => i.id));
      const next = new Set(base);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };
  const isChecked = (id: number) => (selected ? selected.has(id) : true);

  return (
    <Modal
      open
      onClose={onClose}
      title={t("multiUser.newTitle")}
      size="lg"
      footer={
        done ? (
          <Button variant="primary" onClick={onSaved}>
            {t("multiUser.btnDoneClose")}
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button
              variant="primary"
              disabled={create.isPending}
              onClick={() => {
                setErr("");
                create.mutate();
              }}
            >
              {create.isPending ? t("multiUser.creating") : t("multiUser.btnCreate")}
            </Button>
          </>
        )
      }
    >
      {done ? (
        <div className="space-y-3 text-sm">
          <div className="rounded-lg border border-emerald-400/30 bg-emerald-400/10 px-3 py-2 text-emerald-100">
            {t("multiUser.createdMsg", { n: done.tags.length })}
          </div>
          {done.skipped.length > 0 && (
            <div className="rounded-lg border border-amber-400/30 bg-amber-400/10 px-3 py-2 text-amber-100">
              {t("multiUser.skippedMsg", { tags: done.skipped.join(", ") })}
            </div>
          )}
          <p className="text-white/60">{t("multiUser.createdSubHint")}</p>
        </div>
      ) : (
        <div className="space-y-4">
          <Field label={t("multiUser.fieldIdentifier")}>
            <Input
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder={t("multiUser.identifierPlaceholder")}
            />
          </Field>

          <Field label={t("multiUser.fieldInbounds")} hint={t("multiUser.inboundsHint")}>
            <div className="space-y-1 max-h-48 overflow-y-auto rounded-lg border border-white/10 p-2">
              {inbounds.length === 0 && (
                <div className="text-white/40 text-xs px-1">{t("multiUser.noInbounds")}</div>
              )}
              {inbounds.map((ib) => (
                <label key={ib.id} className="flex items-center gap-2 cursor-pointer px-1 py-0.5">
                  <input type="checkbox" checked={isChecked(ib.id)} onChange={() => toggle(ib.id)} />
                  <span className="font-mono text-xs text-white/80">{ib.tag}</span>
                  {!ib.enabled && (
                    <span className="text-[10px] text-white/40">({t("multiUser.inboundDisabled")})</span>
                  )}
                </label>
              ))}
            </div>
          </Field>

          <QuotaExpiryFields
            quotaValue={quotaVal}
            setQuotaValue={setQuotaVal}
            unit={unit}
            setUnit={setUnit}
            expiryDays={expiryDays}
            setExpiryDays={setExpiryDays}
          />

          <ErrorText>{err}</ErrorText>
        </div>
      )}
    </Modal>
  );
}

export function EditUserModal({
  user,
  onClose,
  onSaved,
}: {
  user: UserRow;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const init = bytesToQuota(user.quota_bytes);
  const [identifier, setIdentifier] = useState(user.email);
  const [quotaVal, setQuotaVal] = useState(init.value);
  const [unit, setUnit] = useState<QuotaUnit>(init.unit);
  const [enabled, setEnabled] = useState(user.enabled);
  const [resetUsage, setResetUsage] = useState(false);
  const [expiryDays, setExpiryDays] = useState(""); // "" = leave unchanged
  const [members, setMembers] = useState<Set<number> | null>(null); // null = not loaded
  const [err, setErr] = useState("");

  const isExpired = user.expiry_at > 0 && user.expiry_at < Math.floor(Date.now() / 1000);
  // Re-enabling a user whose expiry has already passed only sticks if we also
  // clear the stale past expiry — otherwise the next check disables them again.
  // Prefill the expiry field to 0 (never) when flipping enable on for such a
  // user, so the reset is visible (the backend also enforces this as a safety net).
  const onToggleEnabled = (v: boolean) => {
    setEnabled(v);
    if (v && isExpired && expiryDays.trim() === "") setExpiryDays("0");
  };

  // Full inbound list for membership (SS/SOCKS can't host multi-user).
  const { data: inboundsData } = useQuery({
    queryKey: ["inbounds"],
    queryFn: () => call<InboundLite[]>(api.get("/inbounds")),
  });
  const inbounds = (inboundsData ?? []).filter(
    (i) => i.type !== "shadowsocks" && i.type !== "socks",
  );
  // The user's current membership — fetched fresh (the caller's UserRow may be a
  // partial adapter, e.g. opened from a single inbound). Initialises checkboxes.
  const { data: usersData } = useQuery({
    queryKey: ["users"],
    queryFn: () => call<{ users: UserRow[] }>(api.get("/users")),
  });
  const currentRow = usersData?.users.find((u) => u.email === user.email);
  const membershipReady = !!inboundsData && !!currentRow;
  if (members === null && membershipReady) {
    setMembers(new Set(currentRow!.inbound_ids));
  }
  const toggleMember = (id: number) =>
    setMembers((prev) => {
      const next = new Set(prev ?? []);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const save = useMutation({
    mutationFn: () => {
      const body: Record<string, unknown> = {
        quota_bytes: quotaToBytes(quotaVal, unit),
        enabled,
        reset_usage: resetUsage,
      };
      const newEmail = identifier.trim();
      if (newEmail && newEmail !== user.email) body.new_email = newEmail;
      if (expiryDays.trim() !== "") body.expiry_days = parseInt(expiryDays, 10);
      // Only touch membership when we actually loaded it — never wipe blindly.
      if (membershipReady && members) body.inbound_ids = Array.from(members);
      return call(api.patch(`/users/${encodeURIComponent(user.email)}`, body));
    },
    onSuccess: onSaved,
    onError: (e: any) => setErr(e?.response?.data?.error?.message ?? e.message),
  });

  return (
    <Modal
      open
      onClose={onClose}
      title={t("multiUser.editTitle", { email: user.email })}
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="primary"
            disabled={save.isPending}
            onClick={() => {
              setErr("");
              save.mutate();
            }}
          >
            {save.isPending ? t("multiUser.saving") : t("multiUser.btnSave")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label={t("multiUser.fieldIdentifierEdit")} hint={t("multiUser.identifierEditHint")}>
          <Input value={identifier} onChange={(e) => setIdentifier(e.target.value)} />
        </Field>
        <QuotaExpiryFields
          quotaValue={quotaVal}
          setQuotaValue={setQuotaVal}
          unit={unit}
          setUnit={setUnit}
          expiryDays={expiryDays}
          setExpiryDays={setExpiryDays}
          allowNegativeExpiry
          expiryHint={t("multiUser.expiryEditHint")}
          expiryPlaceholder={t("multiUser.expiryEditPlaceholder")}
        />
        <Field label={t("multiUser.fieldMembership")} hint={t("multiUser.membershipHint")}>
          <div className="space-y-1 max-h-40 overflow-y-auto rounded-lg border border-white/10 p-2">
            {!membershipReady && (
              <div className="text-white/40 text-xs px-1">{t("common.loading")}</div>
            )}
            {membershipReady &&
              inbounds.map((ib) => (
                <label key={ib.id} className="flex items-center gap-2 cursor-pointer px-1 py-0.5">
                  <input
                    type="checkbox"
                    checked={members?.has(ib.id) ?? false}
                    onChange={() => toggleMember(ib.id)}
                  />
                  <span className="font-mono text-xs text-white/80">{ib.tag}</span>
                </label>
              ))}
          </div>
        </Field>
        <Field label={t("multiUser.fieldEnabled")}>
          <Toggle
            checked={enabled}
            onChange={onToggleEnabled}
            label={enabled ? t("multiUser.on") : t("multiUser.off")}
          />
        </Field>
        <label className="flex items-center gap-2 cursor-pointer text-sm text-white/80">
          <input
            type="checkbox"
            checked={resetUsage}
            onChange={(e) => setResetUsage(e.target.checked)}
          />
          <span>{t("multiUser.fieldResetUsage")}</span>
        </label>
        <p className="text-xs text-amber-300 bg-amber-400/10 border border-amber-400/20 rounded-lg px-3 py-2 -mt-1">
          {t("multiUser.resetUsageHint")}
        </p>
        {resetUsage && !enabled && (
          <p className="text-xs text-amber-400/90 -mt-2">
            {t("multiUser.resetWithoutEnableWarn")}
          </p>
        )}
        <ErrorText>{err}</ErrorText>
      </div>
    </Modal>
  );
}
