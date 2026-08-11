import { useEffect, useMemo, useState, type ComponentProps } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Braces, Copy, Cpu, Database, ExternalLink, Gauge, Globe2, KeyRound, Network, Plus, RefreshCw, Save, Trash2, Waypoints } from "lucide-react";
import { api, APIError } from "./api";
import { Badge, Button, Card, Field as BaseField, LoadingPage, MultiSelect, Notice, PageHeader, Select, TextArea, TextInput, Toggle as BaseToggle } from "./components";
import type {
  WormholeConfig,
  MCPServerConfig,
  ProfilesResponse,
  RemoteIngressConfig,
  RemoteIngressStatusResponse,
  SecretsResponse,
  ToolCatalogResponse,
  UpstreamMCPStatus,
  WorkspacesResponse,
} from "./types";

type Tab = "general" | "memory" | "mcp" | "tools" | "advanced";
const tabs: { id: Tab; label: string; icon: React.ReactNode }[] = [
  { id: "general", label: "General & Tunnel", icon: <Network size={16} /> },
  { id: "memory", label: "Memory", icon: <Database size={16} /> },
  { id: "mcp", label: "MCP Servers", icon: <Waypoints size={16} /> },
  { id: "tools", label: "Tools & Limits", icon: <Gauge size={16} /> },
  { id: "advanced", label: "Advanced JSON", icon: <Braces size={16} /> },
];

const CONFIG_HELP: Record<string, string> = {
  "Primary workspace": "The default repository root used when a request is not bound to a named workspace.",
  "Execution mode": "Controls which local operations are available. Safe mode restricts risky execution; full mode enables the complete tool set subject to policy.",
  "Policy": "Determines when mutating or sensitive actions require approval. Strict asks more often; full permits the broadest execution.",
  "Host": "Network interface used by the MCP listener. The Admin UI remains restricted to loopback for security.",
  "Port": "TCP port used by the local Wormhole HTTP and MCP server.",
  "Extra roots": "Additional directories that tools may access besides the primary workspace. Each path must pass root-confinement checks.",
  "Allowed browser origins": "Exact browser origins permitted to call the local server. Use full origins such as http://127.0.0.1:3000.",
  "Disable tunnel": "Prevents Wormhole from creating the external Secure MCP Tunnel and keeps the service local only.",
  "Tunnel ID": "Identifier of the Secure MCP Tunnel that exposes this local Wormhole daemon.",
  "Organization ID": "Organization that owns or authorizes the configured tunnel.",
  "Tunnel binary": "Optional custom path or command name for the tunnel executable.",
  "Profile": "Named tunnel profile used to resolve tunnel credentials and settings.",
  "Profile directory": "Directory containing tunnel profile files when they are not stored in the default location.",
  "Runtime key environment": "Environment variable name containing the runtime API key. The secret value is managed on the Secrets page.",
  "Tunnel definitions": "Named independently managed OpenAI Secure MCP Tunnels. Each tunnel exposes a selected session profile as its main ChatGPT channel.",
  "Remote MCP ingresses": "Dedicated loopback-only hosted-MCP listeners for Notion and other remote clients. External publishers are the generic default; Cloudflare can optionally be managed by Wormhole.",
  "Audit tool calls": "Records bounded local audit events for tool calls, outcomes, and approvals without storing full results.",
  "Include redacted argument metadata": "Adds reduced and redacted argument metadata to audit records for better diagnostics.",
  "HTTP access log": "Logs Admin and MCP HTTP requests locally. Useful for troubleshooting but can add noise.",
  "Enable memory": "Allows Wormhole to capture selected project context and retrieve it in later agent sessions.",
  "Provider": "Memory backend adapter to use, for example agentmemory.",
  "Endpoint": "Base URL of the memory provider service.",
  "Secret environment": "Environment variable name containing the memory provider credential; the value is never stored here.",
  "Agent ID": "Stable identity sent to the memory provider to separate observations from different agents.",
  "Capture mode": "Controls automatic memory capture: off disables capture, metadata stores reduced metadata, and selected stores approved contextual observations.",
  "Project strategy": "Defines how projects receive stable memory identities. git-origin follows the repository remote; path-hash derives identity from the local path.",
  "Required for startup": "When enabled, Wormhole startup fails if the memory provider is unavailable instead of continuing without memory.",
  "Request timeout (ms)": "Maximum time allowed for a memory provider request before it is cancelled.",
  "Token budget": "Maximum amount of retrieved memory context that may be inserted into an agent prompt.",
  "Queue size": "Maximum number of memory observations waiting for asynchronous delivery.",
  "Delivery workers": "Number of concurrent workers that send queued memory observations.",
  "Delivery timeout (ms)": "Maximum time allowed for one queued memory delivery attempt.",
  "Retry attempts": "Maximum number of delivery attempts before an observation is dropped or reported as failed.",
  "Retry backoff (ms)": "Delay between memory delivery retries.",
  "Health cache (ms)": "How long a memory provider health result is reused before checking again.",
  "Provider options": "Provider-specific non-secret settings passed to the selected memory adapter.",
  "Server enabled": "Controls whether this upstream MCP server is available to Wormhole workspaces.",
  "Transport": "Connection type for the upstream MCP server: a local stdio process or a Streamable HTTP endpoint.",
  "Startup mode": "Eager starts during daemon initialization, background starts asynchronously, and lazy connects on the first tool request.",
  "Command": "Executable used to start a stdio MCP server.",
  "Arguments": "Command-line arguments passed to the stdio MCP server, one argument per line.",
  "Working directory": "Directory used as the current working directory when starting the stdio MCP process.",
  "URL": "Streamable HTTP endpoint of the upstream MCP server.",
  "Allow remote endpoint": "Allows a non-loopback upstream URL. Enable only for a trusted, secured network endpoint.",
  "Workspace IDs": "Limits this upstream server to selected workspace IDs. Empty or * makes it available to every workspace.",
  "Allowed tools": "Allowlist of tool names exposed from this scope. When populated, tools not listed are hidden.",
  "Denied tools": "Denylist of tool names that must never be exposed, even when another allow rule matches.",
  "Max concurrency": "Maximum number of simultaneous calls sent to this upstream MCP server.",
  "Max tools": "Maximum number of tools accepted from the upstream server catalog.",
  "Complete server JSON": "Advanced MCP server settings including environment references, headers, policy, timeouts, and transport limits.",
  "Allowed groups": "Tool groups that may be exposed globally, such as filesystem, git, execution, or memory groups.",
  "Max read chars": "Hard upper bound for characters returned by a single file read request.",
  "Default read chars": "Default character limit used when a file read request does not specify one.",
  "Max batch read chars": "Hard upper bound for the combined output of a multi-file read request.",
  "Max command output": "Hard upper bound for captured stdout and stderr from one command.",
  "Default command output": "Default command output limit when a tool call does not specify one.",
  "Max HTTP body bytes": "Largest HTTP request body accepted by the local Admin and MCP server.",
  "Max managed processes": "Maximum number of background processes Wormhole may manage at the same time.",
  "Max concurrent tool calls": "Maximum number of tool requests that may execute concurrently inside one workspace runtime. Additional calls wait and remain cancellable.",
  "Git status cache (ms)": "How long a git status result is reused before Wormhole executes git status again.",
  "Complete JSON document": "Raw editor for the entire non-secret configuration. Changes are applied to the structured form before validation and saving.",
};

function Field(props: ComponentProps<typeof BaseField>) {
  return <BaseField {...props} help={props.help ?? CONFIG_HELP[props.label]} />;
}

function Toggle(props: ComponentProps<typeof BaseToggle>) {
  return <BaseToggle {...props} help={props.help ?? CONFIG_HELP[props.label]} />;
}

export function Configuration() {
  const queryClient = useQueryClient();
  const snapshot = useQuery({ queryKey: ["config"], queryFn: api.config });
  const toolCatalog = useQuery({ queryKey: ["tool-catalog"], queryFn: api.toolCatalog });
  const profiles = useQuery({ queryKey: ["profiles"], queryFn: api.profiles });
  const workspaces = useQuery({ queryKey: ["workspaces"], queryFn: api.workspaces });
  const secrets = useQuery({ queryKey: ["secrets"], queryFn: api.secrets });
  const remoteIngressStatus = useQuery({ queryKey: ["remote-ingresses"], queryFn: api.remoteIngresses, refetchInterval: 15_000 });
  const [tab, setTab] = useState<Tab>("general");
  const [draft, setDraft] = useState<WormholeConfig | null>(null);
  const [dirty, setDirty] = useState(false);
  const [message, setMessage] = useState<{ tone: "success" | "danger" | "info"; text: string } | null>(null);

  useEffect(() => {
    if (snapshot.data && !dirty) setDraft(structuredClone(snapshot.data.config));
  }, [snapshot.data, dirty]);

  const validate = useMutation({
    mutationFn: () => api.validateConfig(draft!),
    onSuccess: ({ config }) => {
      setDraft(config);
      setMessage({ tone: "success", text: "Configuration is valid." });
    },
    onError: (error) => setMessage({ tone: "danger", text: errorMessage(error) }),
  });

  const save = useMutation({
    mutationFn: () => api.saveConfig(draft!, snapshot.data!.revision),
    onSuccess: (data) => {
      queryClient.setQueryData(["config"], data);
      setDraft(structuredClone(data.config));
      setDirty(false);
      setMessage({ tone: "success", text: "Saved safely. Restart Wormhole to activate the new configuration." });
      void queryClient.invalidateQueries({ queryKey: ["secrets"] });
      void queryClient.invalidateQueries({ queryKey: ["workspaces"] });
    },
    onError: (error) => setMessage({ tone: "danger", text: errorMessage(error) }),
  });

  const restart = useMutation({
    mutationFn: async () => {
      let saved = snapshot.data!;
      if (dirty) saved = await api.saveConfig(draft!, snapshot.data!.revision);
      const scheduled = await api.restart();
      return { saved, scheduled };
    },
    onSuccess: ({ saved, scheduled }) => {
      queryClient.setQueryData(["config"], saved);
      setDraft(structuredClone(saved.config));
      setDirty(false);
      setMessage({ tone: "info", text: scheduled.message ?? "Restart scheduled. Waiting for Wormhole to return…" });
      void waitForDaemon(scheduled.retryAfterMs);
    },
    onError: (error) => setMessage({ tone: "danger", text: errorMessage(error) }),
  });

  if (snapshot.isLoading || !draft || !snapshot.data) return <LoadingPage />;

  const update = (next: WormholeConfig) => {
    setDraft(next);
    setDirty(true);
    setMessage(null);
  };

  return (
    <>
      <PageHeader
        eyebrow="Persisted global config"
        title="Configuration"
        description="Edit the complete non-secret configuration with server-side validation and revision conflict protection."
        actions={<><Badge tone={dirty ? "warning" : "success"}>{dirty ? "Unsaved changes" : "In sync"}</Badge><Button variant="secondary" onClick={() => validate.mutate()} loading={validate.isPending}>Validate</Button><Button variant="secondary" onClick={() => save.mutate()} loading={save.isPending} disabled={!dirty || restart.isPending}><Save size={15} /> Save</Button><Button onClick={() => restart.mutate()} loading={restart.isPending} disabled={save.isPending}><RefreshCw size={15} /> {dirty ? "Save & Restart" : "Restart"}</Button></>}
      />
      {message && <Notice tone={message.tone}>{message.text}</Notice>}
      <Notice tone="info">Secrets are intentionally excluded from this document. Configure referenced environment variables from the <strong>Secrets</strong> page.</Notice>
      <div className="config-layout">
        <nav className="subnav" aria-label="Configuration sections">
          {tabs.map((item) => <button key={item.id} className={tab === item.id ? "active" : ""} onClick={() => setTab(item.id)}>{item.icon}<span>{item.label}</span></button>)}
        </nav>
        <div className="config-content">
          {tab === "general" && <GeneralEditor
            value={draft}
            onChange={update}
            profiles={profiles.data}
            workspaces={workspaces.data}
            secrets={secrets.data}
            remoteStatus={remoteIngressStatus.data}
            remoteStatusLoading={remoteIngressStatus.isFetching}
            onRefreshRemoteStatus={() => void remoteIngressStatus.refetch()}
          />}
          {tab === "memory" && <MemoryEditor value={draft} onChange={update} />}
          {tab === "mcp" && <MCPServersEditor value={draft} onChange={update} />}
          {tab === "tools" && <ToolsEditor value={draft} onChange={update} catalog={toolCatalog.data} catalogLoading={toolCatalog.isLoading} />}
          {tab === "advanced" && <AdvancedEditor value={draft} onChange={update} />}
        </div>
      </div>
    </>
  );
}

interface GeneralEditorProps extends EditorProps {
  profiles?: ProfilesResponse;
  workspaces?: WorkspacesResponse;
  secrets?: SecretsResponse;
  remoteStatus?: RemoteIngressStatusResponse;
  remoteStatusLoading: boolean;
  onRefreshRemoteStatus: () => void;
}

function GeneralEditor({ value, onChange, profiles, workspaces, secrets, remoteStatus, remoteStatusLoading, onRefreshRemoteStatus }: GeneralEditorProps) {
  const set = <K extends keyof WormholeConfig>(key: K, next: WormholeConfig[K]) => onChange({ ...value, [key]: next });
  return <div className="stack">
    <Card title="Runtime and access" description="Safe defaults are recommended for daily development.">
      <div className="form-grid">
        <Field label="Primary workspace" hint="Must be an existing local directory." wide><TextInput value={value.workspace} onChange={(e) => set("workspace", e.target.value)} /></Field>
        <Field label="Execution mode"><Select value={value.mode} onChange={(e) => set("mode", e.target.value)}><option value="safe">safe</option><option value="full">full</option></Select></Field>
        <Field label="Policy"><Select value={value.policy} onChange={(e) => set("policy", e.target.value)}><option value="strict">strict</option><option value="balanced">balanced</option><option value="full">full</option></Select></Field>
        <Field label="Host" hint="Admin remains loopback-only even if MCP host changes."><TextInput value={value.host} onChange={(e) => set("host", e.target.value)} /></Field>
        <Field label="Port"><TextInput type="number" min={1} max={65535} value={value.port} onChange={(e) => set("port", number(e.target.value))} /></Field>
        <ListField label="Extra roots" value={value.extraRoots ?? []} onChange={(next) => set("extraRoots", next)} hint="One absolute path per line." />
        <ListField label="Allowed browser origins" value={value.allowedOrigins ?? []} onChange={(next) => set("allowedOrigins", next)} hint="Exact origins only; one per line." />
      </div>
    </Card>
    <Card title="Secure MCP Tunnel" description="Runtime keys stay in .env and are never returned by this UI.">
      <div className="form-grid">
        <div className="field-wide toggle-stack"><Toggle checked={!!value.noTunnel} onChange={(next) => set("noTunnel", next)} label="Disable tunnel" description="Run only the local MCP listener." /></div>
        <Field label="Tunnel ID"><TextInput value={value.tunnelId ?? ""} onChange={(e) => set("tunnelId", e.target.value)} placeholder="tunnel_…" /></Field>
        <Field label="Organization ID"><TextInput value={value.organizationId ?? ""} onChange={(e) => set("organizationId", e.target.value)} /></Field>
        <Field label="Tunnel binary" wide><TextInput value={value.tunnelBin ?? ""} onChange={(e) => set("tunnelBin", e.target.value)} /></Field>
        <Field label="Profile"><TextInput value={value.profile ?? ""} onChange={(e) => set("profile", e.target.value)} /></Field>
        <Field label="Profile directory"><TextInput value={value.profileDir ?? ""} onChange={(e) => set("profileDir", e.target.value)} /></Field>
        <Field label="Runtime key environment"><TextInput value={value.runtimeKeyEnv ?? ""} onChange={(e) => set("runtimeKeyEnv", e.target.value)} /></Field>
      </div>
      <JSONField label="Tunnel definitions" value={value.tunnels ?? {}} onChange={(tunnels) => set("tunnels", tunnels as WormholeConfig["tunnels"])} hint="Example: fast/full entries with tunnelId, mode, profile and runtimeKeyEnv." />
    </Card>
    <RemoteIngressEditor
      value={value}
      onChange={onChange}
      profiles={profiles}
      workspaces={workspaces}
      secrets={secrets}
      runtimeStatus={remoteStatus}
      runtimeStatusLoading={remoteStatusLoading}
      onRefreshRuntimeStatus={onRefreshRemoteStatus}
    />
    <Card title="Observability" description="Audit records are local, bounded and redacted by the runtime.">
      <div className="toggle-stack">
        <Toggle checked={value.audit} onChange={(next) => set("audit", next)} label="Audit tool calls" description="Recommended for traceability." />
        <Toggle checked={value.auditArgs} onChange={(next) => set("auditArgs", next)} label="Include redacted argument metadata" />
        <Toggle checked={!!value.httpLog} onChange={(next) => set("httpLog", next)} label="HTTP access log" description="Useful for debugging; disabled by default to reduce noise." />
      </div>
    </Card>
  </div>;
}

function RemoteIngressEditor({ value, onChange, profiles, workspaces, secrets, runtimeStatus, runtimeStatusLoading, onRefreshRuntimeStatus }: {
  value: WormholeConfig;
  onChange: (value: WormholeConfig) => void;
  profiles?: ProfilesResponse;
  workspaces?: WorkspacesResponse;
  secrets?: SecretsResponse;
  runtimeStatus?: RemoteIngressStatusResponse;
  runtimeStatusLoading: boolean;
  onRefreshRuntimeStatus: () => void;
}) {
  const queryClient = useQueryClient();
  const ingresses = value.remoteIngresses ?? {};
  const names = Object.keys(ingresses).sort();
  const [selected, setSelected] = useState(names[0] ?? "");
  const [newName, setNewName] = useState("");
  const [oneTimeBearer, setOneTimeBearer] = useState("");
  const [oneTimeBearerEnv, setOneTimeBearerEnv] = useState("");
  const [credentialMessage, setCredentialMessage] = useState<{ tone: "success" | "danger" | "info"; text: string } | null>(null);
  const [copyMessage, setCopyMessage] = useState("");
  const current = selected ? ingresses[selected] : undefined;
  const normalizedNewName = newName.trim().toLowerCase();
  const validNewName = validRemoteIngressName(normalizedNewName) && !ingresses[normalizedNewName];

  useEffect(() => {
    if (selected && !ingresses[selected]) setSelected(names[0] ?? "");
    if (!selected && names.length) setSelected(names[0]);
  }, [names.join("|"), selected, ingresses]);

  useEffect(() => {
    setCredentialMessage(null);
  }, [selected]);

  useEffect(() => {
    setOneTimeBearer("");
    setOneTimeBearerEnv("");
    setCopyMessage("");
  }, [selected, current?.authTokenEnv, current?.authTokenFallbackEnv]);

  const updateIngress = (next: RemoteIngressConfig) => {
    if (!selected) return;
    onChange({ ...value, remoteIngresses: { ...ingresses, [selected]: next } });
  };
  const addIngress = () => {
    if (!validNewName) return;
    const localPort = nextRemoteIngressPort(value);
    if (!localPort) return;
    const next: RemoteIngressConfig = {
      enabled: true,
      provider: "external",
      localPort,
      toolProfile: "remote-read",
      authTokenEnv: derivedRemoteIngressEnv(normalizedNewName, "AUTH_TOKEN"),
    };
    onChange({ ...value, remoteIngresses: { ...ingresses, [normalizedNewName]: next } });
    setSelected(normalizedNewName);
    setNewName("");
  };
  const removeIngress = () => {
    if (!selected) return;
    const next = { ...ingresses };
    delete next[selected];
    onChange({ ...value, remoteIngresses: next });
    setSelected(Object.keys(next).sort()[0] ?? "");
  };
  const changeProvider = (provider: "external" | "cloudflare") => {
    if (!current) return;
    const next: RemoteIngressConfig = { ...current, provider };
    if (provider === "cloudflare") {
      next.providerTokenEnv = current.providerTokenEnv?.trim() || derivedRemoteIngressEnv(selected, "TUNNEL_TOKEN");
      next.binary = current.binary?.trim() || "cloudflared";
    } else {
      delete next.providerTokenEnv;
      delete next.binary;
    }
    updateIngress(next);
  };

  const storeGeneratedBearer = async (envName: string, staged: boolean) => {
    const token = generateRemoteBearer();
    setCredentialMessage({ tone: "info", text: staged ? "Storing the staged rotation bearer…" : "Storing the new bearer in Wormhole…" });
    try {
      await api.setSecret(envName, token);
      setOneTimeBearer(token);
      setOneTimeBearerEnv(envName);
      setCredentialMessage({
        tone: "success",
        text: staged
          ? "Staged bearer stored. Copy it now, then Save & Restart so both the current and staged bearers are accepted before switching the hosted client."
          : "Bearer stored. Copy it now: Wormhole will not return this value again. Save & Restart before connecting the hosted client.",
      });
      await queryClient.invalidateQueries({ queryKey: ["secrets"] });
    } catch (error) {
      setOneTimeBearer("");
      setOneTimeBearerEnv("");
      setCredentialMessage({ tone: "danger", text: errorMessage(error) });
    }
  };

  const generateInitialBearer = async () => {
    if (!current?.authTokenEnv?.trim() || !authSecret) {
      setCredentialMessage({ tone: "info", text: "Save the ingress definition first so Wormhole can register this write-only secret reference, then generate the bearer." });
      return;
    }
    await storeGeneratedBearer(current.authTokenEnv.trim(), false);
  };

  const stageBearerRotation = () => {
    if (!current || current.authTokenFallbackEnv?.trim()) return;
    updateIngress({ ...current, authTokenFallbackEnv: nextRemoteRotationEnv(selected, current.authTokenEnv) });
    setCredentialMessage({ tone: "info", text: "Rotation slot added to the draft. Save the configuration first; then generate the staged bearer without replacing the live credential." });
  };

  const generateStagedBearer = async () => {
    const envName = current?.authTokenFallbackEnv?.trim() ?? "";
    if (!envName || !fallbackAuthSecret) {
      setCredentialMessage({ tone: "info", text: "Save the rotation slot first so Wormhole can register its write-only secret reference." });
      return;
    }
    await storeGeneratedBearer(envName, true);
  };

  const finalizeBearerRotation = async () => {
    if (!current?.authTokenFallbackEnv?.trim() || !fallbackAuthSecret?.configured || !activeStatus?.fallbackAuthConfigured || !runtimeMatchesDraft) {
      setCredentialMessage({ tone: "info", text: "Restart into dual-token mode and switch the hosted client to the staged bearer before finalizing rotation." });
      return;
    }
    const oldEnv = current.authTokenEnv.trim();
    const newEnv = current.authTokenFallbackEnv.trim();
    try {
      if (authSecret?.managed) await api.deleteSecret(oldEnv);
      updateIngress({ ...current, authTokenEnv: newEnv, authTokenFallbackEnv: undefined });
      setOneTimeBearer("");
      setOneTimeBearerEnv("");
      setCredentialMessage({ tone: "success", text: "Staged bearer promoted in the draft. Save & Restart to retire the old credential from the listener. If the old value came from the process environment, remove it from that external source separately." });
      await queryClient.invalidateQueries({ queryKey: ["secrets"] });
    } catch (error) {
      setCredentialMessage({ tone: "danger", text: errorMessage(error) });
    }
  };

  const cancelBearerRotation = async () => {
    if (!current?.authTokenFallbackEnv?.trim()) return;
    try {
      if (fallbackAuthSecret?.managed) await api.deleteSecret(current.authTokenFallbackEnv.trim());
      updateIngress({ ...current, authTokenFallbackEnv: undefined });
      setOneTimeBearer("");
      setOneTimeBearerEnv("");
      setCredentialMessage({ tone: "info", text: "Rotation removed from the draft. Save & Restart if the running listener had already accepted the staged bearer." });
      await queryClient.invalidateQueries({ queryKey: ["secrets"] });
    } catch (error) {
      setCredentialMessage({ tone: "danger", text: errorMessage(error) });
    }
  };

  const copyText = async (label: string, text: string) => {
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      setCopyMessage(`${label} copied.`);
    } catch {
      setCopyMessage(`Could not copy ${label.toLowerCase()}; select the text manually.`);
    }
  };

  const primaryID = workspaces?.primary.id ?? "default";
  const namedWorkspaces = workspaces?.workspaces ?? [];
  const profileItems = profiles?.profiles ?? [];
  const secretMap = new Map((secrets?.secrets ?? []).map((secret) => [secret.name, secret]));
  const activeStatus = runtimeStatus?.ingresses.find((item) => item.name === selected);
  const activeWorkspaceID = current?.workspaceId?.trim() || primaryID;
  const activeProfileID = current?.toolProfile?.trim() || "remote-read";
  const configuredWorkspaceKnown = activeWorkspaceID === primaryID || namedWorkspaces.some((workspace) => workspace.id === activeWorkspaceID);
  const configuredProfileKnown = profileItems.some((profile) => profile.id === activeProfileID);
  const activeProfile = profileItems.find((profile) => profile.id === activeProfileID);
  const authSecret = current?.authTokenEnv ? secretMap.get(current.authTokenEnv) : undefined;
  const fallbackAuthSecret = current?.authTokenFallbackEnv ? secretMap.get(current.authTokenFallbackEnv) : undefined;
  const providerSecret = current?.providerTokenEnv ? secretMap.get(current.providerTokenEnv) : undefined;
  const provider = current?.provider ?? "external";
  const publicURL = current?.publicUrl?.trim() ?? "";
  const primaryBearerStored = !!authSecret?.configured || (!!oneTimeBearer && oneTimeBearerEnv === current?.authTokenEnv);
  const fallbackBearerStored = !!fallbackAuthSecret?.configured || (!!oneTimeBearer && oneTimeBearerEnv === current?.authTokenFallbackEnv);
  const bearerStored = primaryBearerStored || fallbackBearerStored;
  const rotationStaged = !!current?.authTokenFallbackEnv?.trim();
  const dualTokenRuntimeReady = rotationStaged && !!activeStatus?.primaryAuthReady && !!activeStatus?.fallbackAuthReady && !!activeStatus?.mcpReady;
  const providerReady = provider !== "cloudflare" || !!providerSecret?.configured;
  const runtimeMatchesDraft = !!activeStatus && !!current
    && current.enabled !== false
    && activeStatus.provider === provider
    && activeStatus.workspaceId === activeWorkspaceID
    && activeStatus.toolProfile === activeProfileID
    && activeStatus.localPort === current.localPort
    && (activeStatus.publicUrl ?? "") === publicURL
    && activeStatus.authTokenEnv === current.authTokenEnv
    && (activeStatus.authTokenFallbackEnv ?? "") === (current.authTokenFallbackEnv?.trim() ?? "");
  const localConnectionReady = !!activeStatus?.mcpReady && runtimeMatchesDraft && !activeProfile?.restartRequired;
  const connectionKitReady = current?.enabled !== false && !!publicURL && bearerStored && providerReady && localConnectionReady;

  return <Card title="Remote MCP ingress" description="Publish a fixed hosted-MCP contract without exposing Admin or workspace-switching endpoints.">
    <Notice tone="info">Each ingress binds only to <code>127.0.0.1</code>, serves only <code>/mcp</code>, and requires a dedicated bearer secret. <code>external</code> leaves HTTPS publishing to your proxy or tunnel; <code>cloudflare</code> lets Wormhole supervise cloudflared. The safe default profile is <code>remote-read</code>.</Notice>
    {runtimeStatus?.truncated && <Notice tone="warning">Only the first bounded set of active remote ingresses is included in live status.</Notice>}
    <div className="mcp-layout remote-ingress-layout">
      <aside className="mcp-list">
        <div className="inline-add">
          <TextInput value={newName} onChange={(event) => setNewName(event.target.value.toLowerCase())} onKeyDown={(event) => { if (event.key === "Enter") addIngress(); }} placeholder="notion" />
          <Button variant="secondary" onClick={addIngress} disabled={!validNewName}><Plus size={15} /></Button>
        </div>
        {names.map((name) => {
          const ingress = ingresses[name];
          const live = runtimeStatus?.ingresses.find((item) => item.name === name);
          const enabled = ingress.enabled !== false;
          return <button key={name} className={selected === name ? "active" : ""} onClick={() => setSelected(name)}>
            <span>{name}</span>
            <Badge tone={!enabled ? "neutral" : live?.mcpReady ? "success" : live ? "danger" : "warning"}>{!enabled ? "Off" : live?.mcpReady ? "Ready" : live ? "Issue" : "Restart"}</Badge>
          </button>;
        })}
        {!names.length && <p className="muted">No remote MCP ingress configured.</p>}
      </aside>

      <div className="mcp-editor">
        {current ? <>
          <div className="mcp-editor-head">
            <div><h3>{selected}</h3><p>Fixed workspace/profile boundary for one hosted MCP consumer.</p></div>
            <Button variant="danger" onClick={removeIngress}><Trash2 size={15} /> Remove</Button>
          </div>
          <div className="toggle-stack"><Toggle checked={current.enabled !== false} onChange={(enabled) => updateIngress({ ...current, enabled })} label="Ingress enabled" description="Disabled definitions remain in config but do not bind a listener or start a managed publisher." /></div>
          <div className="form-grid top-gap">
            <Field label="Provider" help="External means Wormhole owns only the loopback MCP listener. Cloudflare also starts a managed cloudflared child.">
              <Select value={provider} onChange={(event) => changeProvider(event.target.value as "external" | "cloudflare")}>
                {provider !== "external" && provider !== "cloudflare" && <option value={provider}>{provider} (configured)</option>}
                <option value="external">external</option>
                <option value="cloudflare">cloudflare</option>
              </Select>
            </Field>
            <Field label="Local port" hint="Must differ from the main MCP port and every other enabled ingress."><TextInput type="number" min={1} max={65535} value={current.localPort} onChange={(event) => updateIngress({ ...current, localPort: number(event.target.value) })} /></Field>
            <Field label="Workspace" hint="The ingress cannot switch workspace after connecting.">
              <Select value={current.workspaceId ?? ""} onChange={(event) => updateIngress({ ...current, workspaceId: event.target.value })}>
                <option value="">Primary · {primaryID}</option>
                {current.workspaceId === primaryID && <option value={primaryID}>Primary · {primaryID} (explicit)</option>}
                {!configuredWorkspaceKnown && current.workspaceId && <option value={current.workspaceId}>{current.workspaceId} (not registered)</option>}
                {namedWorkspaces.map((workspace) => <option key={workspace.id} value={workspace.id}>{workspace.id}{workspace.active ? "" : " · restart pending"}</option>)}
              </Select>
            </Field>
            <Field label="Tool profile" hint="Use remote-read unless this client genuinely requires a broader contract.">
              <Select value={activeProfileID} onChange={(event) => updateIngress({ ...current, toolProfile: event.target.value })}>
                {!configuredProfileKnown && <option value={activeProfileID}>{activeProfileID} (not available)</option>}
                {profileItems.map((profile) => <option key={profile.id} value={profile.id}>{profile.id}{profile.restartRequired ? " · restart pending" : ""}</option>)}
                {!profileItems.length && <><option value="remote-read">remote-read</option><option value="fast">fast</option><option value="full">full</option></>}
              </Select>
            </Field>
            <Field label="Public HTTPS URL" wide hint="Optional for server-to-server clients. Required to validate browser Origin headers; when set, the path must be exactly /mcp."><TextInput value={current.publicUrl ?? ""} onChange={(event) => updateIngress({ ...current, publicUrl: event.target.value })} placeholder="https://wormhole.example.com/mcp" /></Field>
            <Field label="MCP bearer environment" wide hint={authSecret?.configured ? `Configured via ${authSecret.source}.` : "Save this reference, then store its value on Secrets. Values are never shown here."}><TextInput value={current.authTokenEnv ?? ""} onChange={(event) => updateIngress({ ...current, authTokenEnv: event.target.value })} /></Field>
            {rotationStaged && <Field label="Staged rotation bearer environment" wide hint={fallbackAuthSecret?.configured ? `Configured via ${fallbackAuthSecret.source}. Keep this distinct from the primary bearer.` : "Save this fallback reference before generating the staged bearer."}><TextInput value={current.authTokenFallbackEnv ?? ""} onChange={(event) => updateIngress({ ...current, authTokenFallbackEnv: event.target.value })} /></Field>}
            {provider === "cloudflare" && <>
              <Field label="Cloudflare tunnel token environment" wide hint={providerSecret?.configured ? `Configured via ${providerSecret.source}.` : "Write-only provider credential used only by the managed cloudflared child."}><TextInput value={current.providerTokenEnv ?? ""} onChange={(event) => updateIngress({ ...current, providerTokenEnv: event.target.value })} /></Field>
              <Field label="cloudflared binary" wide><TextInput value={current.binary ?? ""} onChange={(event) => updateIngress({ ...current, binary: event.target.value })} placeholder="cloudflared" /></Field>
            </>}
          </div>

          <div className="upstream-control-panel remote-ingress-status-panel">
            <div className="upstream-control-head">
              <div><h4>Active runtime readiness</h4><p>Checks only the currently running loopback listener. External publisher availability remains owned by your proxy/tunnel provider.</p></div>
              <Button variant="secondary" onClick={onRefreshRuntimeStatus} loading={runtimeStatusLoading}><RefreshCw size={14} /> Refresh status</Button>
            </div>
            {!activeStatus ? <Notice tone={current.enabled === false ? "info" : "warning"}>{current.enabled === false ? "This ingress is disabled in the draft." : "No active listener with this name exists in the running daemon. Save and restart to reconcile it."}</Notice> : <>
              {!runtimeMatchesDraft && <Notice tone="warning">The running listener uses a different definition. Save and restart before relying on these edits.</Notice>}
              {activeProfile?.restartRequired && <Notice tone="warning">The selected profile contract has pending changes. Restart Wormhole before reconnecting or rescanning the hosted client.</Notice>}
              {activeStatus.issue && <Notice tone="warning">{activeStatus.issue}</Notice>}
              <div className="upstream-contract-grid remote-ingress-runtime-grid">
                <div><small>Local MCP</small><strong>{activeStatus.mcpReady ? "Ready" : activeStatus.listenerReachable ? "Unhealthy" : "Offline"}</strong><span>{activeStatus.listenerReachable ? `127.0.0.1:${activeStatus.localPort}` : "listener unavailable"}</span></div>
                <div><small>Bearer</small><strong>{activeStatus.authConfigured ? activeStatus.fallbackAuthConfigured ? "Dual" : "Present" : "Missing"}</strong><span>{activeStatus.fallbackAuthConfigured ? `${activeStatus.primaryAuthConfigured ? "primary + " : ""}fallback active` : activeStatus.primaryAuthConfigured ? "primary active" : "credential missing"}</span></div>
                <div><small>Contract</small><strong>{activeStatus.toolCount}</strong><span>{activeStatus.protocolVersion ? `${activeStatus.protocolVersion} · tools` : "not negotiated"}</span></div>
                <div><small>Publisher</small><strong>{activeStatus.provider === "external" ? "External" : activeStatus.providerTokenConfigured ? "Configured" : "Missing token"}</strong><span>{activeStatus.publicUrl || "public URL not recorded"}</span></div>
              </div>
            </>}
          </div>

          <div className="remote-credential-panel">
            <div className="upstream-control-head">
              <div><h4>Bearer credential handoff</h4><p>Initial setup stores one browser-generated 256-bit bearer. Later rotations use a second fallback slot so the old and new credentials overlap instead of breaking the hosted client.</p></div>
              {!primaryBearerStored
                ? <Button variant="secondary" onClick={() => void generateInitialBearer()} disabled={!current.authTokenEnv?.trim() || !authSecret}><KeyRound size={14} /> Generate bearer</Button>
                : !rotationStaged
                  ? <Button variant="secondary" onClick={stageBearerRotation}><KeyRound size={14} /> Start safe rotation</Button>
                  : null}
            </div>
            {credentialMessage && <Notice tone={credentialMessage.tone}>{credentialMessage.text}</Notice>}
            {rotationStaged && <div className="remote-rotation-state">
              <div className="remote-readiness-strip">
                <Badge tone={primaryBearerStored ? "success" : "warning"}>1 · Primary {primaryBearerStored ? "stored" : "missing"}</Badge>
                <Badge tone={fallbackAuthSecret ? "success" : "warning"}>2 · Slot {fallbackAuthSecret ? "saved" : "save config"}</Badge>
                <Badge tone={fallbackBearerStored ? "success" : "warning"}>3 · Staged {fallbackBearerStored ? "stored" : "missing"}</Badge>
                <Badge tone={dualTokenRuntimeReady && runtimeMatchesDraft ? "success" : "warning"}>4 · Runtime {dualTokenRuntimeReady && runtimeMatchesDraft ? "dual-token" : "restart needed"}</Badge>
              </div>
              <div className="button-row">
                <Button variant="secondary" onClick={() => void generateStagedBearer()} disabled={!fallbackAuthSecret}><KeyRound size={14} /> {fallbackBearerStored ? "Regenerate staged bearer" : "Generate staged bearer"}</Button>
                <Button onClick={() => void finalizeBearerRotation()} disabled={!fallbackBearerStored || !dualTokenRuntimeReady || !runtimeMatchesDraft}>Promote staged bearer</Button>
                <Button variant="danger" onClick={() => void cancelBearerRotation()}>Cancel rotation</Button>
              </div>
              <p className="muted">After step 4 becomes dual-token, switch the hosted client to the staged bearer and verify it works. Only then promote it; the final Save &amp; Restart removes the old bearer from the listener.</p>
            </div>}
            {oneTimeBearer && <div className="remote-one-time-secret">
              <div><small>One-time bearer value · {oneTimeBearerEnv}</small><code>{oneTimeBearer}</code><span>Visible only in this browser state. Persisted secret values remain unreadable from Wormhole.</span></div>
              <Button variant="secondary" onClick={() => void copyText("Bearer token", oneTimeBearer)}><Copy size={14} /> Copy token</Button>
            </div>}
          </div>

          <div className="remote-connection-kit">
            <div className="upstream-control-head">
              <div><h4>Hosted client connection kit</h4><p>Everything needed to connect this fixed contract to a Notion Custom Agent or another header-authenticated MCP client.</p></div>
              <a className="button secondary" href="https://www.notion.com/help/mcp-connections-for-custom-agents" target="_blank" rel="noreferrer"><ExternalLink size={14} /> Notion setup guide</a>
            </div>
            <div className="remote-readiness-strip">
              <Badge tone={runtimeMatchesDraft ? "success" : "warning"}>{runtimeMatchesDraft ? "Runtime current" : "Restart needed"}</Badge>
              <Badge tone={activeStatus?.mcpReady ? "success" : "warning"}>{activeStatus?.mcpReady ? "Local MCP ready" : "Local MCP pending"}</Badge>
              <Badge tone={publicURL ? "success" : "warning"}>{publicURL ? "Public URL set" : "Public URL missing"}</Badge>
              <Badge tone={bearerStored ? "success" : "warning"}>{bearerStored ? "Bearer stored" : "Bearer missing"}</Badge>
              {rotationStaged && <Badge tone={dualTokenRuntimeReady && runtimeMatchesDraft ? "success" : "warning"}>{dualTokenRuntimeReady && runtimeMatchesDraft ? "Safe rotation live" : "Rotation restart pending"}</Badge>}
              {provider === "cloudflare" && <Badge tone={providerReady ? "success" : "warning"}>{providerReady ? "Cloudflare token set" : "Cloudflare token missing"}</Badge>}
            </div>
            <div className="remote-connection-grid">
              <div><small>MCP URL</small><code>{publicURL || "https://your-host.example/mcp"}</code><Button variant="secondary" onClick={() => void copyText("MCP URL", publicURL)} disabled={!publicURL}><Copy size={13} /> Copy</Button></div>
              <div><small>Authentication header</small><code>Authorization</code><Button variant="secondary" onClick={() => void copyText("Header name", "Authorization")}><Copy size={13} /> Copy</Button></div>
              <div><small>Header value</small><code>{oneTimeBearer ? `Bearer ${oneTimeBearer}` : `Bearer <value of ${current.authTokenEnv || "AUTH_TOKEN"}>`}</code><Button variant="secondary" onClick={() => void copyText("Authorization header", oneTimeBearer ? `Bearer ${oneTimeBearer}` : "")} disabled={!oneTimeBearer}><Copy size={13} /> Copy exact value</Button></div>
              <div><small>Fixed contract</small><code>{activeWorkspaceID} · {activeProfileID}</code><span>{activeStatus?.mcpReady ? `${activeStatus.toolCount} tools · ${activeStatus.protocolVersion || "MCP"}` : "Restart and refresh status to inspect the active contract."}</span></div>
            </div>
            {copyMessage && <p className="remote-copy-message">{copyMessage}</p>}
            <ol className="remote-connection-steps">
              <li>In Notion workspace settings, enable custom MCP servers and approve this server URL if your workspace restricts connections.</li>
              <li>Open the Custom Agent → <strong>Settings</strong> → <strong>Tools &amp; Access</strong> → <strong>Add connection</strong> → <strong>Custom MCP server</strong>.</li>
              <li>Enter the public MCP URL and choose header-based authentication with <code>Authorization: Bearer …</code>.</li>
              <li>Connect, inspect the discovered tools, and keep read tools automatic only when appropriate. Reconnect or rescan after changing the active tool profile contract.</li>
            </ol>
            <Notice tone={connectionKitReady ? "success" : "info"}>{connectionKitReady ? "The Wormhole side is locally ready for hosted-client setup. Public Internet reachability is intentionally not probed from the Admin server; verify the HTTPS publisher separately before connecting Notion." : "Complete the highlighted local prerequisites, save/restart if required, and verify your external HTTPS publisher before connecting the hosted client."}</Notice>
          </div>

          <JSONField label="Complete ingress JSON" value={current as unknown as Record<string, unknown>} onChange={(next) => updateIngress(next as unknown as RemoteIngressConfig)} hint="Advanced escape hatch for this ingress. Server-side validation still enforces provider, port, workspace/profile, URL, and secret-reference rules." />
        </> : <div className="empty"><Globe2 size={30} /><strong>Add a remote MCP ingress</strong><p>Create one fixed, bearer-protected contract for Notion or another hosted MCP client.</p></div>}
      </div>
    </div>
  </Card>;
}

function validRemoteIngressName(value: string): boolean {
  return /^[a-z][a-z0-9_-]{0,31}$/.test(value.trim().toLowerCase());
}

function generateRemoteBearer(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  const encoded = btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
  return `whmcp_${encoded}`;
}

function derivedRemoteIngressEnv(name: string, suffix: "AUTH_TOKEN" | "AUTH_TOKEN_ROTATION_A" | "AUTH_TOKEN_ROTATION_B" | "TUNNEL_TOKEN"): string {
  return `WORMHOLE_REMOTE_${name.trim().toUpperCase().replace(/[-.]/g, "_")}_${suffix}`;
}

function nextRemoteRotationEnv(name: string, primaryEnv: string): string {
  const slotA = derivedRemoteIngressEnv(name, "AUTH_TOKEN_ROTATION_A");
  const slotB = derivedRemoteIngressEnv(name, "AUTH_TOKEN_ROTATION_B");
  return primaryEnv.trim() === slotA ? slotB : slotA;
}

function nextRemoteIngressPort(value: WormholeConfig): number {
  const used = new Set<number>([value.port, ...Object.values(value.remoteIngresses ?? {}).map((ingress) => ingress.localPort)]);
  for (let port = 18133; port <= 65535; port++) if (!used.has(port)) return port;
  for (let port = 1024; port < 18133; port++) if (!used.has(port)) return port;
  return 0;
}

function MemoryEditor({ value, onChange }: EditorProps) {
  const memory = value.memory;
  const setMemory = (patch: Partial<typeof memory>) => onChange({ ...value, memory: { ...memory, ...patch } });
  return <div className="stack">
    <Card title="Provider" description="Memory is fail-open unless Required is enabled.">
      <div className="toggle-stack"><Toggle checked={memory.enabled} onChange={(enabled) => setMemory({ enabled })} label="Enable memory" description="Capture selected project context for later retrieval." /></div>
      <div className="form-grid top-gap">
        <Field label="Provider"><TextInput value={memory.provider} onChange={(e) => setMemory({ provider: e.target.value })} placeholder="agentmemory" /></Field>
        <Field label="Endpoint"><TextInput value={memory.endpoint ?? ""} onChange={(e) => setMemory({ endpoint: e.target.value })} /></Field>
        <Field label="Secret environment"><TextInput value={memory.secretEnv ?? ""} onChange={(e) => setMemory({ secretEnv: e.target.value })} /></Field>
        <Field label="Agent ID"><TextInput value={memory.agentId ?? ""} onChange={(e) => setMemory({ agentId: e.target.value })} /></Field>
        <Field label="Capture mode"><Select value={memory.captureMode ?? "selected"} onChange={(e) => setMemory({ captureMode: e.target.value })}><option value="off">off</option><option value="metadata">metadata</option><option value="selected">selected</option></Select></Field>
        <Field label="Project strategy"><Select value={memory.projectStrategy ?? "git-origin"} onChange={(e) => setMemory({ projectStrategy: e.target.value })}><option value="git-origin">git-origin</option><option value="path-hash">path-hash</option></Select></Field>
        <div className="field-wide toggle-stack"><Toggle checked={!!memory.required} onChange={(required) => setMemory({ required })} label="Required for startup" description="Use only when memory availability must block the daemon." /></div>
      </div>
    </Card>
    <Card title="Budgets and delivery" description="Bounded queues and timeouts protect the coding path.">
      <div className="form-grid">
        <NumberField label="Request timeout (ms)" value={memory.timeoutMs} onChange={(timeoutMs) => setMemory({ timeoutMs })} />
        <NumberField label="Token budget" value={memory.tokenBudget} onChange={(tokenBudget) => setMemory({ tokenBudget })} />
        <NumberField label="Queue size" value={memory.queueSize} onChange={(queueSize) => setMemory({ queueSize })} />
        <NumberField label="Delivery workers" value={memory.deliveryWorkers} onChange={(deliveryWorkers) => setMemory({ deliveryWorkers })} />
        <NumberField label="Delivery timeout (ms)" value={memory.deliveryTimeoutMs} onChange={(deliveryTimeoutMs) => setMemory({ deliveryTimeoutMs })} />
        <NumberField label="Retry attempts" value={memory.retryMaxAttempts} onChange={(retryMaxAttempts) => setMemory({ retryMaxAttempts })} />
        <NumberField label="Retry backoff (ms)" value={memory.retryBackoffMs} onChange={(retryBackoffMs) => setMemory({ retryBackoffMs })} />
        <NumberField label="Health cache (ms)" value={memory.healthCacheMs} onChange={(healthCacheMs) => setMemory({ healthCacheMs })} />
      </div>
    </Card>
    <JSONField label="Provider options" value={memory.options ?? {}} onChange={(options) => setMemory({ options })} hint="Secret-like option keys are rejected by the Go validator." />
  </div>;
}

function MCPServersEditor({ value, onChange }: EditorProps) {
  const queryClient = useQueryClient();
  const upstream = useQuery({ queryKey: ["upstream"], queryFn: api.upstream, refetchInterval: 15_000 });
  const refresh = useMutation({
    mutationFn: ({ workspaceId, serverName }: { workspaceId: string; serverName: string }) => api.refreshUpstream(workspaceId, serverName),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["upstream"] }),
        queryClient.invalidateQueries({ queryKey: ["operations"] }),
      ]);
    },
  });
  const servers = value.mcpServers ?? {};
  const names = Object.keys(servers).sort();
  const [selected, setSelected] = useState(names[0] ?? "");
  const [newName, setNewName] = useState("");
  const current = selected ? servers[selected] : undefined;
  const activeStatuses = (upstream.data?.workspaces ?? []).flatMap((workspace) =>
    workspace.servers.filter((server) => server.name === selected).map((server) => ({ workspaceId: workspace.id, root: workspace.root, server })),
  );

  useEffect(() => {
    if (selected && !servers[selected]) setSelected(names[0] ?? "");
    if (!selected && names.length) setSelected(names[0]);
  }, [names.join("|"), selected, servers]);

  const updateServer = (server: MCPServerConfig) => onChange({ ...value, mcpServers: { ...servers, [selected]: server } });
  const add = () => {
    const name = newName.trim();
    if (!/^[a-z][a-z0-9_-]{0,23}$/.test(name) || servers[name]) return;
    onChange({ ...value, mcpServers: { ...servers, [name]: { enabled: true, transport: "stdio", startupMode: "lazy", policy: { default: "approval" } } } });
    setSelected(name); setNewName("");
  };
  const remove = () => {
    if (!selected) return;
    const next = { ...servers }; delete next[selected];
    onChange({ ...value, mcpServers: next }); setSelected(Object.keys(next).sort()[0] ?? "");
  };

  return <Card title="Upstream MCP servers" description="Each server is namespaced and validated before startup.">
    <div className="mcp-layout">
      <aside className="mcp-list">
        <div className="inline-add"><TextInput value={newName} onChange={(e) => setNewName(e.target.value)} placeholder="server-name" /><Button variant="secondary" onClick={add} disabled={!/^[a-z][a-z0-9_-]{0,23}$/.test(newName.trim()) || !!servers[newName.trim()]}><Plus size={15} /></Button></div>
        {names.map((name) => <button key={name} className={selected === name ? "active" : ""} onClick={() => setSelected(name)}><span>{name}</span><Badge tone={servers[name].enabled === false ? "neutral" : "success"}>{servers[name].enabled === false ? "Off" : "On"}</Badge></button>)}
        {!names.length && <p className="muted">No upstream MCP servers configured.</p>}
      </aside>
      <div className="mcp-editor">
        {current ? <>
          <div className="mcp-editor-head"><div><h3>{selected}</h3><p>Use structured fields for common settings or edit the complete server JSON below.</p></div><Button variant="danger" onClick={remove}><Trash2 size={15} /> Remove</Button></div>
          <div className="toggle-stack"><Toggle checked={current.enabled !== false} onChange={(enabled) => updateServer({ ...current, enabled })} label="Server enabled" /></div>
          <div className="form-grid top-gap">
            <Field label="Transport"><Select value={current.transport ?? (current.command ? "stdio" : "streamable-http")} onChange={(e) => updateServer({ ...current, transport: e.target.value })}><option value="stdio">stdio</option><option value="streamable-http">streamable-http</option></Select></Field>
            <Field label="Startup mode"><Select value={current.startupMode ?? "eager"} onChange={(e) => updateServer({ ...current, startupMode: e.target.value })}><option value="eager">eager</option><option value="background">background</option><option value="lazy">lazy</option></Select></Field>
            {(current.transport ?? "stdio") === "stdio" ? <>
              <Field label="Command" wide><TextInput value={current.command ?? ""} onChange={(e) => updateServer({ ...current, command: e.target.value })} /></Field>
              <ListField label="Arguments" value={current.args ?? []} onChange={(args) => updateServer({ ...current, args })} hint="One argument per line." />
              <Field label="Working directory"><TextInput value={current.cwd ?? ""} onChange={(e) => updateServer({ ...current, cwd: e.target.value })} /></Field>
            </> : <>
              <Field label="URL" wide><TextInput value={current.url ?? ""} onChange={(e) => updateServer({ ...current, url: e.target.value })} placeholder="http://127.0.0.1:…/mcp" /></Field>
              <div className="field-wide toggle-stack"><Toggle checked={!!current.allowRemote} onChange={(allowRemote) => updateServer({ ...current, allowRemote })} label="Allow remote endpoint" description="Loopback endpoints are enforced unless explicitly enabled." /></div>
            </>}
            <ListField label="Workspace IDs" value={current.workspaceIds ?? []} onChange={(workspaceIds) => updateServer({ ...current, workspaceIds })} hint="Empty or * applies to every workspace." />
            <ListField label="Allowed tools" value={current.allowedTools ?? []} onChange={(allowedTools) => updateServer({ ...current, allowedTools })} />
            <ListField label="Denied tools" value={current.deniedTools ?? []} onChange={(deniedTools) => updateServer({ ...current, deniedTools })} />
            <NumberField label="Max concurrency" value={current.maxConcurrency} onChange={(maxConcurrency) => updateServer({ ...current, maxConcurrency })} />
            <NumberField label="Max tools" value={current.maxTools} onChange={(maxTools) => updateServer({ ...current, maxTools })} />
          </div>
          <JSONField label="Complete server JSON" value={current as Record<string, unknown>} onChange={(next) => updateServer(next as MCPServerConfig)} hint="Covers envRefs, headerRefs, policy, timeouts and advanced transport settings." />
          <div className="upstream-control-panel">
            <div className="upstream-control-head"><div><h4>Active catalog status</h4><p>Refresh opens a new upstream session, updates the secret-free cache, and keeps the downstream MCP contract unchanged until restart.</p></div><Button variant="secondary" onClick={() => void upstream.refetch()} loading={upstream.isFetching}><RefreshCw size={14} /> Reload status</Button></div>
            {!!upstream.error && <Notice tone="danger">{errorMessage(upstream.error)}</Notice>}
            {!!refresh.error && <Notice tone="danger">{errorMessage(refresh.error)}</Notice>}
            {!activeStatuses.length ? <Notice tone="info">This server is not active in the running daemon. Save and restart after validating its configuration.</Notice> : <div className="upstream-status-list">{activeStatuses.map((item) => <UpstreamStatusCard key={`${item.workspaceId}:${selected}`} workspaceId={item.workspaceId} root={item.root} status={item.server} refreshing={refresh.isPending && refresh.variables?.workspaceId === item.workspaceId && refresh.variables?.serverName === selected} onRefresh={() => refresh.mutate({ workspaceId: item.workspaceId, serverName: selected })} />)}</div>}
          </div>
        </> : <div className="empty"><Waypoints size={30} /><strong>Select or add an MCP server</strong><p>The raw JSON editor covers every supported field.</p></div>}
      </div>
    </div>
  </Card>;
}

function UpstreamStatusCard({ workspaceId, root, status, refreshing, onRefresh }: { workspaceId: string; root: string; status: UpstreamMCPStatus; refreshing: boolean; onRefresh: () => void }) {
  const diff = status.activeToDesired;
  return <div className="upstream-status-card">
    <div className="upstream-status-title"><div><strong>{workspaceId}</strong><small>{root}</small></div><div><Badge tone={status.health?.available === true ? "success" : "danger"}>{status.health?.available === true ? "healthy" : "unavailable"}</Badge>{status.restartRequired && <Badge tone="warning">restart required</Badge>}</div></div>
    <div className="upstream-contract-grid">
      <div><small>Active</small><strong>{status.activeContract?.toolCount ?? 0}</strong><code>{status.activeContract?.hash || "—"}</code></div>
      <div><small>Cached</small><strong>{status.cachedContract?.toolCount ?? 0}</strong><code>{status.cachedContract?.hash || "—"}</code></div>
      <div><small>Live</small><strong>{status.liveContract?.toolCount ?? 0}</strong><code>{status.liveContract?.hash || "—"}</code></div>
      <div><small>Contract changes</small><strong>{diff?.changedCount ?? 0}</strong><span>{[...(diff?.added ?? []).map((name) => `+${name}`), ...(diff?.removed ?? []).map((name) => `-${name}`), ...(diff?.changed ?? []).map((name) => `~${name}`)].slice(0, 4).join(", ") || "No changes"}</span></div>
    </div>
    {(status.error || status.cachedError || status.liveError) && <Notice tone="warning">{status.error || status.liveError || status.cachedError}</Notice>}
    <div className="upstream-status-actions"><span className="muted">{status.transport ?? "transport unknown"} · {status.startupMode ?? "startup unknown"}</span><Button variant="secondary" onClick={onRefresh} loading={refreshing} disabled={!status.refreshAvailable}><RefreshCw size={14} /> Refresh catalog</Button></div>
  </div>;
}

function ToolsEditor({ value, onChange, catalog, catalogLoading }: EditorProps & { catalog?: ToolCatalogResponse; catalogLoading: boolean }) {
  const tools = value.tools ?? {};
  const set = <K extends keyof WormholeConfig>(key: K, next: WormholeConfig[K]) => onChange({ ...value, [key]: next });
  const setTools = (patch: Partial<typeof tools>) => onChange({ ...value, tools: { ...tools, ...patch } });
  const groupOptions = (catalog?.groups ?? []).map((group) => ({
    value: group.name,
    label: group.name,
    description: `${group.toolCount} tool${group.toolCount === 1 ? "" : "s"}`,
  }));
  const toolOptions = (catalog?.tools ?? []).map((tool) => ({
    value: tool.name,
    label: tool.name,
    description: `${tool.title} · ${tool.groups.join(", ")}`,
  }));
  const loadingHint = catalogLoading ? "Loading available values…" : `${toolOptions.length} tools from ${groupOptions.length} groups`;
  return <div className="stack">
    <Card title="Tool exposure" description="Deny lists and allow lists are enforced before MCP tools are exposed.">
      <div className="form-grid">
        <Field label="Allowed groups" hint={loadingHint} wide><MultiSelect options={groupOptions} value={tools.allowedGroups ?? []} onChange={(allowedGroups) => setTools({ allowedGroups })} placeholder="All groups are allowed" searchPlaceholder="Search tool groups…" /></Field>
        <Field label="Allowed tools" hint="When selected, tools not listed here are hidden." wide><MultiSelect options={toolOptions} value={tools.allowedTools ?? []} onChange={(allowedTools) => setTools({ allowedTools })} placeholder="No explicit tool allowlist" searchPlaceholder="Search available tools…" /></Field>
        <Field label="Denied tools" hint="Denied tools override allowed groups and allowed tools." wide><MultiSelect options={toolOptions} value={tools.deniedTools ?? []} onChange={(deniedTools) => setTools({ deniedTools })} placeholder="No denied tools" searchPlaceholder="Search available tools…" /></Field>
      </div>
    </Card>
    <Card title="Resource limits" description="Keep output and process limits bounded to protect the local daemon.">
      <div className="form-grid">
        <NumberField label="Max read chars" value={value.maxReadChars} onChange={(v) => set("maxReadChars", v)} />
        <NumberField label="Default read chars" value={value.readDefault} onChange={(v) => set("readDefault", v)} />
        <NumberField label="Max batch read chars" value={value.maxBatchReadChars} onChange={(v) => set("maxBatchReadChars", v)} />
        <NumberField label="Max command output" value={value.maxCommandOutput} onChange={(v) => set("maxCommandOutput", v)} />
        <NumberField label="Default command output" value={value.commandOutputDefault} onChange={(v) => set("commandOutputDefault", v)} />
        <NumberField label="Max HTTP body bytes" value={value.maxBodyBytes} onChange={(v) => set("maxBodyBytes", v)} />
        <NumberField label="Max managed processes" value={value.maxProcesses} onChange={(v) => set("maxProcesses", v)} />
        <NumberField label="Max concurrent tool calls" value={value.maxConcurrentToolCalls} onChange={(v) => set("maxConcurrentToolCalls", v)} />
        <NumberField label="Git status cache (ms)" value={value.gitStatusCacheMs} onChange={(v) => set("gitStatusCacheMs", v)} />
      </div>
    </Card>
  </div>;
}

function AdvancedEditor({ value, onChange }: EditorProps) {
  const [raw, setRaw] = useState(() => JSON.stringify(value, null, 2));
  const [error, setError] = useState("");
  useEffect(() => setRaw(JSON.stringify(value, null, 2)), [value]);
  const apply = () => {
    try { onChange(JSON.parse(raw) as WormholeConfig); setError(""); }
    catch (err) { setError(err instanceof Error ? err.message : String(err)); }
  };
  return <Card title="Complete JSON document" titleHelp={CONFIG_HELP["Complete JSON document"]} description="Use this fallback for exact control over every supported field." actions={<Button variant="secondary" onClick={apply}><Cpu size={15} /> Apply to form</Button>}>
    {error && <Notice tone="danger">{error}</Notice>}
    <TextArea className="control textarea code-editor" value={raw} onChange={(e) => setRaw(e.target.value)} spellCheck={false} />
  </Card>;
}

function JSONField({ label, value, onChange, hint }: { label: string; value: Record<string, unknown>; onChange: (value: Record<string, unknown>) => void; hint?: string }) {
  const [raw, setRaw] = useState(() => JSON.stringify(value, null, 2));
  const [error, setError] = useState("");
  useEffect(() => setRaw(JSON.stringify(value, null, 2)), [value]);
  return <Card title={label} titleHelp={CONFIG_HELP[label]} description={hint} actions={<Button variant="secondary" onClick={() => { try { onChange(JSON.parse(raw)); setError(""); } catch (err) { setError(err instanceof Error ? err.message : String(err)); } }}><Save size={15} /> Apply JSON</Button>}>
    {error && <Notice tone="danger">{error}</Notice>}
    <TextArea className="control textarea code-editor compact" value={raw} onChange={(e) => setRaw(e.target.value)} spellCheck={false} />
  </Card>;
}

function ListField({ label, value, onChange, hint }: { label: string; value: string[]; onChange: (value: string[]) => void; hint?: string }) {
  return <Field label={label} hint={hint} wide><TextArea value={value.join("\n")} onChange={(e) => onChange(lines(e.target.value))} rows={4} /></Field>;
}

function NumberField({ label, value, onChange }: { label: string; value?: number; onChange: (value: number) => void }) {
  return <Field label={label}><TextInput type="number" min={1} value={value ?? 0} onChange={(e) => onChange(number(e.target.value))} /></Field>;
}

interface EditorProps { value: WormholeConfig; onChange: (value: WormholeConfig) => void }
const lines = (value: string) => value.split("\n").map((entry) => entry.trim()).filter(Boolean);
const number = (value: string) => Number.parseInt(value || "0", 10);
async function waitForDaemon(initialDelayMs: number) {
  await new Promise((resolve) => window.setTimeout(resolve, Math.max(1_000, initialDelayMs)));
  let observedDowntime = false;
  for (let attempt = 0; attempt < 60; attempt++) {
    try {
      const response = await fetch("/admin/api/v1/auth/status", { credentials: "same-origin", cache: "no-store" });
      if (!response.ok) {
        observedDowntime = true;
      } else {
        const status = await response.json().catch(() => ({}));
        // Browser sessions are process-local. A new daemon reports the account
        // as configured but this old cookie as unauthenticated, even when the
        // effective ConfigID is unchanged.
        if (observedDowntime || status.authenticated === false) {
          window.location.reload();
          return;
        }
      }
    } catch {
      observedDowntime = true;
    }
    await new Promise((resolve) => window.setTimeout(resolve, 1_000));
  }
}

const errorMessage = (error: unknown) => error instanceof APIError ? error.message : error instanceof Error ? error.message : String(error);
