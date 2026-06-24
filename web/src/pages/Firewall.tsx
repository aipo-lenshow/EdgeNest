// Firewall page — read-only. Shows which ports are open and reminds the
// operator that the same ports must be allowed in the cloud provider's
// security group (a layer the panel cannot touch). A packet from the internet
// passes two gates: the cloud security group (at the provider's network edge)
// then the host's iptables (which EdgeNest manages automatically). Both gates
// allow the same set of ports, so a single table is the source of truth.
//
// The port list comes from /firewall/preview (the desired allow-set the
// orchestrator computes from enabled inbounds + SSH + panel) — it is always
// populated and never needs a manual resync.

import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";
import { api, call } from "../api/client";
import Layout from "../components/Layout";
import { Badge, Card, PageHeader } from "../components/ui";

interface FirewallPreview {
  allow_ports: { port: number; proto: string; note: string }[];
}

const PROVIDERS: { name: string; url: string; note: string }[] = [
  {
    name: "AWS EC2",
    url: "https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/working-with-security-groups.html#adding-security-group-rule",
    note: "Console → EC2 → Security Groups → Inbound rules → Edit",
  },
  {
    name: "Google Cloud",
    url: "https://cloud.google.com/vpc/docs/using-firewalls",
    note: "Console → VPC network → Firewall → Create firewall rule",
  },
  {
    name: "Azure",
    url: "https://learn.microsoft.com/azure/virtual-network/manage-network-security-group",
    note: "Portal → Network security group → Inbound security rules → Add",
  },
  {
    name: "Oracle Cloud (OCI)",
    url: "https://docs.oracle.com/iaas/Content/Network/Concepts/securityrules.htm",
    note: "Console → Networking → VCN → Security Lists / NSG → Add Ingress",
  },
  {
    name: "Vultr",
    url: "https://www.vultr.com/docs/vultr-firewall/",
    note: "Console → Firewall → Add new rule",
  },
  {
    name: "DigitalOcean",
    url: "https://docs.digitalocean.com/products/networking/firewalls/",
    note: "Control panel → Networking → Firewalls → Inbound Rules",
  },
  {
    name: "Linode / Akamai",
    url: "https://www.linode.com/docs/products/networking/cloud-firewall/",
    note: "Cloud Manager → Cloud Firewalls → Add an inbound rule",
  },
  {
    name: "阿里云 ECS",
    url: "https://help.aliyun.com/document_detail/25471.html",
    note: "控制台 → ECS → 网络与安全 → 安全组 → 入方向 → 添加",
  },
  {
    name: "腾讯云 CVM",
    url: "https://cloud.tencent.com/document/product/213/12452",
    note: "控制台 → CVM → 安全组 → 入站规则 → 添加规则",
  },
  {
    name: "华为云 ECS",
    url: "https://support.huaweicloud.com/usermanual-vpc/vpc_SecurityGroup_0001.html",
    note: "控制台 → VPC → 安全组 → 入方向规则 → 添加",
  },
];

export default function FirewallPage() {
  const { t } = useTranslation();
  const { data: preview } = useQuery({
    queryKey: ["firewall-preview"],
    queryFn: () => call<FirewallPreview>(api.get("/firewall/preview")),
  });

  const ports = preview?.allow_ports ?? [];

  return (
    <Layout>
      <PageHeader title={t("firewall.title")} subtitle={t("firewall.subtitle")} />

      <Card title={t("firewall.portsTitle")}>
        {ports.length === 0 ? (
          <div className="text-sm text-white/40">{t("firewall.portsEmpty")}</div>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-white/50 text-xs uppercase">
              <tr>
                <th className="text-left py-1 w-24">{t("firewall.port")}</th>
                <th className="text-left py-1 w-24">{t("firewall.proto")}</th>
                <th className="text-left py-1">{t("firewall.note")}</th>
              </tr>
            </thead>
            <tbody>
              {ports.map((p, i) => (
                <tr key={i} className="border-t border-white/5">
                  <td className="py-1 font-mono">{p.port}</td>
                  <td className="py-1">
                    <Badge tone={p.proto === "udp" ? "warn" : "info"}>
                      {p.proto.toUpperCase()}
                    </Badge>
                  </td>
                  <td className="py-1 text-white/60">
                    {p.note.replace(/^edgenest:/, "")}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <div className="mt-3 text-xs text-white/40">
          {t("firewall.safeModeHint")}
        </div>
      </Card>

      <div className="h-4" />

      <details className="rounded-2xl border border-white/10 bg-white/[0.03]">
        <summary className="cursor-pointer select-none px-5 py-3 font-medium text-white/80">
          {t("firewall.providersTitle")}
        </summary>
        <div className="px-5 pb-5">
          <div className="text-sm text-white/60 mb-4">
            {t("firewall.providerSubtitle")}
          </div>
          <ul className="grid md:grid-cols-2 gap-3 text-sm">
            {PROVIDERS.map((p) => (
              <li
                key={p.name}
                className="rounded-lg border border-white/10 bg-white/[0.02] p-3"
              >
                <a
                  href={p.url}
                  target="_blank"
                  rel="noreferrer"
                  className="text-emerald-300 hover:text-emerald-200 font-medium"
                >
                  {p.name} ↗
                </a>
                <div className="text-white/60 mt-1 text-xs">{p.note}</div>
              </li>
            ))}
          </ul>
        </div>
      </details>
    </Layout>
  );
}
