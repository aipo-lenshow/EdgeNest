import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import { Button, ErrorText } from "./ui";

interface UserRow {
  email: string;
  inbound_ids: number[];
}

// Protocols without per-user accounts can't be handed to users (the server
// answers UNSUPPORTED); leave them out instead of failing the batch.
const SINGLE_USER = /shadowsocks|socks/i;

// AddNewInboundsToAllUsers sits on the create-inbound result page: one click
// puts every inbound just created on every existing user, so their
// subscriptions pick the new nodes up without visiting each inbound.
export default function AddNewInboundsToAllUsers({
  inbounds,
}: {
  inbounds: { id: number; tag: string; ui_type: string }[];
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [done, setDone] = useState<number | null>(null);
  const users = useQuery({
    queryKey: ["users"],
    queryFn: () => call<{ users: UserRow[] }>(api.get("/users")),
  });
  const targets = inbounds.filter((ib) => !SINGLE_USER.test(ib.ui_type));
  const missing = (users.data?.users ?? []).filter((u) =>
    targets.some((ib) => !u.inbound_ids?.includes(ib.id)),
  );

  const add = useMutation({
    mutationFn: async () => {
      const added = new Set<string>();
      for (const ib of targets) {
        const emails = missing.filter((u) => !u.inbound_ids?.includes(ib.id)).map((u) => u.email);
        if (emails.length === 0) continue;
        const r = await call<{ added: string[] }>(api.post(`/inbounds/${ib.id}/users`, { emails }));
        r.added.forEach((e) => added.add(e));
      }
      return added.size;
    },
    onSuccess: (n) => {
      setDone(n);
      qc.invalidateQueries({ queryKey: ["users"] });
      qc.invalidateQueries({ queryKey: ["inbounds"] });
    },
  });

  if (targets.length === 0 || (missing.length === 0 && done === null)) return null;

  return (
    <div className="rounded-md border border-emerald-500/40 bg-emerald-500/10 px-3 py-3 text-sm grid gap-2">
      <div className="text-xs text-black/70 dark:text-white/70">
        {t("createInbound.addAllUsersHint", { count: targets.length })}
      </div>
      {done === null ? (
        <div>
          <Button variant="primary" disabled={add.isPending} onClick={() => add.mutate()}>
            {add.isPending
              ? t("inbounds.addUsersAdding")
              : t("createInbound.addAllUsersBtn", { count: missing.length })}
          </Button>
        </div>
      ) : (
        <div className="text-emerald-700 dark:text-emerald-300">
          {t("inbounds.addUsersDone", { count: done })}
        </div>
      )}
      {add.isError && <ErrorText>{t("inbounds.addUsersFailed")}</ErrorText>}
    </div>
  );
}
