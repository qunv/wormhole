import { useEffect, useMemo, useState, type ComponentProps } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Braces, Cpu, Database, Gauge, Network, Plus, Save, Shield, Trash2, Waypoints } from "lucide-react";
import { api, APIError } from "./api";
import { Badge, Button, Card, Field as BaseField, LoadingPage, Notice, PageHeader, Select, TextArea, TextInput, Toggle as BaseToggle } from "./components";
import type { CodebridgeConfig, MCPServerConfig } from "./types";

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
  "Port": "TCP port used by the local Codebridge HTTP and MCP server.",
  "Extra roots": "Additional directories that tools may access besides the primary workspace. Each path must pass root-confinement checks.",
  "Allowed browser origins": "Exact browser origins permitted to call the local server. Use full origins such as http://127.0.0.1:3000.",
  "Disable tunnel": "Prevents Codebridge from creating the external Secure MCP Tunnel and keeps the service local only.",
  "Tunnel ID": "Identifier of the Secure MCP Tunnel that exposes this local Codebridge daemon.",
  "Organization ID": "Organization that owns or authorizes the configured tunnel.",
  "Tunnel binary": "Optional custom path or command name for the tunnel executable.",
  "Profile": "Named tunnel profile used to resolve tunnel credentials and settings.",
  "Profile directory": "Directory containing tunnel profile files when they are not stored in the default location.",
  "Runtime key environment": "Environment variable name containing the runtime API key. The secret value is managed on the Secrets page.",
  "Audit tool calls": "Records bounded local audit events for tool calls, outcomes, and approvals without storing full results.",
  "Include redacted argument metadata": "Adds reduced and redacted argument metadata to audit records for better diagnostics.",
  "HTTP access log": "Logs Admin and MCP HTTP requests locally. Useful for troubleshooting but can add noise.",
  "Enable memory": "Allows Codebridge to capture selected project context and retrieve it in later agent sessions.",
  "Provider": "Memory backend adapter to use, for example agentmemory.",
  "Endpoint": "Base URL of the memory provider service.",
  "Secret environment": "Environment variable name containing the memory provider credential; the value is never stored here.",
  "Agent ID": "Stable identity sent to the memory provider to separate observations from different agents.",
  "Capture mode": "Controls automatic memory capture: off disables capture, metadata stores reduced metadata, and selected stores approved contextual observations.",
  "Project strategy": "Defines how projects receive stable memory identities. git-origin follows the repository remote; path-hash derives identity from the local path.",
  "Required for startup": "When enabled, Codebridge startup fails if the memory provider is unavailable instead of continuing without memory.",
  "Request timeout (ms)": "Maximum time allowed for a memory provider request before it is cancelled.",
  "Token budget": "Maximum amount of retrieved memory context that may be inserted into an agent prompt.",
  "Queue size": "Maximum number of memory observations waiting for asynchronous delivery.",
  "Delivery workers": "Number of concurrent workers that send queued memory observations.",
  "Delivery timeout (ms)": "Maximum time allowed for one queued memory delivery attempt.",
  "Retry attempts": "Maximum number of delivery attempts before an observation is dropped or reported as failed.",
  "Retry backoff (ms)": "Delay between memory delivery retries.",
  "Health cache (ms)": "How long a memory provider health result is reused before checking again.",
  "Provider options": "Provider-specific non-secret settings passed to the selected memory adapter.",
  "Server enabled": "Controls whether this upstream MCP server is available to Codebridge workspaces.",
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
  "Max managed processes": "Maximum number of background processes Codebridge may manage at the same time.",
  "Git status cache (ms)": "How long a git status result is reused before Codebridge executes git status again.",
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
  const [tab, setTab] = useState<Tab>("general");
  const [draft, setDraft] = useState<CodebridgeConfig | null>(null);
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
      setMessage({ tone: "success", text: "Saved safely. Restart Codebridge to activate the new configuration." });
      void queryClient.invalidateQueries({ queryKey: ["secrets"] });
      void queryClient.invalidateQueries({ queryKey: ["workspaces"] });
    },
    onError: (error) => setMessage({ tone: "danger", text: errorMessage(error) }),
  });

  if (snapshot.isLoading || !draft || !snapshot.data) return <LoadingPage />;

  const update = (next: CodebridgeConfig) => {
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
        actions={<><Badge tone={dirty ? "warning" : "success"}>{dirty ? "Unsaved changes" : "In sync"}</Badge><Button variant="secondary" onClick={() => validate.mutate()} loading={validate.isPending}>Validate</Button><Button onClick={() => save.mutate()} loading={save.isPending} disabled={!dirty}><Save size={15} /> Save</Button></>}
      />
      {message && <Notice tone={message.tone}>{message.text}</Notice>}
      <Notice tone="info">Secrets are intentionally excluded from this document. Configure referenced environment variables from the <strong>Secrets</strong> page.</Notice>
      <div className="config-layout">
        <nav className="subnav" aria-label="Configuration sections">
          {tabs.map((item) => <button key={item.id} className={tab === item.id ? "active" : ""} onClick={() => setTab(item.id)}>{item.icon}<span>{item.label}</span></button>)}
        </nav>
        <div className="config-content">
          {tab === "general" && <GeneralEditor value={draft} onChange={update} />}
          {tab === "memory" && <MemoryEditor value={draft} onChange={update} />}
          {tab === "mcp" && <MCPServersEditor value={draft} onChange={update} />}
          {tab === "tools" && <ToolsEditor value={draft} onChange={update} />}
          {tab === "advanced" && <AdvancedEditor value={draft} onChange={update} />}
        </div>
      </div>
    </>
  );
}

function GeneralEditor({ value, onChange }: EditorProps) {
  const set = <K extends keyof CodebridgeConfig>(key: K, next: CodebridgeConfig[K]) => onChange({ ...value, [key]: next });
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
    </Card>
    <Card title="Observability" description="Audit records are local, bounded and redacted by the runtime.">
      <div className="toggle-stack">
        <Toggle checked={value.audit} onChange={(next) => set("audit", next)} label="Audit tool calls" description="Recommended for traceability." />
        <Toggle checked={value.auditArgs} onChange={(next) => set("auditArgs", next)} label="Include redacted argument metadata" />
        <Toggle checked={!!value.httpLog} onChange={(next) => set("httpLog", next)} label="HTTP access log" description="Useful for debugging; disabled by default to reduce noise." />
      </div>
    </Card>
  </div>;
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
  const servers = value.mcpServers ?? {};
  const names = Object.keys(servers).sort();
  const [selected, setSelected] = useState(names[0] ?? "");
  const [newName, setNewName] = useState("");
  const current = selected ? servers[selected] : undefined;

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
        </> : <div className="empty"><Waypoints size={30} /><strong>Select or add an MCP server</strong><p>The raw JSON editor covers every supported field.</p></div>}
      </div>
    </div>
  </Card>;
}

function ToolsEditor({ value, onChange }: EditorProps) {
  const tools = value.tools ?? {};
  const set = <K extends keyof CodebridgeConfig>(key: K, next: CodebridgeConfig[K]) => onChange({ ...value, [key]: next });
  const setTools = (patch: Partial<typeof tools>) => onChange({ ...value, tools: { ...tools, ...patch } });
  return <div className="stack">
    <Card title="Tool exposure" description="Deny lists and allow lists are enforced before MCP tools are exposed.">
      <div className="form-grid">
        <ListField label="Allowed groups" value={tools.allowedGroups ?? []} onChange={(allowedGroups) => setTools({ allowedGroups })} />
        <ListField label="Allowed tools" value={tools.allowedTools ?? []} onChange={(allowedTools) => setTools({ allowedTools })} />
        <ListField label="Denied tools" value={tools.deniedTools ?? []} onChange={(deniedTools) => setTools({ deniedTools })} />
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
    try { onChange(JSON.parse(raw) as CodebridgeConfig); setError(""); }
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

interface EditorProps { value: CodebridgeConfig; onChange: (value: CodebridgeConfig) => void }
const lines = (value: string) => value.split("\n").map((entry) => entry.trim()).filter(Boolean);
const number = (value: string) => Number.parseInt(value || "0", 10);
const errorMessage = (error: unknown) => error instanceof APIError ? error.message : error instanceof Error ? error.message : String(error);
