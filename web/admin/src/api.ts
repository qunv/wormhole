import type {
  AdminAuthStatus,
  ApprovalRecord,
  ApprovalsResponse,
  AuditResponse,
  Bootstrap,
  CodebridgeConfig,
  ConfigSnapshot,
  OperationsResponse,
  ProfilesResponse,
  SecretsResponse,
  ToolCatalogResponse,
  WorkspaceBrowseResponse,
  WorkspaceConfigResponse,
  WorkspaceMutationResponse,
  WorkspacesResponse,
} from "./types";

const API = "/admin/api/v1";
export const AUTH_REQUIRED_EVENT = "codebridge-auth-required";

export class APIError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "APIError";
    this.status = status;
    this.code = code;
  }
}

function readCookie(name: string): string {
  const encoded = `${encodeURIComponent(name)}=`;
  for (const part of document.cookie.split(";")) {
    const value = part.trim();
    if (value.startsWith(encoded)) return decodeURIComponent(value.slice(encoded.length));
  }
  return "";
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  if (!["GET", "HEAD", "OPTIONS"].includes(method)) {
    headers.set("X-Codebridge-CSRF", readCookie("codebridge_admin_csrf"));
  }
  const response = await fetch(`${API}${path}`, { ...init, headers, credentials: "same-origin" });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = body?.error ?? {};
    const code = error.code ?? "request_failed";
    if (response.status === 401 && code === "authentication_required") {
      window.dispatchEvent(new Event(AUTH_REQUIRED_EVENT));
    }
    throw new APIError(response.status, code, error.message ?? `Request failed (${response.status})`);
  }
  return body as T;
}

export const api = {
  authStatus: () => request<AdminAuthStatus>("/auth/status"),
  setupAdmin: (username: string, password: string) =>
    request<{ configured: true; authenticated: true; username: string }>("/auth/setup", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),
  login: (username: string, password: string) =>
    request<{ authenticated: true; username: string }>("/auth/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),
  logout: () => request<{ authenticated: false }>("/auth/logout", { method: "POST" }),
  bootstrap: () => request<Bootstrap>("/bootstrap"),
  profiles: () => request<ProfilesResponse>("/profiles"),
  toolCatalog: () => request<ToolCatalogResponse>("/tools/catalog"),
  operations: () => request<OperationsResponse>("/operations"),
  approvals: (status = "pending", workspace = "", limit = 100) => {
    const query = new URLSearchParams({ status, limit: String(limit) });
    if (workspace) query.set("workspace", workspace);
    return request<ApprovalsResponse>(`/approvals?${query.toString()}`);
  },
  decideApproval: (workspaceId: string, id: string, decision: "approved" | "denied") =>
    request<ApprovalRecord>(`/approvals/${encodeURIComponent(workspaceId)}/${encodeURIComponent(id)}`, {
      method: "POST",
      body: JSON.stringify({ decision }),
    }),
  audit: (filters: { workspace?: string; tool?: string; status?: string; limit?: number } = {}) => {
    const query = new URLSearchParams();
    if (filters.workspace) query.set("workspace", filters.workspace);
    if (filters.tool) query.set("tool", filters.tool);
    if (filters.status) query.set("status", filters.status);
    query.set("limit", String(filters.limit ?? 100));
    return request<AuditResponse>(`/audit?${query.toString()}`);
  },
  config: () => request<ConfigSnapshot>("/config"),
  validateConfig: (config: CodebridgeConfig) =>
    request<{ valid: true; config: CodebridgeConfig }>("/config/validate", {
      method: "POST",
      body: JSON.stringify(config),
    }),
  saveConfig: (config: CodebridgeConfig, revision: string) =>
    request<ConfigSnapshot>("/config", {
      method: "PUT",
      headers: { "If-Match": `"${revision}"` },
      body: JSON.stringify(config),
    }),
  workspaces: () => request<WorkspacesResponse>("/workspaces"),
  browseWorkspaces: (path = "", showHidden = false) => {
    const query = new URLSearchParams();
    if (path) query.set("path", path);
    if (showHidden) query.set("showHidden", "true");
    return request<WorkspaceBrowseResponse>(`/workspaces/browse?${query.toString()}`);
  },
  createWorkspace: (id: string, workspace: string, revision: string) =>
    request<WorkspaceMutationResponse>("/workspaces", {
      method: "POST",
      headers: { "If-Match": `"${revision}"` },
      body: JSON.stringify({ id, workspace }),
    }),
  workspaceConfig: (id: string) => request<WorkspaceConfigResponse>(`/workspaces/${encodeURIComponent(id)}`),
  saveWorkspaceConfig: (id: string, override: Record<string, unknown>, revision: string) =>
    request<WorkspaceConfigResponse>(`/workspaces/${encodeURIComponent(id)}`, {
      method: "PUT",
      headers: { "If-Match": `"${revision}"` },
      body: JSON.stringify(override),
    }),
  removeWorkspace: (id: string, revision: string, deleteConfig = false) =>
    request<WorkspaceMutationResponse>(`/workspaces/${encodeURIComponent(id)}${deleteConfig ? "?deleteConfig=true" : ""}`, {
      method: "DELETE",
      headers: { "If-Match": `"${revision}"` },
    }),
  secrets: () => request<SecretsResponse>("/secrets"),
  setSecret: (name: string, value: string) =>
    request<{ name: string; configured: boolean; restartRequired: boolean }>(`/secrets/${encodeURIComponent(name)}`, {
      method: "PUT",
      body: JSON.stringify({ value }),
    }),
  deleteSecret: (name: string) =>
    request<{ name: string; configured: boolean; restartRequired: boolean }>(`/secrets/${encodeURIComponent(name)}`, {
      method: "DELETE",
    }),
};
