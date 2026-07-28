import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Braces, ChevronLeft, ExternalLink, FolderGit2, FolderOpen, Plus, Save, Trash2, X } from "lucide-react";
import { api, APIError } from "./api";
import { Badge, Button, Card, EmptyState, Field, LoadingPage, Notice, PageHeader, TextArea, TextInput } from "./components";

export function Workspaces() {
  const queryClient = useQueryClient();
  const workspaces = useQuery({ queryKey: ["workspaces"], queryFn: api.workspaces });
  const [selected, setSelected] = useState("");
  const [showAdd, setShowAdd] = useState(false);
  const [candidateId, setCandidateId] = useState("");
  const [candidatePath, setCandidatePath] = useState("");
  const [idTouched, setIdTouched] = useState(false);
  const [browsePath, setBrowsePath] = useState("");
  const [browseInput, setBrowseInput] = useState("");
  const [showHidden, setShowHidden] = useState(false);
  const [message, setMessage] = useState<{ tone: "success" | "danger" | "warning"; text: string } | null>(null);

  const items = workspaces.data?.workspaces ?? [];
  const detail = useQuery({ queryKey: ["workspace-config", selected], queryFn: () => api.workspaceConfig(selected), enabled: !!selected });
  const browser = useQuery({
    queryKey: ["workspace-browser", browsePath, showHidden],
    queryFn: () => api.browseWorkspaces(browsePath, showHidden),
    enabled: showAdd,
  });

  useEffect(() => {
    if (!items.length) {
      if (selected) setSelected("");
      return;
    }
    if (!selected || !items.some((item) => item.id === selected)) setSelected(items[0].id);
  }, [items, selected]);

  useEffect(() => {
    if (!browser.data) return;
    setBrowseInput(browser.data.path);
    if (!browsePath) setBrowsePath(browser.data.path);
  }, [browser.data, browsePath]);

  const openAdd = () => {
    setShowAdd(true);
    setCandidateId("");
    setCandidatePath("");
    setIdTouched(false);
    setBrowsePath("");
    setBrowseInput("");
    setMessage(null);
  };

  const create = useMutation({
    mutationFn: () => api.createWorkspace(candidateId.trim(), candidatePath.trim(), workspaces.data?.revision ?? ""),
    onSuccess: async (data) => {
      await queryClient.invalidateQueries({ queryKey: ["workspaces"] });
      const id = data.workspace?.id;
      if (id) setSelected(id);
      setShowAdd(false);
      setMessage({ tone: "success", text: data.message ?? "Workspace registered. Restart Codebridge to activate it." });
    },
    onError: (error) => setMessage({ tone: "danger", text: errorMessage(error) }),
  });

  const useCurrentDirectory = () => {
    if (!browser.data) return;
    setCandidatePath(browser.data.selected.path);
    if (!idTouched) setCandidateId(browser.data.selected.suggestedId);
  };

  if (workspaces.isLoading) return <LoadingPage />;

  return <>
    <PageHeader
      eyebrow="Named runtimes"
      title="Workspace management"
      description="Register local repositories, edit isolated overrides, and safely remove named workspace registrations."
      actions={<Button onClick={showAdd ? () => setShowAdd(false) : openAdd}>{showAdd ? <X size={15} /> : <Plus size={15} />}{showAdd ? "Cancel" : "Add workspace"}</Button>}
    />
    {message && <Notice tone={message.tone}>{message.text}</Notice>}
    <Notice tone="info">Registry changes are saved immediately, but MCP endpoints are reconciled only after restarting Codebridge. Removing a workspace never deletes its runtime state.</Notice>

    {showAdd && <Card title="Register a workspace" description="Browse within your home directory or type any existing absolute directory path.">
      <div className="workspace-add-grid">
        <div className="stack">
          <div className="form-grid">
            <Field label="Workspace ID" hint="Lowercase letters, digits, hyphens, and underscores; maximum 32 characters.">
              <TextInput value={candidateId} placeholder="loyalty-api" onChange={(event) => { setCandidateId(event.target.value); setIdTouched(true); }} />
            </Field>
            <Field label="Workspace path" hint="The backend resolves symlinks and verifies that this directory exists.">
              <TextInput value={candidatePath} placeholder="/home/user/projects/api" onChange={(event) => setCandidatePath(event.target.value)} />
            </Field>
          </div>
          <Notice tone="info">The directory browser is restricted to your home directory and returns folders only. You can still paste an existing path outside home into the workspace path field.</Notice>
          <div className="button-row">
            <Button onClick={() => create.mutate()} loading={create.isPending} disabled={!candidatePath.trim() || !candidateId.trim() || !workspaces.data?.revision}><Plus size={15} /> Register workspace</Button>
            {candidatePath && <span className="muted">Selected: {candidatePath}</span>}
          </div>
        </div>
        <div className="directory-browser">
          <div className="browser-toolbar">
            <Button variant="secondary" onClick={() => browser.data?.parent && setBrowsePath(browser.data.parent)} disabled={!browser.data?.parent}><ChevronLeft size={15} /> Up</Button>
            <TextInput value={browseInput} onChange={(event) => setBrowseInput(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") setBrowsePath(browseInput); }} />
            <Button variant="secondary" onClick={() => setBrowsePath(browseInput)}><FolderOpen size={15} /> Open</Button>
          </div>
          <div className="browser-options">
            <label><input type="checkbox" checked={showHidden} onChange={(event) => setShowHidden(event.target.checked)} /> Show hidden directories</label>
            <Button variant="secondary" onClick={useCurrentDirectory} disabled={!browser.data}><FolderGit2 size={15} /> Use current folder</Button>
          </div>
          {browser.isLoading && <LoadingPage />}
          {browser.error && <Notice tone="danger">{errorMessage(browser.error)}</Notice>}
          {browser.data && <>
            <div className="browser-current"><FolderGit2 size={17} /><span><strong>{browser.data.selected.suggestedId}</strong><small>{browser.data.path}</small></span>{browser.data.selected.git && <Badge tone="success">Git</Badge>}</div>
            <div className="directory-list">
              {browser.data.directories.map((directory) => <button key={directory.path} onClick={() => setBrowsePath(directory.path)}><FolderOpen size={16} /><span><strong>{directory.name}</strong><small>{directory.path}</small></span>{directory.git && <Badge tone="success">Git</Badge>}</button>)}
              {!browser.data.directories.length && <EmptyState title="No subdirectories" description="Select the current folder or navigate to another path." />}
            </div>
            {browser.data.truncated && <p className="muted">Showing the first {browser.data.limit} directories.</p>}
          </>}
        </div>
      </div>
    </Card>}

    <Card title="Primary workspace" description="The primary root is owned by the global configuration and cannot be removed here.">
      <div className="workspace-primary"><FolderGit2 size={21} /><div><strong>{workspaces.data?.primary.id}</strong><span>{workspaces.data?.primary.workspace}</span></div><Badge tone="success">Active</Badge></div>
    </Card>
    <div className="workspace-layout">
      <Card title="Registered workspaces" description={`${items.length} named workspace${items.length === 1 ? "" : "s"}.`}>
        <div className="workspace-list">
          {items.map((item) => <button key={item.id} className={selected === item.id ? "active" : ""} onClick={() => setSelected(item.id)}><span><strong>{item.id}</strong><small>{item.workspace}</small></span><span className="workspace-badges"><Badge tone={item.enabled ? "info" : "neutral"}>{item.enabled ? "Enabled" : "Disabled"}</Badge><Badge tone={item.active ? "success" : "warning"}>{item.active ? "Active" : "Restart"}</Badge></span></button>)}
          {!items.length && <EmptyState title="No named workspaces" description="Use Add workspace to register a local repository." />}
        </div>
      </Card>
      <div>{selected && <WorkspaceEditor id={selected} query={detail} registryRevision={workspaces.data?.revision ?? ""} onRemoved={(text) => { setSelected(""); setMessage({ tone: "success", text }); }} />}</div>
    </div>
  </>;
}

function WorkspaceEditor({ id, query, registryRevision, onRemoved }: {
  id: string;
  query: ReturnType<typeof useQuery<any, Error>>;
  registryRevision: string;
  onRemoved: (message: string) => void;
}) {
  const queryClient = useQueryClient();
  const [raw, setRaw] = useState("{}");
  const [dirty, setDirty] = useState(false);
  const [deleteConfig, setDeleteConfig] = useState(false);
  const [message, setMessage] = useState<{ tone: "success" | "danger"; text: string } | null>(null);

  useEffect(() => {
    if (query.data && !dirty) setRaw(JSON.stringify(query.data.override, null, 2));
  }, [query.data, dirty, id]);
  useEffect(() => { setDirty(false); setDeleteConfig(false); setMessage(null); }, [id]);

  const save = useMutation({
    mutationFn: async () => api.saveWorkspaceConfig(id, JSON.parse(raw), query.data.revision),
    onSuccess: (data) => {
      queryClient.setQueryData(["workspace-config", id], data);
      setRaw(JSON.stringify(data.override, null, 2)); setDirty(false);
      setMessage({ tone: "success", text: "Override saved. Restart Codebridge to activate it." });
    },
    onError: (error) => setMessage({ tone: "danger", text: errorMessage(error) }),
  });

  const remove = useMutation({
    mutationFn: () => api.removeWorkspace(id, registryRevision, deleteConfig),
    onSuccess: async (data) => {
      queryClient.removeQueries({ queryKey: ["workspace-config", id] });
      await queryClient.invalidateQueries({ queryKey: ["workspaces"] });
      onRemoved(data.activeUntilRestart
        ? `Workspace ${id} was removed from the registry. Restart Codebridge to unload its active endpoint.`
        : `Workspace ${id} was removed from the registry.`);
    },
    onError: (error) => setMessage({ tone: "danger", text: errorMessage(error) }),
  });

  const confirmRemove = () => {
    const detail = deleteConfig
      ? "Its override config file will also be deleted. Runtime state is preserved."
      : "Its override config and runtime state will be preserved for later re-registration.";
    if (window.confirm(`Remove workspace ${id}?\n\n${detail}`)) remove.mutate();
  };

  if (query.isLoading) return <LoadingPage />;
  if (!query.data) return <EmptyState title="Unable to load workspace" description={query.error?.message ?? "Select another workspace."} />;

  return <div className="stack">
    {message && <Notice tone={message.tone}>{message.text}</Notice>}
    <Card title={`${id} override`} description="Objects merge recursively; arrays replace; null removes an inherited key." actions={<><Badge tone={dirty ? "warning" : "success"}>{dirty ? "Unsaved" : "In sync"}</Badge><Button onClick={() => save.mutate()} loading={save.isPending} disabled={!dirty}><Save size={15} /> Save</Button></>}>
      <TextArea className="control textarea code-editor compact" value={raw} onChange={(event) => { setRaw(event.target.value); setDirty(true); setMessage(null); }} spellCheck={false} />
    </Card>
    <Card title="Effective configuration" description="Global config plus this override and registry-owned fields.">
      <details className="json-details"><summary><Braces size={16} /> Inspect effective JSON</summary><pre>{JSON.stringify(query.data.effective, null, 2)}</pre></details>
      <dl className="definition-grid top-gap"><div><dt>Root</dt><dd>{query.data.registration.workspace}</dd></div><div><dt>Config file</dt><dd>{query.data.registration.configPath}</dd></div><div><dt>Data directory</dt><dd>{query.data.registration.dataDir}</dd></div><div><dt>Endpoint</dt><dd><code>/mcp/workspaces/{id}</code> <ExternalLink size={13} /></dd></div></dl>
    </Card>
    <Card title="Remove workspace" description="Unregister this named runtime without deleting repository files or runtime state.">
      <label className="danger-option"><input type="checkbox" checked={deleteConfig} onChange={(event) => setDeleteConfig(event.target.checked)} /><span><strong>Also delete the workspace override file</strong><small>Notes, tasks, approvals, audit, backups, and other runtime state remain preserved.</small></span></label>
      <div className="button-row"><Button variant="danger" onClick={confirmRemove} loading={remove.isPending} disabled={!registryRevision}><Trash2 size={15} /> Remove {id}</Button></div>
    </Card>
  </div>;
}

function errorMessage(error: unknown): string {
  return error instanceof APIError ? error.message : error instanceof Error ? error.message : String(error);
}
