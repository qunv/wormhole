import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  Check,
  Clock3,
  Gauge,
  RefreshCw,
  ScrollText,
  ShieldCheck,
  Timer,
  X,
} from "lucide-react";
import { api, APIError } from "./api";
import { Badge, Button, Card, EmptyState, LoadingPage, Notice, PageHeader, Select, TextInput } from "./components";
import type { ApprovalRecord, AuditRecord, OperationsWorkspace, RuntimeToolMetric } from "./types";

type OperationsTab = "runtime" | "approvals" | "audit";
type ApprovalDecision = "approved" | "denied";

export function Operations() {
  const queryClient = useQueryClient();
  const [tab, setTab] = useState<OperationsTab>("runtime");
  const [approvalStatus, setApprovalStatus] = useState("pending");
  const [approvalWorkspace, setApprovalWorkspace] = useState("");
  const [auditWorkspace, setAuditWorkspace] = useState("");
  const [auditTool, setAuditTool] = useState("");
  const [auditStatus, setAuditStatus] = useState("");

  const operations = useQuery({
    queryKey: ["operations"],
    queryFn: api.operations,
    refetchInterval: 10_000,
  });
  const pendingApprovals = useQuery({
    queryKey: ["approvals", "pending", ""],
    queryFn: () => api.approvals("pending"),
    refetchInterval: 10_000,
  });
  const approvals = useQuery({
    queryKey: ["approvals", approvalStatus, approvalWorkspace],
    queryFn: () => api.approvals(approvalStatus, approvalWorkspace),
    refetchInterval: approvalStatus === "pending" ? 10_000 : false,
  });
  const audit = useQuery({
    queryKey: ["audit", auditWorkspace, auditTool, auditStatus],
    queryFn: () => api.audit({ workspace: auditWorkspace, tool: auditTool.trim(), status: auditStatus, limit: 100 }),
    enabled: tab === "audit",
  });
  const decide = useMutation({
    mutationFn: ({ approval, decision }: { approval: ApprovalRecord; decision: ApprovalDecision }) =>
      api.decideApproval(approval.workspaceId, approval.id, decision),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["approvals"] });
    },
  });

  const workspaces = operations.data?.workspaces ?? [];
  const pendingCount = pendingApprovals.data?.count ?? 0;
  const totalCompleted = workspaces.reduce((total, item) => total + item.metrics.completed_calls, 0);
  const totalFailed = workspaces.reduce((total, item) => total + item.metrics.failed, 0);
  const activeBindings = numberValue(operations.data?.sessionRouter.active_bindings);

  const refresh = async () => {
    await Promise.all([
      operations.refetch(),
      approvals.refetch(),
      pendingApprovals.refetch(),
      tab === "audit" ? audit.refetch() : Promise.resolve(),
    ]);
  };

  if (operations.isLoading && !operations.data) return <LoadingPage />;
  if (operations.error && !operations.data) {
    return <Notice tone="danger">{errorMessage(operations.error)}</Notice>;
  }

  return <>
    <PageHeader
      eyebrow="Local control plane"
      title="Operations"
      description="Inspect live runtime health, decide exact approval requests, and explore bounded redacted audit events across active workspaces."
      actions={<Button variant="secondary" onClick={() => void refresh()} loading={operations.isFetching || approvals.isFetching || audit.isFetching}><RefreshCw size={15} /> Refresh</Button>}
    />

    <div className="operations-summary-grid">
      <Summary icon={<Activity />} label="Completed calls" value={totalCompleted} detail={`${workspaces.length} active workspace${workspaces.length === 1 ? "" : "s"}`} />
      <Summary icon={<X />} label="Failed calls" value={totalFailed} detail={totalCompleted ? `${Math.round(totalFailed * 100 / totalCompleted)}% of completed` : "No completed calls"} tone={totalFailed ? "danger" : "success"} />
      <Summary icon={<ShieldCheck />} label="Pending approvals" value={pendingCount} detail="Exact, expiring actions" tone={pendingCount ? "warning" : "success"} />
      <Summary icon={<Timer />} label="Active bindings" value={activeBindings} detail={`${numberValue(operations.data?.sessionRouter.max_bindings)} capacity`} />
    </div>

    <div className="operations-tabs" role="tablist">
      <button className={tab === "runtime" ? "active" : ""} onClick={() => setTab("runtime")}><Gauge size={15} /> Runtime</button>
      <button className={tab === "approvals" ? "active" : ""} onClick={() => setTab("approvals")}><ShieldCheck size={15} /> Approvals{pendingCount > 0 && <span>{pendingCount}</span>}</button>
      <button className={tab === "audit" ? "active" : ""} onClick={() => setTab("audit")}><ScrollText size={15} /> Audit</button>
    </div>

    {tab === "runtime" && <RuntimePanel workspaces={workspaces} router={operations.data?.sessionRouter ?? {}} shared={operations.data?.sharedResources ?? {}} />}
    {tab === "approvals" && <ApprovalsPanel
      approvals={approvals.data?.approvals ?? []}
      loading={approvals.isLoading}
      error={approvals.error}
      status={approvalStatus}
      workspace={approvalWorkspace}
      workspaces={workspaces}
      onStatus={setApprovalStatus}
      onWorkspace={setApprovalWorkspace}
      onDecision={(approval, decision) => decide.mutate({ approval, decision })}
      decidingId={decide.isPending ? decide.variables?.approval.id : undefined}
      mutationError={decide.error}
      truncated={approvals.data?.truncated ?? false}
    />}
    {tab === "audit" && <AuditPanel
      records={audit.data?.records ?? []}
      loading={audit.isLoading}
      error={audit.error}
      workspace={auditWorkspace}
      tool={auditTool}
      status={auditStatus}
      workspaces={workspaces}
      onWorkspace={setAuditWorkspace}
      onTool={setAuditTool}
      onStatus={setAuditStatus}
      truncated={audit.data?.truncated ?? false}
    />}
  </>;
}

function Summary({ icon, label, value, detail, tone = "info" }: { icon: React.ReactNode; label: string; value: number; detail: string; tone?: "info" | "success" | "warning" | "danger" }) {
  return <div className={`operations-summary ${tone}`}><span>{icon}</span><div><small>{label}</small><strong>{value.toLocaleString()}</strong><em>{detail}</em></div></div>;
}

function RuntimePanel({ workspaces, router, shared }: { workspaces: OperationsWorkspace[]; router: Record<string, unknown>; shared: Record<string, unknown> }) {
  return <div className="operations-runtime-stack">
    {workspaces.map((workspace) => <Card
      key={workspace.id}
      title={workspace.id}
      description={workspace.root}
      actions={<><Badge tone="info">{workspace.mode}</Badge><Badge tone={workspace.policy === "full" ? "warning" : "success"}>{workspace.policy}</Badge></>}
    >
      {workspace.startupWarnings.length > 0 && <Notice tone="warning"><strong>{workspace.startupWarnings.length} startup warning{workspace.startupWarnings.length === 1 ? "" : "s"}</strong><ul>{workspace.startupWarnings.map((warning) => <li key={warning}>{warning}</li>)}</ul></Notice>}
      <div className="runtime-metric-grid">
        <RuntimeMetric label="Succeeded" value={workspace.metrics.succeeded} />
        <RuntimeMetric label="Failed" value={workspace.metrics.failed} danger={workspace.metrics.failed > 0} />
        <RuntimeMetric label="In flight" value={workspace.metrics.in_flight} />
        <RuntimeMetric label="Concurrency" value={`${workspace.metrics.concurrency.executing}/${workspace.metrics.concurrency.limit}`} />
        <RuntimeMetric label="Average latency" value={formatMicros(workspace.metrics.latency_us.average)} />
        <RuntimeMetric label="Uptime" value={formatDuration(workspace.metrics.uptime_seconds)} />
      </div>
      <ToolMetrics tools={workspace.metrics.tools ?? []} />
      <details className="json-details operations-json"><summary>Module health and full runtime details</summary><pre>{JSON.stringify({ modules: workspace.modules, metrics: workspace.metrics }, null, 2)}</pre></details>
    </Card>)}
    <div className="two-column operations-system-details">
      <Card title="Session router" description="Conversation binding capacity and expiry activity."><KeyValueGrid value={router} /></Card>
      <Card title="Shared resources" description="Daemon-wide pooled providers, clients, recorders, and audit writers."><KeyValueGrid value={shared} /></Card>
    </div>
  </div>;
}

function RuntimeMetric({ label, value, danger = false }: { label: string; value: number | string; danger?: boolean }) {
  return <div className={`runtime-metric ${danger ? "danger" : ""}`}><small>{label}</small><strong>{typeof value === "number" ? value.toLocaleString() : value}</strong></div>;
}

function ToolMetrics({ tools }: { tools: RuntimeToolMetric[] }) {
  const top = useMemo(() => [...tools].sort((a, b) => b.completed_calls - a.completed_calls || b.failed - a.failed).slice(0, 12), [tools]);
  if (!top.length) return <div className="operations-inline-empty">No tool calls have completed yet.</div>;
  return <div className="operations-table-wrap"><table className="operations-table"><thead><tr><th>Tool</th><th>Module</th><th>Calls</th><th>Failed</th><th>Avg latency</th><th>Last call</th></tr></thead><tbody>{top.map((tool) => <tr key={tool.tool}><td><code>{tool.tool}</code></td><td>{tool.module}</td><td>{tool.completed_calls}</td><td className={tool.failed ? "danger" : ""}>{tool.failed}</td><td>{formatMicros(tool.latency_us.average)}</td><td>{formatTime(tool.last_call_at)}</td></tr>)}</tbody></table></div>;
}

function ApprovalsPanel({ approvals, loading, error, status, workspace, workspaces, onStatus, onWorkspace, onDecision, decidingId, mutationError, truncated }: {
  approvals: ApprovalRecord[];
  loading: boolean;
  error: unknown;
  status: string;
  workspace: string;
  workspaces: OperationsWorkspace[];
  onStatus: (value: string) => void;
  onWorkspace: (value: string) => void;
  onDecision: (approval: ApprovalRecord, decision: ApprovalDecision) => void;
  decidingId?: string;
  mutationError: unknown;
  truncated: boolean;
}) {
  return <Card title="Approval Center" description="Decisions use the authenticated loopback Admin boundary. The MCP operator token is never sent to the browser.">
    <div className="operations-filter-bar">
      <Select value={status} onChange={(event) => onStatus(event.target.value)}><option value="pending">Pending</option><option value="approved">Approved</option><option value="denied">Denied</option><option value="consumed">Consumed</option><option value="expired">Expired</option><option value="all">All statuses</option></Select>
      <Select value={workspace} onChange={(event) => onWorkspace(event.target.value)}><option value="">All workspaces</option>{workspaces.map((item) => <option key={item.id} value={item.id}>{item.id}</option>)}</Select>
    </div>
    {!!error && <Notice tone="danger">{errorMessage(error)}</Notice>}
    {!!mutationError && <Notice tone="danger">{errorMessage(mutationError)}</Notice>}
    {truncated && <Notice tone="warning">Only the newest bounded result set is shown.</Notice>}
    {loading ? <div className="operations-inline-empty">Loading approvals…</div> : approvals.length === 0 ? <EmptyState title="No matching approvals" description="Exact approval requests will appear here when an agent reaches a gated action." /> : <div className="approval-list">{approvals.map((approval) => <div className="approval-row" key={`${approval.workspaceId}:${approval.id}`}>
      <div className="approval-main"><div><Badge tone={approvalTone(approval.status)}>{approval.status}</Badge><Badge>{approval.workspaceId}</Badge><span>{formatTime(approval.created)}</span></div><code>{approval.action}</code><p>{approval.reason || "No reason supplied."}</p><small>Expires {formatTime(approval.expires_at)} · {approval.actions.length} exact action{approval.actions.length === 1 ? "" : "s"}</small></div>
      {approval.status === "pending" && <div className="approval-actions"><Button variant="secondary" loading={decidingId === approval.id} onClick={() => onDecision(approval, "denied")}><X size={14} /> Deny</Button><Button loading={decidingId === approval.id} onClick={() => onDecision(approval, "approved")}><Check size={14} /> Approve</Button></div>}
    </div>)}</div>}
  </Card>;
}

function AuditPanel({ records, loading, error, workspace, tool, status, workspaces, onWorkspace, onTool, onStatus, truncated }: {
  records: AuditRecord[];
  loading: boolean;
  error: unknown;
  workspace: string;
  tool: string;
  status: string;
  workspaces: OperationsWorkspace[];
  onWorkspace: (value: string) => void;
  onTool: (value: string) => void;
  onStatus: (value: string) => void;
  truncated: boolean;
}) {
  return <Card title="Audit Explorer" description="Reads only a bounded tail of each active workspace's current redacted audit file.">
    <div className="operations-filter-bar audit-filters">
      <Select value={workspace} onChange={(event) => onWorkspace(event.target.value)}><option value="">All workspaces</option>{workspaces.map((item) => <option key={item.id} value={item.id}>{item.id}</option>)}</Select>
      <TextInput value={tool} onChange={(event) => onTool(event.target.value)} placeholder="Exact tool name" />
      <Select value={status} onChange={(event) => onStatus(event.target.value)}><option value="">All statuses</option><option value="succeeded">Succeeded</option><option value="failed">Failed</option><option value="policy_rejected">Policy rejected</option><option value="canceled">Canceled</option><option value="deadline_exceeded">Deadline exceeded</option></Select>
    </div>
    {!!error && <Notice tone="danger">{errorMessage(error)}</Notice>}
    {truncated && <Notice tone="warning">The audit tail or result list was truncated to its safety limit.</Notice>}
    {loading ? <div className="operations-inline-empty">Loading audit records…</div> : records.length === 0 ? <EmptyState title="No matching audit events" description="Change the filters or execute a tool to produce a new redacted audit event." /> : <div className="audit-list">{records.map((record, index) => <details className="audit-row" key={`${record.call_id}:${index}`}>
      <summary><span className={`audit-status ${record.status}`}>{record.ok ? <Check size={13} /> : <X size={13} />}</span><span className="audit-copy"><strong><code>{record.tool}</code><Badge>{record.workspaceId}</Badge><Badge tone={auditTone(record.status)}>{record.status}</Badge></strong><small>{formatTime(record.ts)} · {formatMicros(record.duration_us)} · {record.tool_module}</small>{!!record.error && <em>{String(record.error)}</em>}</span></summary>
      <pre>{JSON.stringify(record, null, 2)}</pre>
    </details>)}</div>}
  </Card>;
}

function KeyValueGrid({ value }: { value: Record<string, unknown> }) {
  const entries = Object.entries(value).filter(([, item]) => typeof item !== "object" || item === null).slice(0, 16);
  return <div className="operations-kv-grid">{entries.map(([key, item]) => <div key={key}><small>{key.replaceAll("_", " ")}</small><strong>{String(item)}</strong></div>)}</div>;
}

function numberValue(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function formatMicros(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0 ms";
  if (value < 1_000) return `${Math.round(value)} µs`;
  if (value < 1_000_000) return `${(value / 1_000).toFixed(value < 10_000 ? 1 : 0)} ms`;
  return `${(value / 1_000_000).toFixed(1)} s`;
}

function formatDuration(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3_600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86_400) return `${Math.floor(seconds / 3_600)}h`;
  return `${Math.floor(seconds / 86_400)}d`;
}

function formatTime(value?: string): string {
  if (!value) return "Never";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
}

function approvalTone(status: string): "neutral" | "success" | "warning" | "danger" | "info" {
  if (status === "pending") return "warning";
  if (status === "approved" || status === "consumed") return "success";
  if (status === "denied") return "danger";
  return "neutral";
}

function auditTone(status: string): "neutral" | "success" | "warning" | "danger" | "info" {
  if (status === "succeeded") return "success";
  if (status === "policy_rejected") return "warning";
  if (status === "failed" || status === "deadline_exceeded") return "danger";
  return "neutral";
}

function errorMessage(error: unknown): string {
  return error instanceof APIError ? error.message : error instanceof Error ? error.message : error ? String(error) : "";
}
