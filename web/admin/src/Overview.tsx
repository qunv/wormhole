import { useQuery } from "@tanstack/react-query";
import { Activity, Boxes, Cpu, Database, Download, FolderGit2, KeyRound, ShieldCheck, TriangleAlert } from "lucide-react";
import { api, DIAGNOSTICS_URL } from "./api";
import { Badge, Button, Card, Notice, PageHeader } from "./components";

export function Overview() {
  const bootstrap = useQuery({ queryKey: ["bootstrap"], queryFn: api.bootstrap });
  const config = useQuery({ queryKey: ["config"], queryFn: api.config });
  const workspaces = useQuery({ queryKey: ["workspaces"], queryFn: api.workspaces });
  const secrets = useQuery({ queryKey: ["secrets"], queryFn: api.secrets });

  const cfg = config.data?.config;
  const workspaceItems = workspaces.data?.workspaces ?? [];
  const secretItems = secrets.data?.secrets ?? [];
  const configuredSecrets = secretItems.filter((item) => item.configured).length;
  const mcpCount = Object.keys(cfg?.mcpServers ?? {}).length;

  return (
    <>
      <PageHeader eyebrow="Local control plane" title="System overview" description="A focused view of the active daemon and persisted configuration." actions={<a href={DIAGNOSTICS_URL} download><Button variant="secondary"><Download size={15} /> Download diagnostics</Button></a>} />
      {bootstrap.data?.startupWarnings?.length ? (
        <Notice tone="warning"><strong>Startup warnings</strong><ul>{bootstrap.data.startupWarnings.map((warning) => <li key={warning}>{warning}</li>)}</ul></Notice>
      ) : null}
      <div className="metric-grid">
        <Metric icon={<Activity />} label="Daemon" value="Online" detail={`v${bootstrap.data?.version ?? "—"}`} tone="success" />
        <Metric icon={<FolderGit2 />} label="Workspaces" value={String(1 + workspaceItems.length)} detail={`${workspaceItems.filter((item) => item.active).length + 1} active`} />
        <Metric icon={<Boxes />} label="MCP servers" value={String(mcpCount)} detail={mcpCount ? "Configured upstreams" : "No upstreams"} />
        <Metric icon={<KeyRound />} label="Secrets" value={`${configuredSecrets}/${secretItems.length}`} detail="Values remain write-only" />
      </div>
      <div className="two-column">
        <Card title="Runtime posture" description="Effective security and execution defaults.">
          <div className="summary-list">
            <Summary icon={<ShieldCheck />} label="Policy" value={cfg?.policy ?? "—"} badge={cfg?.policy === "strict" ? "success" : "info"} />
            <Summary icon={<Cpu />} label="Execution mode" value={cfg?.mode ?? "—"} badge={cfg?.mode === "safe" ? "success" : "warning"} />
            <Summary icon={<Database />} label="Memory" value={cfg?.memory?.enabled ? cfg.memory.provider : "Disabled"} badge={cfg?.memory?.enabled ? "info" : "neutral"} />
            <Summary icon={<TriangleAlert />} label="Audit" value={cfg?.audit ? "Enabled" : "Disabled"} badge={cfg?.audit ? "success" : "danger"} />
          </div>
        </Card>
        <Card title="Admin boundary" description="Controls enforced by the Go server, not only the browser.">
          <div className="security-grid">
            <SecurityItem title="Loopback only" active={bootstrap.data?.security.loopbackOnly ?? true} />
            <SecurityItem title="Same-origin writes" active={bootstrap.data?.security.sameOriginWrites ?? true} />
            <SecurityItem title="CSRF protected" active={bootstrap.data?.security.csrfProtected ?? true} />
            <SecurityItem title="Admin login required" active={bootstrap.data?.security.adminAuthentication ?? true} />
            <SecurityItem title="Secrets are unreadable" active={!bootstrap.data?.security.secretValuesReadable} />
          </div>
        </Card>
      </div>
      <Card title="Paths and identity" description="Useful when diagnosing which installation is being edited.">
        <dl className="definition-grid">
          <div><dt>Configuration</dt><dd>{bootstrap.data?.configPath ?? "—"}</dd></div>
          <div><dt>Wormhole home</dt><dd>{bootstrap.data?.homePath ?? "—"}</dd></div>
          <div><dt>Active config ID</dt><dd><code>{bootstrap.data?.activeConfigId ?? "—"}</code></dd></div>
          <div><dt>Primary workspace</dt><dd>{cfg?.workspace ?? "—"}</dd></div>
        </dl>
      </Card>
    </>
  );
}

function Metric({ icon, label, value, detail, tone = "neutral" }: { icon: React.ReactNode; label: string; value: string; detail: string; tone?: string }) {
  return <div className={`metric ${tone}`}><div className="metric-icon">{icon}</div><div><span>{label}</span><strong>{value}</strong><small>{detail}</small></div></div>;
}

function Summary({ icon, label, value, badge }: { icon: React.ReactNode; label: string; value: string; badge: "neutral" | "success" | "warning" | "danger" | "info" }) {
  return <div className="summary-item"><span className="summary-icon">{icon}</span><span><small>{label}</small><strong>{value}</strong></span><Badge tone={badge}>{value}</Badge></div>;
}

function SecurityItem({ title, active }: { title: string; active: boolean }) {
  return <div className="security-item"><ShieldCheck size={18} /><span>{title}</span><Badge tone={active ? "success" : "danger"}>{active ? "On" : "Off"}</Badge></div>;
}
