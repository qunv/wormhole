import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Boxes, Cable, Search, ShieldCheck, Wrench } from "lucide-react";
import { api, APIError } from "./api";
import { Badge, Card, LoadingPage, Notice, PageHeader, TextInput } from "./components";
import type { ProfileTool, ToolProfile } from "./types";

type ScopeFilter = "all" | "session" | "workspace";

export function Profiles() {
  const query = useQuery({ queryKey: ["profiles"], queryFn: api.profiles });
  const [selectedId, setSelectedId] = useState("fast");
  const [search, setSearch] = useState("");
  const [scope, setScope] = useState<ScopeFilter>("all");

  if (query.isLoading) return <LoadingPage />;
  if (query.error || !query.data) {
    return <Notice tone="danger">{errorMessage(query.error) || "Unable to load tool profiles."}</Notice>;
  }

  const profiles = query.data.profiles;
  const selected = profiles.find((profile) => profile.id === selectedId) ?? profiles[0];
  if (!selected) return <Notice tone="warning">No tool profiles are available.</Notice>;

  return <>
    <PageHeader
      eyebrow="Effective MCP contracts"
      title="Tool profiles"
      description="Inspect the exact session tools exposed by each Fast or Full profile after runtime tool policy and workspace availability are applied."
    />
    <div className="profile-selector">
      {profiles.map((profile) => <ProfileButton
        key={profile.id}
        profile={profile}
        active={profile.id === selected.id}
        onClick={() => setSelectedId(profile.id)}
      />)}
    </div>
    <ProfileSummary profile={selected} workspaceCount={query.data.workspaceCount} />
    <Card
      title={`${selected.name} tools`}
      description={`${selected.tools.length} tools exposed through ${selected.endpoint}. Session controls are always available; workspace tools require workspace_select.`}
      actions={<Badge tone={selected.id === "fast" ? "info" : "success"}>{selected.id}</Badge>}
    >
      <ToolCatalog profile={selected} search={search} onSearch={setSearch} scope={scope} onScope={setScope} />
    </Card>
  </>;
}

function ProfileButton({ profile, active, onClick }: { profile: ToolProfile; active: boolean; onClick: () => void }) {
  const sessionCount = profile.tools.filter((tool) => tool.scope === "session").length;
  return <button className={`profile-option ${active ? "active" : ""}`} onClick={onClick}>
    <span className="profile-option-icon">{profile.id === "fast" ? <Wrench size={19} /> : <Boxes size={19} />}</span>
    <span className="profile-option-copy">
      <span><strong>{profile.name}</strong><Badge tone={profile.id === "fast" ? "info" : "success"}>{profile.tools.length} tools</Badge></span>
      <small>{profile.description}</small>
      <code>{profile.endpoint}</code>
    </span>
    <span className="profile-option-meta">{sessionCount} session controls</span>
  </button>;
}

function ProfileSummary({ profile, workspaceCount }: { profile: ToolProfile; workspaceCount: number }) {
  const sessionCount = profile.tools.filter((tool) => tool.scope === "session").length;
  const workspaceTools = profile.tools.length - sessionCount;
  const enabledTunnels = profile.tunnels.filter((tunnel) => tunnel.enabled);
  return <div className="profile-summary-grid">
    <SummaryMetric icon={<Wrench />} label="Exposed tools" value={String(profile.tools.length)} detail={`${workspaceTools} workspace tools`} />
    <SummaryMetric icon={<ShieldCheck />} label="Session controls" value={String(sessionCount)} detail="No workspace binding needed" />
    <SummaryMetric icon={<Boxes />} label="Workspaces" value={String(workspaceCount)} detail="Availability shown per tool" />
    <SummaryMetric icon={<Cable />} label="Enabled tunnels" value={String(enabledTunnels.length)} detail={enabledTunnels.map((item) => item.name).join(", ") || "No tunnel uses this mode"} />
  </div>;
}

function SummaryMetric({ icon, label, value, detail }: { icon: React.ReactNode; label: string; value: string; detail: string }) {
  return <div className="profile-summary-item"><span>{icon}</span><div><small>{label}</small><strong>{value}</strong><em>{detail}</em></div></div>;
}

function ToolCatalog({ profile, search, onSearch, scope, onScope }: {
  profile: ToolProfile;
  search: string;
  onSearch: (value: string) => void;
  scope: ScopeFilter;
  onScope: (value: ScopeFilter) => void;
}) {
  const tools = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return profile.tools.filter((tool) => {
      if (scope !== "all" && tool.scope !== scope) return false;
      return !needle || tool.name.toLowerCase().includes(needle) || tool.title.toLowerCase().includes(needle) || tool.description.toLowerCase().includes(needle);
    });
  }, [profile.tools, scope, search]);

  return <>
    <div className="profile-tool-toolbar">
      <label className="profile-tool-search"><Search size={15} /><TextInput value={search} onChange={(event) => onSearch(event.target.value)} placeholder="Search tool name or description" /></label>
      <div className="profile-scope-filter">
        {(["all", "session", "workspace"] as ScopeFilter[]).map((item) => <button key={item} className={scope === item ? "active" : ""} onClick={() => onScope(item)}>{item}</button>)}
      </div>
      <span className="muted">Showing {tools.length} of {profile.tools.length}</span>
    </div>
    <div className="profile-tool-list">
      {tools.map((tool) => <ToolRow key={tool.name} tool={tool} />)}
      {!tools.length && <div className="empty"><Search size={28} /><strong>No matching tools</strong><p>Change the search text or scope filter.</p></div>}
    </div>
  </>;
}

function ToolRow({ tool }: { tool: ProfileTool }) {
  return <article className="profile-tool-row">
    <div className="profile-tool-main">
      <div className="profile-tool-title"><code>{tool.name}</code><strong>{tool.title}</strong></div>
      <p>{tool.description}</p>
      {tool.scope === "workspace" && <small>Available in: {tool.workspaceIds.join(", ") || "no active workspace"}</small>}
    </div>
    <div className="profile-tool-badges">
      <Badge tone={tool.scope === "session" ? "info" : "neutral"}>{tool.scope}</Badge>
      <Badge tone={tool.readOnly ? "success" : "warning"}>{tool.readOnly ? "read-only" : "mutating"}</Badge>
      {tool.destructive && <Badge tone="danger">destructive</Badge>}
      {tool.openWorld && <Badge tone="warning">open-world</Badge>}
    </div>
  </article>;
}

const errorMessage = (error: unknown) => error instanceof APIError ? error.message : error instanceof Error ? error.message : error ? String(error) : "";
