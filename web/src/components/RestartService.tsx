import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api, call } from "../api/client";
import { Button, ConfirmDialog } from "./ui";

// RestartService restarts the whole EdgeNest service (panel + engines). The
// panel goes away for a few seconds; we poll until it answers again, then
// refetch everything so the page shows the fresh state.
export default function RestartService({ variant = "default" }: { variant?: "default" | "primary" }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [confirm, setConfirm] = useState(false);
  const [phase, setPhase] = useState<"idle" | "restarting" | "back" | "timeout" | "error">("idle");
  const [err, setErr] = useState("");

  async function run() {
    setConfirm(false);
    setErr("");
    setPhase("restarting");
    try {
      await call(api.post("/system/restart", {}));
    } catch (e) {
      setErr((e as Error).message);
      setPhase("error");
      return;
    }
    // Give the old process time to exit before the first poll, or it answers.
    await sleep(4000);
    const deadline = Date.now() + 90_000;
    while (Date.now() < deadline) {
      try {
        await call(api.get("/dashboard"));
        await qc.invalidateQueries();
        setPhase("back");
        return;
      } catch {
        await sleep(2000);
      }
    }
    setPhase("timeout");
  }

  return (
    <div className="flex flex-wrap items-center gap-3">
      <Button variant={variant} disabled={phase === "restarting"} onClick={() => setConfirm(true)}>
        {phase === "restarting" ? t("dashboard.restartRunning") : t("dashboard.restartBtn")}
      </Button>
      {phase === "back" && <span className="text-sm text-emerald-300">{t("dashboard.restartDone")}</span>}
      {phase === "timeout" && <span className="text-sm text-amber-300">{t("dashboard.restartTimeout")}</span>}
      {phase === "error" && <span className="text-sm text-red-400">{err}</span>}
      <ConfirmDialog
        open={confirm}
        title={t("dashboard.restartConfirmTitle")}
        body={t("dashboard.restartConfirmBody")}
        confirmLabel={t("dashboard.restartBtn")}
        cancelLabel={t("routes.btnCancel")}
        onConfirm={run}
        onCancel={() => setConfirm(false)}
      />
    </div>
  );
}

function sleep(ms: number) {
  return new Promise((r) => setTimeout(r, ms));
}
