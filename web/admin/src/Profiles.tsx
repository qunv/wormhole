import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Boxes, Cable, CopyPlus, Save, Search, ShieldCheck, Trash2, Wrench } from "lucide-react";
import { api, APIError } from "./api";
import { Badge, Button, Card, Field, LoadingPage, MultiSelect, Notice, PageHeader, Select, TextInput, Toggle } from "./components";
import type { WormholeConfig, ProfileTool, ToolProfile, ToolProfileConfig } from "./types";

type ScopeFilter = "all" | "session" | "workspace";

export function Profiles() {
  const queryClient = useQueryClient();
  const profilesQuery = useQuery({ queryKey: ["profiles"], queryFn: api.profiles });
  const configQuery = useQuery({ queryKey: ["config"], queryFn: api.config });
  const catalogQuery = useQuery({ queryKey: ["tool-catalog"], queryFn: api.toolCatalog });
  const [selectedId, setSelectedId] = useState("fast");
  const [search, setSearch] = useState("");
  const [scope, setScope] = useState<ScopeFilter>("all");
  const [newID, setNewID] = useState("");
  const [editor, setEditor] = useState<ToolProfileConfig | null>(null);
  const [assignedTunnels, setAssignedTunnels] = useState<string[]>([]);
  const [message, setMessage] = useState<{ tone: "success" | "danger" | "info"; text: string } | null>(null);

  const profiles = profilesQuery.data?.profiles ?? [];
  const selected = profiles.find((profile) => profile.id === selectedId) ?? profiles[0];
  const configSnapshot = configQuery.data;
  const configuredProfiles = configSnapshot?.config.toolProfiles ?? {};

  useEffect(() => {
    if (!selected || selected.builtIn || !configSnapshot) {
      setEditor(null);
      setAssignedTunnels([]);
      return;
    }
    setEditor(structuredClone(configuredProfiles[selected.id] ?? {
      name: selected.name,
      description: selected.description,
      outputMode: selected.outputMode,
      compactDefaults: selected.compactDefaults,
      allowedGroups: selected.allowedGroups,
      allowedTools: selected.allowedTools,
      deniedTools: selected.deniedTools,
    }));
    setAssignedTunnels(Object.entries(configSnapshot.config.tunnels ?? {})
      .filter(([, tunnel]) => effectiveTunnelProfile(tunnel.mode, tunnel.toolProfile) === selected.id)
      .map(([name]) => name));
  }, [selected?.id, selected?.contractHash, configSnapshot?.revision]);

  const saveConfig = useMutation({
    mutationFn: ({ config, success }: { config: WormholeConfig; success: string }) =>
      api.saveConfig(config, configSnapshot!.revision).then((result) => ({ result, success })),
    onSuccess: async ({ result, success }) => {
      queryClient.setQueryData(["config"], result);
      setMessage({ tone: "success", text: success });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["profiles"] }),
        queryClient.invalidateQueries({ queryKey: ["tool-catalog"] }),
      ]);
    },
    onError: (error) => setMessage({ tone: "danger", text: errorMessage(error) }),
  });

  if (profilesQuery.isLoading || configQuery.isLoading || !configSnapshot) return <LoadingPage />;
  if (profilesQuery.error || !profilesQuery.data) return <Notice tone="danger">{errorMessage(profilesQuery.error) || "Unable to load tool profiles."}</Notice>;
  if (!selected) return <Notice tone="warning">No tool profiles are available.</Notice>;

  const groupOptions = (catalogQuery.data?.groups ?? []).map((group) => ({ value: group.name, label: group.name, description: `${group.toolCount} tools` }));
  const toolOptions = (catalogQuery.data?.tools ?? []).map((tool) => ({ value: tool.name, label: tool.name, description: `${tool.title} · ${tool.groups.join(", ")}` }));
  const tunnelOptions = Object.entries(configSnapshot.config.tunnels ?? {}).map(([name, tunnel]) => ({
    value: name,
    label: name,
    description: `${tunnel.enabled === false ? "disabled" : "enabled"} · ${tunnel.tunnelId || "Tunnel ID missing"}`,
  }));

  const create = () => {
    const id = newID.trim().toLowerCase();
    if (!validProfileID(id) || profiles.some((profile) => profile.id === id)) return;
    const next: WormholeConfig = structuredClone(configSnapshot.config);
    next.toolProfiles = { ...(next.toolProfiles ?? {}), [id]: { name: id, outputMode: "both" } };
    saveConfig.mutate({ config: next, success: `Profile ${id} was saved. Restart Wormhole to activate its endpoints.` });
    setSelectedId(id);
    setNewID("");
  };

  const saveProfile = () => {
    if (!editor || selected.builtIn) return;
    const next: WormholeConfig = structuredClone(configSnapshot.config);
    next.toolProfiles = { ...(next.toolProfiles ?? {}), [selected.id]: editor };
    const tunnels = { ...(next.tunnels ?? {}) };
    for (const [name, tunnel] of Object.entries(tunnels)) {
      const currentlyAssigned = effectiveTunnelProfile(tunnel.mode, tunnel.toolProfile) === selected.id;
      const shouldAssign = assignedTunnels.includes(name);
      if (shouldAssign) tunnels[name] = { ...tunnel, toolProfile: selected.id };
      else if (currentlyAssigned) tunnels[name] = { ...tunnel, toolProfile: "" };
    }
    next.tunnels = tunnels;
    saveConfig.mutate({ config: next, success: `Profile ${selected.id} was saved. Restart Wormhole to activate the new contract.` });
  };

  const removeProfile = () => {
    if (selected.builtIn) return;
    const next: WormholeConfig = structuredClone(configSnapshot.config);
    const profiles = { ...(next.toolProfiles ?? {}) };
    delete profiles[selected.id];
    next.toolProfiles = profiles;
    const tunnels = { ...(next.tunnels ?? {}) };
    for (const [name, tunnel] of Object.entries(tunnels)) {
      if (effectiveTunnelProfile(tunnel.mode, tunnel.toolProfile) === selected.id) tunnels[name] = { ...tunnel, toolProfile: "" };
    }
    next.tunnels = tunnels;
    saveConfig.mutate({ config: next, success: `Profile ${selected.id} was removed. Restart Wormhole to remove its endpoints.` });
    setSelectedId("fast");
  };

  return <>
    <PageHeader
      eyebrow="Effective MCP contracts"
      title="Tool profiles"
      description="Inspect built-in contracts and create persisted custom profiles that filter the globally enabled runtime catalog."
    />
    {message && <Notice tone={message.tone}>{message.text}</Notice>}
    <Card title="Create custom profile" description="Profile IDs become stable MCP endpoint suffixes and cannot be renamed after creation.">
      <div className="profile-create-row">
        <TextInput value={newID} onChange={(event) => setNewID(event.target.value.toLowerCase())} placeholder="review" />
        <Button onClick={create} disabled={!validProfileID(newID) || profiles.some((profile) => profile.id === newID.trim().toLowerCase()) || saveConfig.isPending}><CopyPlus size={15} /> Create</Button>
      </div>
      <small className="muted">Use lowercase letters, numbers, underscore, or hyphen. The reserved IDs fast and full cannot be replaced.</small>
    </Card>
    <div className="profile-selector">
      {profiles.map((profile) => <ProfileButton key={profile.id} profile={profile} active={profile.id === selected.id} onClick={() => setSelectedId(profile.id)} />)}
    </div>
    {selected.restartRequired && <Notice tone="warning"><strong>{selected.active ? "Saved configuration differs from the active runtime." : "Configured but not active."}</strong> Restart Wormhole before relying on <code>{selected.endpoint}</code>.</Notice>}
    <ProfileSummary profile={selected} workspaceCount={profilesQuery.data.workspaceCount} />

    {!selected.builtIn && editor && <Card
      title={`Edit ${selected.id}`}
      description="Allow rules form a union; denied tools always win. Global tool exposure remains the outer security boundary."
      actions={<><Button variant="danger" onClick={removeProfile} disabled={saveConfig.isPending}><Trash2 size={14} /> Delete</Button><Button onClick={saveProfile} loading={saveConfig.isPending}><Save size={14} /> Save profile</Button></>}
    >
      <div className="form-grid profile-editor-grid">
        <Field label="Display name"><TextInput value={editor.name ?? ""} onChange={(event) => setEditor({ ...editor, name: event.target.value })} /></Field>
        <Field label="Output mode"><Select value={editor.outputMode ?? "both"} onChange={(event) => setEditor({ ...editor, outputMode: event.target.value })}><option value="both">both</option><option value="structured">structured</option><option value="text">text</option></Select></Field>
        <Field label="Description" wide><TextInput value={editor.description ?? ""} onChange={(event) => setEditor({ ...editor, description: event.target.value })} /></Field>
        <Field label="Allowed groups" wide><MultiSelect options={groupOptions} value={editor.allowedGroups ?? []} onChange={(allowedGroups) => setEditor({ ...editor, allowedGroups })} placeholder="All groups when both allow lists are empty" searchPlaceholder="Search groups…" /></Field>
        <Field label="Allowed tools" wide><MultiSelect options={toolOptions} value={editor.allowedTools ?? []} onChange={(allowedTools) => setEditor({ ...editor, allowedTools })} placeholder="No exact tool allowlist" searchPlaceholder="Search tools…" /></Field>
        <Field label="Denied tools" wide><MultiSelect options={toolOptions} value={editor.deniedTools ?? []} onChange={(deniedTools) => setEditor({ ...editor, deniedTools })} placeholder="No denied tools" searchPlaceholder="Search tools…" /></Field>
        <Field label="Assigned named tunnels" wide><MultiSelect options={tunnelOptions} value={assignedTunnels} onChange={setAssignedTunnels} placeholder="No named tunnel uses this profile" searchPlaceholder="Search tunnels…" /></Field>
        <div className="field-wide toggle-stack"><Toggle checked={!!editor.compactDefaults} onChange={(compactDefaults) => setEditor({ ...editor, compactDefaults })} label="Apply compact defaults" description="Use Fast-like defaults for workspace_snapshot, task_context, and codegraph_explore when arguments are omitted." /></div>
      </div>
    </Card>}

    <Card
      title={`${selected.name} tools`}
      description={`${selected.tools.length} tools exposed through ${selected.endpoint}. Session controls are always available; workspace tools require workspace_select.`}
      actions={<><Badge tone={selected.builtIn ? "info" : "neutral"}>{selected.builtIn ? "built-in" : "custom"}</Badge><Badge>{selected.contractHash}</Badge></>}
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
      <span><strong>{profile.name}</strong><Badge tone={profile.restartRequired ? "warning" : "success"}>{profile.restartRequired ? "restart" : `${profile.tools.length} tools`}</Badge></span>
      <small>{profile.description}</small>
      <code>{profile.endpoint}</code>
    </span>
    <span className="profile-option-meta">{sessionCount} controls · {profile.outputMode}</span>
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
    <SummaryMetric icon={<Cable />} label="Enabled tunnels" value={String(enabledTunnels.length)} detail={enabledTunnels.map((item) => item.name).join(", ") || "No tunnel uses this profile"} />
  </div>;
}

function SummaryMetric({ icon, label, value, detail }: { icon: React.ReactNode; label: string; value: string; detail: string }) {
  return <div className="profile-summary-item"><span>{icon}</span><div><small>{label}</small><strong>{value}</strong><em>{detail}</em></div></div>;
}

function ToolCatalog({ profile, search, onSearch, scope, onScope }: { profile: ToolProfile; search: string; onSearch: (value: string) => void; scope: ScopeFilter; onScope: (value: ScopeFilter) => void }) {
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
      <div className="profile-scope-filter">{(["all", "session", "workspace"] as ScopeFilter[]).map((item) => <button key={item} className={scope === item ? "active" : ""} onClick={() => onScope(item)}>{item}</button>)}</div>
      <span className="muted">Showing {tools.length} of {profile.tools.length}</span>
    </div>
    <div className="profile-tool-list">{tools.map((tool) => <ToolRow key={tool.name} tool={tool} />)}{!tools.length && <div className="empty"><Search size={28} /><strong>No matching tools</strong><p>Change the search text or scope filter.</p></div>}</div>
  </>;
}

function ToolRow({ tool }: { tool: ProfileTool }) {
  return <article className="profile-tool-row"><div className="profile-tool-main"><div className="profile-tool-title"><code>{tool.name}</code><strong>{tool.title}</strong></div><p>{tool.description}</p>{tool.scope === "workspace" && <small>Available in: {tool.workspaceIds.join(", ") || "no active workspace"}</small>}</div><div className="profile-tool-badges"><Badge tone={tool.scope === "session" ? "info" : "neutral"}>{tool.scope}</Badge><Badge tone={tool.readOnly ? "success" : "warning"}>{tool.readOnly ? "read-only" : "mutating"}</Badge>{tool.destructive && <Badge tone="danger">destructive</Badge>}{tool.openWorld && <Badge tone="warning">open-world</Badge>}</div></article>;
}

function effectiveTunnelProfile(mode?: string, toolProfile?: string): string {
  if (toolProfile?.trim()) return toolProfile.trim().toLowerCase();
  return mode === "fast" ? "fast" : "full";
}

function validProfileID(value: string): boolean {
  const id = value.trim().toLowerCase();
  return /^[a-z][a-z0-9_-]{0,31}$/.test(id) && id !== "fast" && id !== "full";
}

const errorMessage = (error: unknown) => error instanceof APIError ? error.message : error instanceof Error ? error.message : error ? String(error) : "";
