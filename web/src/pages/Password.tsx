import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import { Button, Card, ErrorText, Field, Input } from "../components/ui";

// Standalone full-page form. Shown when /me reports must_change_password=true
// so the operator can't reach any other route until they swap the bootstrap
// password.
export default function PasswordPage() {
  const nav = useNavigate();
  const qc = useQueryClient();
  const { t } = useTranslation();
  const [oldPw, setOldPw] = useState("");
  const [newPw, setNewPw] = useState("");
  const [confirmPw, setConfirmPw] = useState("");
  const [err, setErr] = useState("");

  const m = useMutation({
    mutationFn: () =>
      call(
        api.post("/password", { old_password: oldPw, new_password: newPw }),
      ),
    // Refetch /me before navigating so the Protected guard sees the cleared
    // must_change_password flag — otherwise the stale react-query cache bounces
    // the operator straight back here.
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["me"] });
      nav("/");
    },
    onError: (e: any) =>
      setErr(e?.response?.data?.error?.message ?? e.message),
  });

  return (
    <div className="min-h-screen flex items-center justify-center p-6 bg-black/40">
      <Card title={t("auth.changePasswordTitle")}>
        <div className="w-96 space-y-4">
          <p className="text-sm text-white/70">{t("auth.changePasswordHint")}</p>
          <Field label={t("auth.currentPassword")}>
            <Input
              type="password"
              value={oldPw}
              onChange={(e) => setOldPw(e.target.value)}
            />
          </Field>
          <Field label={t("auth.newPassword")} hint={t("auth.newPasswordHint")}>
            <Input
              type="password"
              value={newPw}
              onChange={(e) => setNewPw(e.target.value)}
            />
          </Field>
          <Field label={t("auth.confirmNewPassword")}>
            <Input
              type="password"
              value={confirmPw}
              onChange={(e) => setConfirmPw(e.target.value)}
            />
          </Field>
          <ErrorText>{err}</ErrorText>
          <Button
            variant="primary"
            className="w-full"
            disabled={
              m.isPending ||
              newPw.length < 8 ||
              newPw !== confirmPw
            }
            onClick={() => {
              setErr("");
              m.mutate();
            }}
          >
            {m.isPending ? t("auth.saving") : t("auth.saveAndContinue")}
          </Button>
        </div>
      </Card>
    </div>
  );
}
