import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import { Button, ErrorText, Modal } from "./ui";

interface UserRow {
  email: string;
  inbound_ids: number[];
  enabled: boolean;
}

interface AddResult {
  added: string[];
  already: string[];
  unknown: string[];
}

// AddUsersToInboundModal puts existing users on one more inbound in one go
// (e.g. a new Argo inbound for everyone). Each user keeps their quota, expiry
// and on/off state, and their subscription picks the new node up.
export default function AddUsersToInboundModal({
  inbound,
  onClose,
}: {
  inbound: { id: number; tag: string };
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const users = useQuery({
    queryKey: ["users"],
    queryFn: () => call<{ users: UserRow[] }>(api.get("/users")),
  });
  const rows = users.data?.users ?? [];
  const candidates = useMemo(
    () => rows.filter((u) => !u.inbound_ids?.includes(inbound.id)),
    [rows, inbound.id],
  );
  const onIt = rows.length - candidates.length;
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const allPicked = candidates.length > 0 && picked.size === candidates.length;

  const add = useMutation({
    mutationFn: () =>
      call<AddResult>(api.post(`/inbounds/${inbound.id}/users`, { emails: [...picked] })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["users"] });
      qc.invalidateQueries({ queryKey: ["inbounds"] });
      setPicked(new Set());
    },
  });

  const toggle = (email: string) =>
    setPicked((cur) => {
      const next = new Set(cur);
      if (next.has(email)) next.delete(email);
      else next.add(email);
      return next;
    });

  return (
    <Modal
      open
      onClose={onClose}
      title={t("inbounds.addUsersTitle", { tag: inbound.tag })}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            {t("inbounds.addUsersClose")}
          </Button>
          <Button
            type="button"
            variant="primary"
            disabled={picked.size === 0 || add.isPending}
            onClick={() => add.mutate()}
          >
            {add.isPending
              ? t("inbounds.addUsersAdding")
              : t("inbounds.addUsersSubmit", { count: picked.size })}
          </Button>
        </>
      }
    >
      <p className="text-xs leading-relaxed text-white/50">{t("inbounds.addUsersHint")}</p>
      {onIt > 0 && (
        <p className="mt-2 text-xs text-white/40">{t("inbounds.addUsersAlreadyOn", { count: onIt })}</p>
      )}

      {users.isLoading ? null : candidates.length === 0 ? (
        <p className="mt-3 text-sm text-white/60">{t("inbounds.addUsersNone")}</p>
      ) : (
        <div className="mt-3">
          <label className="flex items-center gap-2 text-sm text-white/80">
            <input
              type="checkbox"
              className="h-3.5 w-3.5 accent-emerald-500"
              checked={allPicked}
              onChange={() =>
                setPicked(allPicked ? new Set() : new Set(candidates.map((u) => u.email)))
              }
            />
            {t("inbounds.addUsersSelectAll", { count: candidates.length })}
          </label>
          <div className="mt-2 max-h-64 space-y-1 overflow-y-auto pr-1">
            {candidates.map((u) => (
              <label key={u.email} className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  className="h-3.5 w-3.5 accent-emerald-500"
                  checked={picked.has(u.email)}
                  onChange={() => toggle(u.email)}
                />
                <span className="font-mono text-xs text-white/80 break-all">{u.email}</span>
                {!u.enabled && (
                  <span className="text-[11px] text-white/40">{t("inbounds.addUsersDisabledTag")}</span>
                )}
              </label>
            ))}
          </div>
        </div>
      )}

      {add.isSuccess && (
        <p className="mt-3 text-sm text-emerald-300">
          {t("inbounds.addUsersDone", { count: add.data.added.length })}
        </p>
      )}
      {add.isError && <ErrorText>{t("inbounds.addUsersFailed")}</ErrorText>}
    </Modal>
  );
}
