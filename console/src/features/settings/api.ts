// features/settings/api — typed wrappers for the settings endpoints that have a typed wire
// shape so far. The paths stay string literals here: control-plane/console_routes_test.go reads
// them as text to prove each one has a route.
//
// `api()` resolves with the parsed body whether the status was 2xx or not, so every reply is
// `T | { error }`; `isOk` is the single place that tells the two apart (a truthy `error`, the
// same test the callers used inline).
import { api, apiJSON, type ApiError } from "../../core/api/client.ts";
import type { CLIRelease } from "./cliRelease.ts";
import type { MemberAutoStop, WsSizing } from "./parts/adminShared.ts";

export type ErrorReply = { error: ApiError };
export type Reply<T> = T | ErrorReply;

/** True for a real answer: not null, and no truthy `error` member. */
export function isOk<T>(r: Reply<T> | null | undefined): r is T {
  return !!r && !(r as { error?: unknown }).error;
}

// --- CP-owned workspace settings: GET/PUT /api/env/ws-settings -----------------------------
// control-plane/ws_settings.go assembles these as map[string]any (get / put / reissuePreview),
// so the optionality below is what each handler actually emits. The core trio is always
// present; preview and dev-IdP fields ride on the same body.

export type WsSettings = {
  agentUpdate: boolean;
  /** The operator gate: whether this tenant lets a member opt in to agentUpdate. */
  allowAgentUpdate: boolean;
  autoImageRefresh: boolean;
  /** "" when this deployment issues no preview subdomains. */
  previewDomain?: string;
  previewPorts?: number[];
  previewFixedSlug?: boolean;
  previewPublic?: boolean;
  previewTenantShare?: boolean;
  previewCrossOrigin?: boolean;
  previewMaxPorts?: number;
  /** Port → issued URL; empty while the workspace is stopped. */
  previewUrls?: Record<string, string>;
  previewShareLinks?: Record<string, string>;
  previewTempPorts?: number[];
  devIdpAvailable?: boolean;
  /** Only when devIdpAvailable. */
  devIdpIssuer?: string;
  /** Only once a client has been issued. */
  devIdpClientId?: string;
  /** GET only, and only when the watcher has an answer. */
  cliRelease?: CLIRelease;
};

/** The body of PUT /api/env/ws-settings: only the known keys are honoured, each optional. */
export type WsSettingsPatch = Partial<
  Pick<
    WsSettings,
    | "agentUpdate"
    | "autoImageRefresh"
    | "previewPorts"
    | "previewFixedSlug"
    | "previewPublic"
    | "previewTenantShare"
    | "previewCrossOrigin"
  >
>;

/** POST …/preview/reissue answers the preview block plus these two (no dev-IdP fields). */
export type PreviewReissueReply = Partial<WsSettings> & { previewReissued: boolean };

/** POST …/dev-idp/rotate: the new client id (the secret reaches the container at next start). */
export type DevIdpRotateReply = { devIdpClientId: string; restartRequired: boolean };

/** One test persona (control-plane/dev_idp_persona.go devIdPPersonaJSON). */
export type DevIdpPersona = { id: string; handle: string; name: string; email: string; sub: string };
/** The answer of every persona endpoint: the whole list after the change. */
export type DevIdpPersonaList = { personas: DevIdpPersona[]; max: number; domain: string };

export const getWsSettings = (): Promise<Reply<WsSettings>> => api("api/env/ws-settings");
export const putWsSettings = (patch: WsSettingsPatch): Promise<Reply<WsSettings>> =>
  apiJSON("api/env/ws-settings", "PUT", patch);
export const reissuePreview = (): Promise<Reply<PreviewReissueReply>> =>
  apiJSON("api/env/ws-settings/preview/reissue", "POST", {});
export const rotateDevIdp = (): Promise<Reply<DevIdpRotateReply>> =>
  apiJSON("api/env/ws-settings/dev-idp/rotate", "POST", {});

export const listDevIdpPersonas = (): Promise<Reply<DevIdpPersonaList>> =>
  api("api/env/ws-settings/dev-idp/personas");
export const createDevIdpPersona = (handle: string, name: string): Promise<Reply<DevIdpPersonaList>> =>
  apiJSON("api/env/ws-settings/dev-idp/personas", "POST", { handle, name });
export const deleteDevIdpPersona = (id: string): Promise<Reply<DevIdpPersonaList>> =>
  apiJSON(`api/env/ws-settings/dev-idp/personas/${encodeURIComponent(id)}`, "DELETE");

// --- Agent toolchains: GET/PUT /api/env/toolchains (workspace/agent/env_toolchains.go) -----

export type Toolchains = {
  node: string;
  java: string;
  go: string;
  timezone: string;
  agentUpdate: boolean;
  /** Offered for selection: installed ∪ installable. */
  java_available?: string[];
  java_installed?: string[];
  /** JDK id → the version its JAVA_HOME resolves to. */
  java_versions?: Record<string, string>;
  node_options?: string[];
  node_installed?: string[];
  /** Major → the patch sessions get. */
  node_versions?: Record<string, string>;
  go_options?: string[];
  tz_options?: string[];
};

/** PUT body: the four selections (the Console always sends all of them). */
export type ToolchainsPatch = Pick<Toolchains, "node" | "java" | "go" | "timezone">;

export const getToolchains = (): Promise<Reply<Toolchains>> => api("api/env/toolchains");
export const putToolchains = (next: ToolchainsPatch): Promise<Reply<Toolchains>> =>
  apiJSON("api/env/toolchains", "PUT", next);

// --- Agent tool versions: GET /api/env/tool-versions (workspace/agent/env_tool_versions.go) -

export type ToolBin = { path: string; version: string; raw: string };
export type BootInstall = { status: "ok" | "skipped" | "failed"; reason?: "no_space" | "network" | "checksum" | "other" };
export type ToolReport = {
  name: string;
  pin?: string;
  /** null when absent. */
  effective: ToolBin | null;
  baked: ToolBin | null;
  userLocal: ToolBin | null;
  overridden: boolean;
  bootInstall?: BootInstall;
};
export type ToolVersions = { checkedAt: string; tools: ToolReport[] };

export const getToolVersions = (refresh = false): Promise<Reply<ToolVersions>> =>
  api("api/env/tool-versions" + (refresh ? "?refresh=1" : ""));

// --- Agent behaviour toggles (workspace/agent: claude / codex settings, agent_rtk.go) --------
// Each handler assembles a map[string]any; the keys below are the ones it emits. A PUT answers
// with the same body as the GET.

/** GET/PUT /api/claude/settings (agents/claude/settings.go settingsBody). */
export type ClaudeSettings = {
  remoteControlAtStartup: boolean;
  agentPushNotifEnabled: boolean;
  skipDangerousModePermissionPrompt: boolean;
  idleCompaction: boolean;
  rtk_enabled: boolean;
  rtk_available: boolean;
};
/** The PUT body: every key optional, only those present are applied (settingsReq). */
export type ClaudeSettingsPatch = Partial<
  Pick<ClaudeSettings, "remoteControlAtStartup" | "agentPushNotifEnabled" | "idleCompaction"> & { rtk: boolean }
>;

/** GET/PUT /api/codex/settings (agents/codex/settings.go settingsBody). */
export type CodexSettings = {
  rate_limit_model_nudge: boolean;
  memories: boolean;
  /** Whether Codex has materialised ~/.codex/memories yet. */
  memories_ready: boolean;
};
export type CodexSettingsPatch = Partial<Pick<CodexSettings, "rate_limit_model_nudge" | "memories">>;

/** GET/PUT /api/agents/rtk (agent_rtk.go agentRTKBody). */
export type AgentRtkSettings = {
  rtk_available: boolean;
  codex_rtk: boolean;
  opencode_rtk: boolean;
  agy_rtk: boolean;
  copilot_rtk: boolean;
};
export type AgentRtkPatch = Partial<Omit<AgentRtkSettings, "rtk_available">>;

export const getClaudeSettings = (): Promise<Reply<ClaudeSettings>> => api("api/claude/settings");
export const putClaudeSettings = (patch: ClaudeSettingsPatch): Promise<Reply<ClaudeSettings>> =>
  apiJSON("api/claude/settings", "PUT", patch);
export const getCodexSettings = (): Promise<Reply<CodexSettings>> => api("api/codex/settings");
export const putCodexSettings = (patch: CodexSettingsPatch): Promise<Reply<CodexSettings>> =>
  apiJSON("api/codex/settings", "PUT", patch);
export const getAgentRtk = (): Promise<Reply<AgentRtkSettings>> => api("api/agents/rtk");
export const putAgentRtk = (patch: AgentRtkPatch): Promise<Reply<AgentRtkSettings>> =>
  apiJSON("api/agents/rtk", "PUT", patch);

// --- On-demand CLI install: GET /api/connections/{kiro,muse}/install ------------------------
/** The version facts ride on GET only, and are skipped while an install runs. */
export type CliInstallStatus = {
  state: string;
  error?: string;
  installed?: boolean;
  version?: string;
  pin?: string;
  updateAvailable?: boolean;
};
export const getKiroInstall = (): Promise<Reply<CliInstallStatus>> => api("api/connections/kiro/install");
export const getMuseInstall = (): Promise<Reply<CliInstallStatus>> => api("api/connections/muse/install");

// --- Admin overview endpoints (control-plane: admin_sessions.go, audit.go, usage.go) --------

/** One session of GET /api/admin/sessions (adminSessionRow), tagged with its owning member. */
export type AdminSessionRow = {
  tenant: string;
  user_key: string;
  email: string;
  /** running | stopped | none */
  workspace_state: string;
  name: string;
  kind: string;
  label: string;
  repo: string;
  dir: string;
  /** idle | working | question | stopped */
  state: string;
  alive: boolean;
  resumable: boolean;
  started: string;
};
export const getAdminSessions = (tenant: string): Promise<Reply<{ sessions: AdminSessionRow[] }>> =>
  api("api/admin/sessions" + (tenant ? "?tenant=" + encodeURIComponent(tenant) : ""));

/** One ledger row of GET /api/admin/audit (the map audit.go assembles). `detail` is free-form. */
export type AuditRow = {
  id: string;
  tenant: string;
  tenant_id: string;
  actor_kind: string;
  actor_id: string;
  actor_email: string;
  action: string;
  target: string;
  detail: unknown;
  http_status: number;
  at: string;
};
export const getAdminAudit = (tenant: string): Promise<Reply<{ audit: AuditRow[] }>> =>
  api("api/admin/audit" + (tenant ? "?tenant=" + encodeURIComponent(tenant) : ""));

/** One member's total of GET /api/admin/usage (usageTotal). */
export type UsageTotal = {
  tenant: string;
  user_key: string;
  email: string;
  running_secs: number;
  running_hours: number;
};
/** GET /api/admin/usage; the `monthly*` members ride along only when `months` was requested. */
export type AdminUsage = {
  from: string;
  to: string;
  /** Per member per day. */
  days: (UsageTotal & { day: string })[];
  totals: UsageTotal[];
  monthly?: unknown[];
  monthly_members?: unknown[];
  monthly_members_total?: number;
};
export const getAdminUsage = (query: string): Promise<Reply<AdminUsage>> =>
  api("api/admin/usage" + (query ? "?" + query : ""));

// --- Member detail (control-plane admin_stats.go, GET …/members/{key}/stats) ----------------
/** The Agent's stats body passed through, plus what the CP adds (`agent_silent`, `auto_stop`,
 *  `boot_failed`, the observed-disk fields). A stopped workspace answers `{ running: false }`
 *  and little more, so every figure is optional. */
export type MemberStats = {
  running?: boolean;
  /** Running per the runtime, but the Agent has not answered for minutes. */
  agent_silent?: boolean;
  mem_used?: number;
  mem_max?: number;
  cpu_pct?: number;
  disk_used?: number;
  disk_total?: number;
  disk_quota?: number;
  disk_observed_at?: string;
  disk_pending?: boolean;
  home_own?: boolean;
  auto_stop?: MemberAutoStop;
  /** Tool → why its install failed at the last start; absent when none did. */
  boot_failed?: Record<string, string>;
};

/** GET /api/admin/workspace-sizing: deployment-wide and immutable while the Console runs. */
export const getWorkspaceSizing = (): Promise<Reply<WsSizing>> => api("api/admin/workspace-sizing");

// --- Egress allowlist (control-plane egress.go, super admin) -------------------------------
/** One host of GET /api/admin/egress: what the proxy let through and what it refused. */
export type EgressStat = { host: string; allowed: number; blocked: number };
export type EgressStats = { egress: EgressStat[]; days: number; mode: string; enforce: boolean };
export type EgressAllowEntry = {
  id: string;
  tenant_id: string;
  entry: string;
  /** "proposed" | "active" | "retired" */
  state: string;
  reason: string;
  added_by: string;
  added_at: string;
  /** Set when the stored entry no longer passes the CP's own validation. */
  invalid_reason?: string;
};
/** `builtin` is the CP's own hosts: the server always admits them, outside the editable list. */
export type EgressAllowlist = { allowlist: EgressAllowEntry[]; defaults?: string[]; builtin: string[] };
export const getEgressStats = (days: number): Promise<Reply<EgressStats>> => api("api/admin/egress?days=" + days);
export const getEgressAllowlist = (): Promise<Reply<EgressAllowlist>> => api("api/admin/egress/allowlist");
