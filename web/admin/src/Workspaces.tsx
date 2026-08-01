import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  Braces,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  ExternalLink,
  Eye,
  FolderGit2,
  FolderOpen,
  Plus,
  Save,
  Search,
  Sparkles,
  Trash2,
  X,
} from "lucide-react";
import { api, APIError } from "./api";
import { Badge, Button, Card, EmptyState, Field, LoadingPage, Notice, PageHeader, TextArea, TextInput } from "./components";
const PRIMARY_WORKSPACE = "$primary";

function detailFromHash(): { id: string; primary: boolean } {
  const [page, encodedDetail] = window.location.hash.slice(1).split("/", 2);
  if (page !== "workspaces" || !encodedDetail) return { id: "", primary: false };
  try {
    const detail = decodeURIComponent(encodedDetail);
    return detail === PRIMARY_WORKSPACE ? { id: "", primary: true } : { id: detail, primary: false };
  } catch {
    return { id: "", primary: false };
  }
}

export function Workspaces() {
  const queryClient = useQueryClient();
  const workspaces = useQuery({ queryKey: ["workspaces"], queryFn: api.workspaces });
  const initialDetail = useMemo(detailFromHash, []);
  const [selected, setSelected] = useState("");
  const [detailId, setDetailId] = useState(initialDetail.id);
  const [showPrimaryDetail, setShowPrimaryDetail] = useState(initialDetail.primary);
  const [filter, setFilter] = useState("");
  const [showAdd, setShowAdd] = useState(false);
  const [candidateId, setCandidateId] = useState("");
  const [candidatePath, setCandidatePath] = useState("");
  const [idTouched, setIdTouched] = useState(false);
  const [browsePath, setBrowsePath] = useState("");
  const [browseInput, setBrowseInput] = useState("");
  const [showHidden, setShowHidden] = useState(false);
  const [message, setMessage] = useState<{ tone: "success" | "danger" | "warning"; text: string } | null>(null);

  const items = workspaces.data?.workspaces ?? [];
  const filteredItems = useMemo(() => {
    const query = filter.trim().toLowerCase();
    if (!query) return items;
    return items.filter((item) => item.id.toLowerCase().includes(query) || item.workspace.toLowerCase().includes(query));
  }, [filter, items]);
  const detail = useQuery({
    queryKey: ["workspace-config", detailId],
    queryFn: () => api.workspaceConfig(detailId),
    enabled: !!detailId,
  });
  const browser = useQuery({
    queryKey: ["workspace-browser", browsePath, showHidden],
    queryFn: () => api.browseWorkspaces(browsePath, showHidden),
    enabled: showAdd,
  });

  useEffect(() => {
    const syncDetail = () => {
      const next = detailFromHash();
      setDetailId(next.id);
      setShowPrimaryDetail(next.primary);
      if (next.primary) setSelected(PRIMARY_WORKSPACE);
      else if (next.id) setSelected(next.id);
    };
    window.addEventListener("hashchange", syncDetail);
    return () => window.removeEventListener("hashchange", syncDetail);
  }, []);

  useEffect(() => {
    if (!workspaces.data) return;
    if (selected !== PRIMARY_WORKSPACE && selected && !items.some((item) => item.id === selected)) setSelected("");
    if (detailId && !items.some((item) => item.id === detailId)) {
      window.history.replaceState(null, "", "#workspaces");
      setDetailId("");
    }
  }, [detailId, items, selected, workspaces.data]);

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

  const closeDetail = () => {
    window.history.replaceState(null, "", "#workspaces");
    setDetailId("");
    setShowPrimaryDetail(false);
  };

  const openDetail = (id: string) => {
    window.location.hash = `workspaces/${encodeURIComponent(id)}`;
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

  const isDetailView = showPrimaryDetail || !!detailId;
  const activeNamedCount = items.filter((item) => item.active).length;
  const primaryActive = workspaces.data?.primary.active ?? false;

  return <>
    <PageHeader
      eyebrow={isDetailView ? "Workspace detail" : "Named runtimes"}
      title={showPrimaryDetail ? workspaces.data?.primary.id ?? "Primary workspace" : detailId || "Workspaces"}
      description={isDetailView
        ? "Inspect workspace identity, runtime paths, effective configuration, and management actions."
        : "Select a workspace first, then choose the next action without losing context in the list."}
      actions={isDetailView
        ? <Button variant="secondary" onClick={closeDetail}><ChevronLeft size={15} /> Back to workspaces</Button>
        : <Button onClick={showAdd ? () => setShowAdd(false) : openAdd}>{showAdd ? <X size={15} /> : <Plus size={15} />}{showAdd ? "Cancel" : "Add workspace"}</Button>}
    />

    {message && <Notice tone={message.tone}>{message.text}</Notice>}

    {!isDetailView && <>
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

      <div className="workspace-summary-strip">
        <div><span>Total workspaces</span><strong>{items.length + 1}</strong></div>
        <div><span>Active runtimes</span><strong>{activeNamedCount + (primaryActive ? 1 : 0)}</strong></div>
        <div><span>Restart pending</span><strong>{items.length - activeNamedCount + (primaryActive ? 0 : 1)}</strong></div>
      </div>

      <Card
        className="workspace-directory-panel"
        title="Workspace list"
        description="Click a workspace to reveal the actions available for it."
        actions={<label className="workspace-search"><Search size={15} /><input value={filter} onChange={(event) => setFilter(event.target.value)} placeholder="Search ID or path" /></label>}
      >
        <div className="workspace-group">
          <div className="workspace-group-head"><span>Primary workspace</span><small>Global configuration</small></div>
          <WorkspaceRow
            id={workspaces.data?.primary.id ?? "primary"}
            path={workspaces.data?.primary.workspace ?? ""}
            selected={selected === PRIMARY_WORKSPACE}
            primary
            active={workspaces.data?.primary.active ?? true}
            onSelect={() => setSelected(selected === PRIMARY_WORKSPACE ? "" : PRIMARY_WORKSPACE)}
            onView={() => openDetail(PRIMARY_WORKSPACE)}
          />
        </div>

        <div className="workspace-group">
          <div className="workspace-group-head"><span>Registered workspaces</span><small>{filteredItems.length} of {items.length}</small></div>
          <div className="workspace-list-focused">
            {filteredItems.map((item) => <WorkspaceRow
              key={item.id}
              id={item.id}
              path={item.workspace}
              selected={selected === item.id}
              enabled={item.enabled}
              active={item.active}
              onSelect={() => setSelected(selected === item.id ? "" : item.id)}
              onView={() => openDetail(item.id)}
            />)}
            {!items.length && <EmptyState title="No named workspaces" description="Use Add workspace to register a local repository." />}
            {!!items.length && !filteredItems.length && <EmptyState title="No matching workspaces" description="Try another workspace ID or path." />}
          </div>
        </div>
      </Card>
    </>}

    {showPrimaryDetail && workspaces.data && <PrimaryWorkspaceDetail primary={workspaces.data.primary} />}
    {detailId && <WorkspaceEditor
      id={detailId}
      query={detail}
      registryRevision={workspaces.data?.revision ?? ""}
      onRemoved={(text) => {
        window.history.replaceState(null, "", "#workspaces");
        setDetailId("");
        setSelected("");
        setMessage({ tone: "success", text });
      }}
    />}
  </>;
}

function WorkspaceRow({ id, path, selected, primary = false, enabled = true, active, onSelect, onView }: {
  id: string;
  path: string;
  selected: boolean;
  primary?: boolean;
  enabled?: boolean;
  active: boolean;
  onSelect: () => void;
  onView: () => void;
}) {
  return <div className={`workspace-row ${selected ? "selected" : ""}`}>
    <button className="workspace-row-main" onClick={onSelect} aria-expanded={selected}>
      <span className="workspace-row-icon"><FolderGit2 size={20} /></span>
      <span className="workspace-row-identity"><strong>{id}</strong><small>{path}</small></span>
      <span className="workspace-row-status">
        <Badge tone={primary || enabled ? "info" : "neutral"}>{primary ? "Primary" : enabled ? "Enabled" : "Disabled"}</Badge>
        <Badge tone={active ? "success" : "warning"}>{active ? "Active" : "Restart"}</Badge>
      </span>
      <ChevronRight size={18} className="workspace-row-chevron" />
    </button>
    {selected && <div className="workspace-row-actions">
      <div><strong>Choose the next action</strong><span>{primary ? "Inspect the global workspace identity and paths." : "Open configuration, runtime paths, and management controls."}</span></div>
      <Button onClick={onView}><Eye size={15} /> View details <ArrowRight size={14} /></Button>
    </div>}
  </div>;
}

function PrimaryWorkspaceDetail({ primary }: {
  primary: { id: string; workspace: string; active: boolean; configPath: string };
}) {
  return <div className="workspace-detail-stack">
    <div className="workspace-detail-hero">
      <span className="workspace-detail-icon"><FolderGit2 size={24} /></span>
      <div><span>Primary workspace</span><strong>{primary.id}</strong><small>{primary.workspace}</small></div>
      <Badge tone={primary.active ? "success" : "warning"}>{primary.active ? "Active" : "Restart required"}</Badge>
    </div>
    <Card title="Workspace identity" description="The primary root is owned by the global configuration and cannot be removed here.">
      <dl className="definition-grid">
        <div><dt>Workspace ID</dt><dd><code>{primary.id}</code></dd></div>
        <div><dt>Runtime status</dt><dd>{primary.active ? "Active" : "Pending restart"}</dd></div>
        <div><dt>Root</dt><dd>{primary.workspace}</dd></div>
        <div><dt>Configuration file</dt><dd>{primary.configPath}</dd></div>
      </dl>
    </Card>
    <Notice tone="info">Edit this workspace through Global Configuration. Named workspace override and removal actions are intentionally unavailable for the primary root.</Notice>
  </div>;
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
      setRaw(JSON.stringify(data.override, null, 2));
      setDirty(false);
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
  if (!query.data) return <EmptyState title="Unable to load workspace" description={query.error?.message ?? "Return to the workspace list and try again."} />;

  return <div className="workspace-detail-stack">
    <div className="workspace-detail-hero">
      <span className="workspace-detail-icon"><FolderGit2 size={24} /></span>
      <div><span>Named workspace</span><strong>{id}</strong><small>{query.data.registration.workspace}</small></div>
      <span className="workspace-row-status"><Badge tone={query.data.registration.enabled ? "info" : "neutral"}>{query.data.registration.enabled ? "Enabled" : "Disabled"}</Badge><Badge tone={query.data.registration.active ? "success" : "warning"}>{query.data.registration.active ? "Active" : "Restart"}</Badge></span>
    </div>
    {message && <Notice tone={message.tone}>{message.text}</Notice>}
    <div className="workspace-detail-grid">
      <Card title="Workspace identity" description="Registration-owned paths and endpoint.">
        <dl className="definition-grid">
          <div><dt>Root</dt><dd>{query.data.registration.workspace}</dd></div>
          <div><dt>Endpoint</dt><dd><code>/mcp/workspaces/{id}</code> <ExternalLink size={13} /></dd></div>
          <div><dt>Config file</dt><dd>{query.data.registration.configPath}</dd></div>
          <div><dt>Data directory</dt><dd>{query.data.registration.dataDir}</dd></div>
        </dl>
      </Card>
      <Card title="Effective configuration" description="Global config plus this override and registry-owned fields.">
        <details className="json-details"><summary><Braces size={16} /> Inspect effective JSON</summary><pre>{JSON.stringify(query.data.effective, null, 2)}</pre></details>
      </Card>
    </div>
    <Card
      title="Inheritance and overrides"
      description="See which values come from the global configuration, which paths are explicitly changed, and which entries can be compacted."
      actions={<Button variant="secondary" onClick={() => { setRaw(JSON.stringify(query.data.provenance.compactedOverride, null, 2)); setDirty(true); setMessage(null); }} disabled={!query.data.provenance.redundantPaths.length}><Sparkles size={15} /> Apply compacted preview</Button>}
    >
      <div className="override-provenance-summary">
        <div><span><CheckCircle2 size={15} /></span><small>Explicit paths</small><strong>{query.data.provenance.entries.length}</strong></div>
        <div><span><ArrowRight size={15} /></span><small>Inherited top-level</small><strong>{query.data.provenance.inheritedTopLevel.length}</strong></div>
        <div><span><Sparkles size={15} /></span><small>Redundant paths</small><strong>{query.data.provenance.redundantPaths.length}</strong></div>
      </div>
      {query.data.provenance.truncated && <Notice tone="warning">The provenance list reached its safety limit. The raw override remains complete.</Notice>}
      {!!query.data.provenance.redundantPaths.length && <Notice tone="info"><strong>Compaction available:</strong> {query.data.provenance.redundantPaths.join(", ")}</Notice>}
      <div className="override-provenance-list">
        {query.data.provenance.entries.map((entry: any) => <details key={entry.path} className="override-provenance-row">
          <summary><code>{entry.path}</code><Badge tone={entry.state === "removed" ? "warning" : "info"}>{entry.state}</Badge></summary>
          <pre>{JSON.stringify({ inherited: entry.inherited, override: entry.override, effective: entry.effective }, null, 2)}</pre>
        </details>)}
        {!query.data.provenance.entries.length && <EmptyState title="Fully inherited" description="This workspace currently inherits every configurable value from the global configuration." />}
      </div>
      <details className="json-details top-gap"><summary><Braces size={15} /> Inherited top-level fields</summary><pre>{JSON.stringify(query.data.provenance.inheritedTopLevel, null, 2)}</pre></details>
    </Card>
    <Card title={`${id} override`} description="Objects merge recursively; arrays replace; null removes an inherited key." actions={<><Badge tone={dirty ? "warning" : "success"}>{dirty ? "Unsaved" : "In sync"}</Badge><Button onClick={() => save.mutate()} loading={save.isPending} disabled={!dirty}><Save size={15} /> Save</Button></>}>
      <TextArea className="control textarea code-editor compact" value={raw} onChange={(event) => { setRaw(event.target.value); setDirty(true); setMessage(null); }} spellCheck={false} />
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
