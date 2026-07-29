export interface MemoryConfig {
  enabled: boolean;
  provider: string;
  endpoint?: string;
  secretEnv?: string;
  timeoutMs?: number;
  captureMode?: "off" | "metadata" | "selected" | string;
  tokenBudget?: number;
  agentId?: string;
  required?: boolean;
  projectStrategy?: "git-origin" | "path-hash" | string;
  options?: Record<string, unknown>;
  queueSize?: number;
  deliveryWorkers?: number;
  deliveryTimeoutMs?: number;
  retryMaxAttempts?: number;
  retryBackoffMs?: number;
  healthCacheMs?: number;
}

export interface MCPServerPolicyConfig {
  trustAnnotations?: boolean;
  default?: "approval" | "read-only" | "always-approve" | string;
  readOnlyTools?: string[];
  approvalTools?: string[];
  alwaysApproveTools?: string[];
}

export interface MCPServerConfig {
  enabled?: boolean;
  transport?: "stdio" | "streamable-http" | string;
  command?: string;
  args?: string[];
  cwd?: string;
  url?: string;
  headers?: Record<string, string>;
  headerRefs?: Record<string, string>;
  allowRemote?: boolean;
  env?: Record<string, string>;
  envRefs?: Record<string, string>;
  inheritEnv?: string[];
  workspaceIds?: string[];
  allowedTools?: string[];
  deniedTools?: string[];
  required?: boolean;
  startupMode?: "eager" | "background" | "lazy" | string;
  startupTimeoutMs?: number;
  callTimeoutMs?: number;
  healthTimeoutMs?: number;
  healthCacheMs?: number;
  failureCooldownMs?: number;
  maxConcurrency?: number;
  maxTools?: number;
  policy?: MCPServerPolicyConfig;
}

export interface ToolExposureConfig {
  allowedGroups?: string[];
  allowedTools?: string[];
  deniedTools?: string[];
}

export interface CodebridgeConfig {
  workspace: string;
  extraRoots?: string[];
  mode: "safe" | "full" | string;
  policy: "strict" | "balanced" | "full" | string;
  port: number;
  host: string;
  allowedOrigins?: string[];
  noTunnel?: boolean;
  tunnelBin?: string;
  tunnelId?: string;
  organizationId?: string;
  profile?: string;
  profileDir?: string;
  runtimeKeyEnv?: string;
  memory: MemoryConfig;
  mcpServers?: Record<string, MCPServerConfig>;
  tools?: ToolExposureConfig;
  maxReadChars?: number;
  readDefault?: number;
  maxBatchReadChars?: number;
  maxCommandOutput?: number;
  commandOutputDefault?: number;
  maxBodyBytes?: number;
  maxProcesses?: number;
  gitStatusCacheMs?: number;
  audit: boolean;
  auditArgs: boolean;
  httpLog?: boolean;
}

export interface ConfigSnapshot {
  config: CodebridgeConfig;
  revision: string;
  path: string;
  restartRequired: boolean;
}

export interface AdminAuthStatus {
  configured: boolean;
  authenticated: boolean;
  username: string;
  credentialPath: string;
}

export interface Bootstrap {
  name: string;
  version: string;
  tier: string;
  activeConfigId: string;
  workspaceId: string;
  activeWorkspaceIds: string[];
  configPath: string;
  homePath: string;
  restartRequiredAfterSave: boolean;
  security: {
    loopbackOnly: boolean;
    sameOriginWrites: boolean;
    csrfProtected: boolean;
    adminAuthentication: boolean;
    secretValuesReadable: boolean;
  };
  startupWarnings: string[];
}

export interface WorkspaceSummary {
  id: string;
  workspace: string;
  enabled: boolean;
  active: boolean;
  configPath: string;
  dataDir: string;
  revision: string;
  createdAt: string;
  updatedAt: string;
}

export interface WorkspacesResponse {
  primary: { id: string; workspace: string; active: boolean; configPath: string };
  workspaces: WorkspaceSummary[];
  revision: string;
}

export interface WorkspaceBrowseDirectory {
  name: string;
  path: string;
  git: boolean;
  suggestedId: string;
}

export interface WorkspaceBrowseResponse {
  root: string;
  path: string;
  parent: string | null;
  directories: WorkspaceBrowseDirectory[];
  showHidden: boolean;
  truncated: boolean;
  limit: number;
  selected: { path: string; git: boolean; suggestedId: string };
}

export interface WorkspaceMutationResponse {
  revision: string;
  restartRequired: boolean;
  message?: string;
  workspace?: WorkspaceSummary;
  id?: string;
  removed?: boolean;
  configDeleted?: boolean;
  statePreserved?: boolean;
  activeUntilRestart?: boolean;
}

export interface WorkspaceConfigResponse {
  registration: WorkspaceSummary;
  override: Record<string, unknown>;
  effective: CodebridgeConfig;
  revision: string;
  restartRequired: boolean;
}

export interface SecretSummary {
  name: string;
  configured: boolean;
  managed: boolean;
  source: "dotenv" | "environment" | "missing";
  referencedBy: string[];
}

export interface SecretsResponse {
  path: string;
  secrets: SecretSummary[];
  valuesReadable: boolean;
}
