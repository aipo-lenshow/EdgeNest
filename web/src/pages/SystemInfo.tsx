// SystemInfo page. Read-mostly snapshot of the host plus the BBR enable/disable
// controls (moved here from Advanced — operators expect "is BBR on?" to live in
// a "system info" page, not buried under Advanced).

import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Layout from "../components/Layout";
import { Badge, Button, Card, ErrorText, PageHeader } from "../components/ui";
import { api, call } from "../api/client";
import { useState } from "react";

interface BBRState {
  supported: boolean;
  congestion_control: string;
  default_qdisc: string;
  enabled: boolean;
  os: string;
  notes?: string;
}

interface InboundPort {
  proto: string;
  port: number;
  tag: string;
}

interface CPUInfo {
  vcpu: number;
  physical_cores: number;
  threads_per_core: number;
  model: string;
}

interface SystemInfoResp {
  os: string;
  os_id: string;
  os_name: string;
  arch: string;
  kernel: string;
  cpu: CPUInfo;
  cpu_cores: number; // legacy alias for vcpu
  memory_total_kb: number;
  hostname: string;
  bbr: BBRState;
  inbound_ports: InboundPort[];
  network_capability?: {
    ipv4_addr?: string;
    ipv4_addrs?: string[];
    ipv6_addr?: string;
    ipv6_addrs?: string[];
  };
}

interface XrayStatus {
  installed: boolean;
  version?: string;
  path: string;
  pinned_version: string;
  update_available: boolean;
}

function fmtMem(kb: number): string {
  if (!kb) return "—";
  const mib = kb / 1024;
  if (mib >= 1024) return `${(mib / 1024).toFixed(1)} GiB`;
  return `${mib.toFixed(0)} MiB`;
}

// SystemInfoPage renders standalone or embedded inside the merged Dashboard
// ("总览") below the status tiles — the parent then supplies the page header.
export default function SystemInfoPage({ embedded = false }: { embedded?: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [err, setErr] = useState("");
  const info = useQuery({
    queryKey: ["system-info"],
    queryFn: () => call<SystemInfoResp>(api.get("/system/info")),
  });
  const xray = useQuery({
    queryKey: ["xray-status"],
    queryFn: () => call<XrayStatus>(api.get("/system/xray/status")),
    retry: false,
  });
  const installXray = useMutation({
    mutationFn: () => call<XrayStatus>(api.post("/system/xray/install")),
    onSuccess: () => {
      setErr("");
      qc.invalidateQueries({ queryKey: ["xray-status"] });
    },
    onError: (e: Error) => setErr(e.message),
  });

  const content = (
    <>
      {info.isLoading && <div className="text-white/50">{t("common.loading")}</div>}
      {info.data && (
        <div className="grid gap-6">
          <Card title={t("systemInfo.title")}>
            <dl className="grid grid-cols-2 md:grid-cols-3 gap-y-3 gap-x-6 text-sm">
              <Row label={t("systemInfo.host")} value={info.data.hostname} />
              <Row
                label={t("systemInfo.os")}
                value={info.data.os_name || info.data.os}
              />
              <Row label={t("systemInfo.kernel")} value={info.data.kernel || "—"} />
              <Row label={t("systemInfo.arch")} value={info.data.arch} />
              <Row
                label={t("systemInfo.cpuVcpu")}
                value={
                  info.data.cpu
                    ? t("systemInfo.cpuBreakdown", {
                        vcpu: info.data.cpu.vcpu,
                        cores: info.data.cpu.physical_cores,
                        threads: info.data.cpu.threads_per_core,
                      })
                    : String(info.data.cpu_cores)
                }
              />
              {info.data.cpu?.model && (
                <Row label={t("systemInfo.cpuModel")} value={info.data.cpu.model} />
              )}
              <Row label={t("systemInfo.memory")} value={fmtMem(info.data.memory_total_kb)} />
              {(() => {
                const cap = info.data.network_capability;
                const v4 = cap?.ipv4_addrs?.length
                  ? cap.ipv4_addrs
                  : cap?.ipv4_addr
                  ? [cap.ipv4_addr]
                  : [];
                const v6 = cap?.ipv6_addrs?.length
                  ? cap.ipv6_addrs
                  : cap?.ipv6_addr
                  ? [cap.ipv6_addr]
                  : [];
                if (!v4.length && !v6.length) return null;
                const stack =
                  v4.length && v6.length
                    ? t("systemInfo.ipDualStack")
                    : t("systemInfo.ipSingle");
                return (
                  <>
                    <Row label={t("systemInfo.ipStack")} value={stack} />
                    {v4.length > 0 && (
                      <Row label={t("systemInfo.ipv4")} value={v4.join(" · ")} />
                    )}
                    {v6.length > 0 && (
                      <Row label={t("systemInfo.ipv6")} value={v6.join(" · ")} />
                    )}
                  </>
                );
              })()}
            </dl>
          </Card>

          <div id="xray">
          <Card title={t("systemInfo.xrayTitle")}>
            <div className="grid gap-3 text-sm">
              <div className="flex items-center gap-3">
                <span className="text-white/50 w-40">{t("systemInfo.xrayStatus")}</span>
                <Badge tone={xray.data?.installed ? "success" : "warn"}>
                  {xray.data?.installed
                    ? t("systemInfo.xrayInstalled")
                    : t("systemInfo.xrayNotInstalled")}
                </Badge>
                {xray.data?.installed && xray.data.version && (
                  <code className="text-white/80">v{xray.data.version}</code>
                )}
              </div>
              <div className="flex items-center gap-3">
                <span className="text-white/50 w-40">{t("systemInfo.xrayPinned")}</span>
                <code className="text-white/80">v{xray.data?.pinned_version ?? "—"}</code>
                {xray.data?.update_available && (
                  <Badge tone="warn">{t("systemInfo.xrayUpdateAvail")}</Badge>
                )}
              </div>
              <div className="flex items-center gap-3 pt-2">
                <Button
                  variant="primary"
                  disabled={!!xray.data?.installed && !xray.data?.update_available || installXray.isPending}
                  onClick={() => installXray.mutate()}
                >
                  {installXray.isPending
                    ? t("systemInfo.xrayInstalling")
                    : xray.data?.installed
                    ? t("systemInfo.xrayReinstall")
                    : t("systemInfo.xrayInstall")}
                </Button>
                {xray.data?.installed && !xray.data?.update_available && (
                  <span className="text-white/40 text-xs">
                    {t("systemInfo.xrayAlreadyInstalledHint")}
                  </span>
                )}
                {err && <ErrorText>{err}</ErrorText>}
              </div>
            </div>
          </Card>
          </div>
        </div>
      )}
    </>
  );

  if (embedded) return content;

  return (
    <Layout>
      <PageHeader title={t("systemInfo.title")} subtitle={t("systemInfo.subtitle")} />
      {content}
    </Layout>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-white/50">{label}</dt>
      <dd className="text-white/90 mt-0.5">{value}</dd>
    </div>
  );
}
