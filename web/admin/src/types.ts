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

export interface TunnelConfig {
  enabled?: boolean;
  tunnelId?: string;
  mode?: "fast" | "full" | string;
  toolProfile?: string;
  profile?: string;
  runtimeKeyEnv?: string;
  organizationId?: string;
}

export interface RemoteIngressConfig {
  enabled?: boolean;
  provider?: "external" | "cloudflare" | string;
  mode?: "fixed" | "session" | string;
  workspaceId?: string;
  toolProfile?: string;
  localPort: number;
  publicUrl?: string;
  authTokenEnv: string;
  authTokenFallbackEnv?: string;
  providerTokenEnv?: string;
  binary?: string;
}

export interface RemoteIngressRuntimeStatus {
  name: string;
  provider: string;
  mode: string;
  workspaceId: string;
  toolProfile: string;
  localPort: number;
  publicUrl?: string;
  authTokenEnv: string;
  authTokenFallbackEnv?: string;
  authConfigured: boolean;
  primaryAuthConfigured: boolean;
  primaryAuthReady: boolean;
  fallbackAuthConfigured?: boolean;
  fallbackAuthReady?: boolean;
  providerTokenConfigured?: boolean;
  listenerReachable: boolean;
  mcpReady: boolean;
  protocolVersion?: string;
  toolCount: number;
  issue?: string;
}

export interface RemoteIngressStatusResponse {
  generatedAt: string;
  ingresses: RemoteIngressRuntimeStatus[];
  truncated: boolean;
}

export interface ToolExposureConfig {
  allowedGroups?: string[];
  allowedTools?: string[];
  deniedTools?: string[];
}

export interface ToolProfileConfig {
  name?: string;
  description?: string;
  allowedGroups?: string[];
  allowedTools?: string[];
  deniedTools?: string[];
  outputMode?: "both" | "text" | "structured" | string;
  compactDefaults?: boolean;
}

export interface WormholeConfig {
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
  tunnels?: Record<string, TunnelConfig>;
  remoteIngresses?: Record<string, RemoteIngressConfig>;
  memory: MemoryConfig;
  mcpServers?: Record<string, MCPServerConfig>;
  tools?: ToolExposureConfig;
  toolProfiles?: Record<string, ToolProfileConfig>;
  maxReadChars?: number;
  readDefault?: number;
  maxBatchReadChars?: number;
  maxCommandOutput?: number;
  commandOutputDefault?: number;
  maxBodyBytes?: number;
  maxProcesses?: number;
  maxConcurrentToolCalls?: number;
  gitStatusCacheMs?: number;
  audit: boolean;
  auditArgs: boolean;
  httpLog?: boolean;
}

export interface ProfileTool {
  name: string;
  title: string;
  description: string;
  scope: "session" | "workspace" | string;
  readOnly: boolean;
  destructive: boolean;
  openWorld: boolean;
  workspaceIds: string[];
}

export interface ProfileTunnel {
  name: string;
  enabled: boolean;
  tunnelId: string;
  profile: string;
  toolProfile: string;
}

export interface ToolProfile {
  id: "remote-read" | "fast" | "full" | string;
  name: string;
  endpoint: string;
  description: string;
  tools: ProfileTool[];
  tunnels: ProfileTunnel[];
  builtIn: boolean;
  active: boolean;
  restartRequired: boolean;
  activeContractHash: string;
  outputMode: "both" | "text" | "structured" | string;
  compactDefaults: boolean;
  allowedGroups: string[];
  allowedTools: string[];
  deniedTools: string[];
  contractHash: string;
}

export interface ProfilesResponse {
  profiles: ToolProfile[];
  workspaceCount: number;
}

export interface ToolCatalogTool {
  name: string;
  title: string;
  description: string;
  groups: string[];
  readOnly: boolean;
  destructive: boolean;
  openWorld: boolean;
  workspaceIds: string[];
}

export interface ToolCatalogGroup {
  name: string;
  toolCount: number;
}

export interface ToolCatalogResponse {
  tools: ToolCatalogTool[];
  groups: ToolCatalogGroup[];
  workspaceCount: number;
}

export interface ConfigSnapshot {
  config: WormholeConfig;
  revision: string;
  path: string;
  restartRequired: boolean;
}

export interface RuntimeToolMetric {
  tool: string;
  module: string;
  started_calls: number;
  completed_calls: number;
  succeeded: number;
  failed: number;
  policy_rejected: number;
  canceled: number;
  deadline_exceeded: number;
  in_flight: number;
  max_in_flight: number;
  last_call_at?: string;
  last_failure_at?: string;
  latency_us: { total: number; average: number; max: number };
}

export interface RuntimeRecentCall {
  call_id: string;
  tool: string;
  module: string;
  status: string;
  started_at: string;
  duration_us: number;
  audit_write_failed: boolean;
  memory_observation?: string;
}

export interface RuntimeMetrics {
  started_at: string;
  uptime_seconds: number;
  started_calls: number;
  completed_calls: number;
  succeeded: number;
  failed: number;
  policy_rejected: number;
  canceled: number;
  deadline_exceeded: number;
  in_flight: number;
  max_in_flight: number;
  concurrency: { limit: number; executing: number };
  latency_us: { total: number; average: number; max: number };
  tools?: RuntimeToolMetric[];
  recent_calls?: RuntimeRecentCall[];
  audit: Record<string, unknown>;
  memory_observations: Record<string, number>;
  repository_cache: Record<string, unknown>;
}

export interface OperationsWorkspace {
  id: string;
  root: string;
  configId: string;
  mode: string;
  policy: string;
  startupWarnings: string[];
  metrics: RuntimeMetrics;
  modules: Record<string, unknown>;
}

export interface OperationsResponse {
  generatedAt: string;
  workspaces: OperationsWorkspace[];
  sharedResources: Record<string, unknown>;
  sessionRouter: Record<string, unknown>;
}

export interface ApprovalRecord {
  workspaceId: string;
  root: string;
  id: string;
  action: string;
  actions: string[];
  consumed_actions: string[];
  reason: string;
  status: string;
  created: string;
  expires_at: string;
  approved_at?: string;
  denied_at?: string;
  consumed_at?: string;
}

export interface ApprovalsResponse {
  approvals: ApprovalRecord[];
  count: number;
  truncated: boolean;
}

export interface AuditRecord {
  workspaceId: string;
  ts: string;
  call_id: string;
  tool: string;
  tool_module: string;
  status: string;
  ok: boolean;
  duration_us: number;
  workspace_id: string;
  workspace: string;
  session_id: string;
  error?: string;
  error_kind?: string;
  args?: unknown;
  [key: string]: unknown;
}

export interface AuditResponse {
  records: AuditRecord[];
  count: number;
  truncated: boolean;
}

export interface UpstreamToolContract {
  toolCount: number;
  hash: string;
  tools: string[];
}

export interface UpstreamContractDiff {
  added: string[];
  removed: string[];
  changed: string[];
  changedCount: number;
}

export interface UpstreamMCPStatus {
  name: string;
  configured: boolean;
  active: boolean;
  transport?: string;
  startupMode?: string;
  required?: boolean;
  refreshAvailable?: boolean;
  health?: Record<string, unknown>;
  activeContract?: UpstreamToolContract;
  cachedContract?: UpstreamToolContract;
  liveContract?: UpstreamToolContract;
  activeToDesired?: UpstreamContractDiff;
  cachedToLive?: UpstreamContractDiff;
  restartRequired?: boolean;
  cachedError?: string;
  liveError?: string;
  error?: string;
}

export interface UpstreamWorkspaceStatus {
  id: string;
  root: string;
  servers: UpstreamMCPStatus[];
}

export interface UpstreamMCPResponse {
  generatedAt: string;
  workspaces: UpstreamWorkspaceStatus[];
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

export interface WorkspaceOverrideProvenanceEntry {
  path: string;
  state: "overridden" | "removed" | string;
  inherited?: unknown;
  override?: unknown;
  effective?: unknown;
}

export interface WorkspaceOverrideProvenance {
  entries: WorkspaceOverrideProvenanceEntry[];
  inheritedTopLevel: string[];
  compactedOverride: Record<string, unknown>;
  redundantPaths: string[];
  truncated: boolean;
}

export interface WorkspaceConfigResponse {
  registration: WorkspaceSummary;
  override: Record<string, unknown>;
  effective: WormholeConfig;
  provenance: WorkspaceOverrideProvenance;
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
