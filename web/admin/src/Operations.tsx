import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  Check,
  ChevronRight,
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
type RuntimeScope = "workspaces" | "daemon";
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

  const workspaces = useMemo(
    () => (operations.data?.workspaces ?? []).map(normalizeOperationsWorkspace),
    [operations.data?.workspaces],
  );
  const pendingCount = pendingApprovals.data?.count ?? 0;
  const totalCompleted = workspaces.reduce((total, item) => total + item.metrics.completed_calls, 0);
  const totalFailed = workspaces.reduce((total, item) => total + item.metrics.failed, 0);
  const activeBindings = numberValue(operations.data?.sessionRouter?.active_bindings);

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
      <Summary icon={<Timer />} label="Active bindings" value={activeBindings} detail={`${numberValue(operations.data?.sessionRouter?.max_bindings)} capacity`} />
    </div>

    <div className="operations-tabs" role="tablist">
      <button className={tab === "runtime" ? "active" : ""} onClick={() => setTab("runtime")}><Gauge size={15} /> Runtime</button>
      <button className={tab === "approvals" ? "active" : ""} onClick={() => setTab("approvals")}><ShieldCheck size={15} /> Approvals{pendingCount > 0 && <span>{pendingCount}</span>}</button>
      <button className={tab === "audit" ? "active" : ""} onClick={() => setTab("audit")}><ScrollText size={15} /> Audit</button>
    </div>

    {tab === "runtime" && <RuntimePanel workspaces={workspaces} router={recordValue(operations.data?.sessionRouter)} shared={recordValue(operations.data?.sharedResources)} />}
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
  const [scope, setScope] = useState<RuntimeScope>("workspaces");
  const [selectedWorkspaceId, setSelectedWorkspaceId] = useState(() => workspaces[0]?.id ?? "");

  useEffect(() => {
    if (!workspaces.some((workspace) => workspace.id === selectedWorkspaceId)) {
      setSelectedWorkspaceId(workspaces[0]?.id ?? "");
    }
  }, [selectedWorkspaceId, workspaces]);

  const selectedWorkspace = workspaces.find((workspace) => workspace.id === selectedWorkspaceId) ?? workspaces[0];

  return <div className="operations-runtime-stack">
    <div className="operations-runtime-switch" role="tablist" aria-label="Runtime scope">
      <button className={scope === "workspaces" ? "active" : ""} onClick={() => setScope("workspaces")}><Gauge size={14} /> Workspaces <span>{workspaces.length}</span></button>
      <button className={scope === "daemon" ? "active" : ""} onClick={() => setScope("daemon")}><Activity size={14} /> Daemon</button>
    </div>

    {scope === "workspaces" && (workspaces.length === 0 ?
      <EmptyState title="No active workspaces" description="Restart Codebridge after registering a workspace to include it in runtime operations." /> :
      <div className="operations-master-detail">
        <Card title="Workspace overview" description="Select a workspace to inspect its live runtime details." className="operations-workspace-overview">
          <div className="operations-workspace-list">
            {workspaces.map((workspace) => <WorkspaceOverviewRow
              key={workspace.id}
              workspace={workspace}
              selected={workspace.id === selectedWorkspace?.id}
              onSelect={() => setSelectedWorkspaceId(workspace.id)}
            />)}
          </div>
        </Card>
        {selectedWorkspace && <WorkspaceRuntimeDetail workspace={selectedWorkspace} />}
      </div>
    )}

    {scope === "daemon" && <div className="two-column operations-system-details">
      <Card title="Session router" description="Conversation binding capacity and expiry activity."><KeyValueGrid value={router} /></Card>
      <Card title="Shared resources" description="Daemon-wide pooled providers, clients, recorders, and audit writers."><KeyValueGrid value={shared} /></Card>
    </div>}
  </div>;
}

function WorkspaceOverviewRow({ workspace, selected, onSelect }: { workspace: OperationsWorkspace; selected: boolean; onSelect: () => void }) {
  const health = workspaceHealth(workspace);
  return <button type="button" className={`operations-workspace-row ${selected ? "selected" : ""}`} onClick={onSelect} aria-pressed={selected}>
    <span className={`operations-health-dot ${health.tone}`} />
    <span className="operations-workspace-row-main">
      <span className="operations-workspace-row-title"><strong>{workspace.id}</strong><Badge tone={health.tone}>{health.label}</Badge></span>
      <small title={workspace.root}>{workspace.root}</small>
      <span className="operations-workspace-mini-metrics">
        <span><small>Calls</small><strong>{workspace.metrics.completed_calls.toLocaleString()}</strong></span>
        <span><small>Failed</small><strong className={workspace.metrics.failed ? "danger" : ""}>{workspace.metrics.failed.toLocaleString()}</strong></span>
        <span><small>Active</small><strong>{workspace.metrics.concurrency.executing}/{workspace.metrics.concurrency.limit}</strong></span>
        <span><small>Latency</small><strong>{formatMicros(workspace.metrics.latency_us.average)}</strong></span>
      </span>
    </span>
    <ChevronRight className="operations-workspace-chevron" size={17} />
  </button>;
}

function WorkspaceRuntimeDetail({ workspace }: { workspace: OperationsWorkspace }) {
  const health = workspaceHealth(workspace);
  return <Card
    title={workspace.id}
    description={workspace.root}
    className="operations-workspace-detail"
    actions={<><Badge tone={health.tone}>{health.label}</Badge><Badge tone="info">{workspace.mode}</Badge><Badge tone={workspace.policy === "full" ? "warning" : "success"}>{workspace.policy}</Badge></>}
  >
    {workspace.startupWarnings.length > 0 && <Notice tone="warning"><strong>{workspace.startupWarnings.length} startup warning{workspace.startupWarnings.length === 1 ? "" : "s"}</strong><ul>{workspace.startupWarnings.map((warning) => <li key={warning}>{warning}</li>)}</ul></Notice>}
    <div className="runtime-metric-grid operations-workspace-metrics">
      <RuntimeMetric label="Succeeded" value={workspace.metrics.succeeded} />
      <RuntimeMetric label="Failed" value={workspace.metrics.failed} danger={workspace.metrics.failed > 0} />
      <RuntimeMetric label="In flight" value={workspace.metrics.in_flight} />
      <RuntimeMetric label="Concurrency" value={`${workspace.metrics.concurrency.executing}/${workspace.metrics.concurrency.limit}`} />
      <RuntimeMetric label="Average latency" value={formatMicros(workspace.metrics.latency_us.average)} />
      <RuntimeMetric label="Uptime" value={formatDuration(workspace.metrics.uptime_seconds)} />
    </div>
    <ToolMetrics tools={workspace.metrics.tools ?? []} />
    <details className="json-details operations-json"><summary>Module health and full runtime details</summary><pre>{JSON.stringify({ modules: workspace.modules, metrics: workspace.metrics }, null, 2)}</pre></details>
  </Card>;
}

function RuntimeMetric({ label, value, danger = false }: { label: string; value: number | string; danger?: boolean }) {
  return <div className={`runtime-metric ${danger ? "danger" : ""}`}><small>{label}</small><strong>{typeof value === "number" ? value.toLocaleString() : value}</strong></div>;
}

function ToolMetrics({ tools }: { tools: RuntimeToolMetric[] }) {
  const [expanded, setExpanded] = useState(false);
  const sorted = useMemo(() => [...tools].sort((a, b) => b.completed_calls - a.completed_calls || b.failed - a.failed), [tools]);
  const visible = expanded ? sorted.slice(0, 24) : sorted.slice(0, 5);
  if (!visible.length) return <div className="operations-inline-empty">No tool calls have completed yet.</div>;
  return <>
    <div className="operations-detail-section-head"><div><strong>Tool activity</strong><small>Ordered by completed calls.</small></div>{sorted.length > 5 && <button type="button" onClick={() => setExpanded(!expanded)}>{expanded ? "Show top 5" : `Show all ${Math.min(sorted.length, 24)}`}</button>}</div>
    <div className="operations-table-wrap"><table className="operations-table"><thead><tr><th>Tool</th><th>Module</th><th>Calls</th><th>Failed</th><th>Avg latency</th><th>Last call</th></tr></thead><tbody>{visible.map((tool) => <tr key={tool.tool}><td><code>{tool.tool}</code></td><td>{tool.module}</td><td>{tool.completed_calls}</td><td className={tool.failed ? "danger" : ""}>{tool.failed}</td><td>{formatMicros(tool.latency_us.average)}</td><td>{formatTime(tool.last_call_at)}</td></tr>)}</tbody></table></div>
  </>;
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
      <div className="approval-main"><div><Badge tone={approvalTone(approval.status)}>{approval.status}</Badge><Badge>{approval.workspaceId}</Badge><span>{formatTime(approval.created)}</span></div><code>{approval.action}</code><p>{approval.reason || "No reason supplied."}</p><small>Expires {formatTime(approval.expires_at)} · {approvalActionCount(approval)} exact action{approvalActionCount(approval) === 1 ? "" : "s"}</small></div>
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

function normalizeOperationsWorkspace(workspace: OperationsWorkspace): OperationsWorkspace {
  const metrics = workspace.metrics ?? {} as OperationsWorkspace["metrics"];
  const concurrency = metrics.concurrency ?? { limit: 0, executing: 0 };
  const latency = metrics.latency_us ?? { total: 0, average: 0, max: 0 };
  const tools = Array.isArray(metrics.tools) ? metrics.tools.map((tool) => ({
    ...tool,
    started_calls: numberValue(tool.started_calls),
    completed_calls: numberValue(tool.completed_calls),
    succeeded: numberValue(tool.succeeded),
    failed: numberValue(tool.failed),
    policy_rejected: numberValue(tool.policy_rejected),
    canceled: numberValue(tool.canceled),
    deadline_exceeded: numberValue(tool.deadline_exceeded),
    in_flight: numberValue(tool.in_flight),
    max_in_flight: numberValue(tool.max_in_flight),
    latency_us: {
      total: numberValue(tool.latency_us?.total),
      average: numberValue(tool.latency_us?.average),
      max: numberValue(tool.latency_us?.max),
    },
  })) : [];
  return {
    ...workspace,
    startupWarnings: Array.isArray(workspace.startupWarnings) ? workspace.startupWarnings : [],
    modules: recordValue(workspace.modules),
    metrics: {
      ...metrics,
      started_at: typeof metrics.started_at === "string" ? metrics.started_at : "",
      uptime_seconds: numberValue(metrics.uptime_seconds),
      started_calls: numberValue(metrics.started_calls),
      completed_calls: numberValue(metrics.completed_calls),
      succeeded: numberValue(metrics.succeeded),
      failed: numberValue(metrics.failed),
      policy_rejected: numberValue(metrics.policy_rejected),
      canceled: numberValue(metrics.canceled),
      deadline_exceeded: numberValue(metrics.deadline_exceeded),
      in_flight: numberValue(metrics.in_flight),
      max_in_flight: numberValue(metrics.max_in_flight),
      concurrency: {
        limit: numberValue(concurrency.limit),
        executing: numberValue(concurrency.executing),
      },
      latency_us: {
        total: numberValue(latency.total),
        average: numberValue(latency.average),
        max: numberValue(latency.max),
      },
      tools,
      audit: recordValue(metrics.audit),
      memory_observations: numericRecord(metrics.memory_observations),
      repository_cache: recordValue(metrics.repository_cache),
    },
  };
}

function recordValue(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function numericRecord(value: unknown): Record<string, number> {
  return Object.fromEntries(Object.entries(recordValue(value)).map(([key, item]) => [key, numberValue(item)]));
}

function approvalActionCount(approval: ApprovalRecord): number {
  return Array.isArray(approval.actions) ? approval.actions.length : 0;
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

function workspaceHealth(workspace: OperationsWorkspace): { label: string; tone: "success" | "warning" | "danger" } {
  if (workspace.startupWarnings.length > 0) return { label: "Warning", tone: "danger" };
  if (workspace.metrics.failed > 0) return { label: "Failures", tone: "warning" };
  return { label: "Healthy", tone: "success" };
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
