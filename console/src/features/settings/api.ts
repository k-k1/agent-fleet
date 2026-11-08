// features/settings/api — typed wrappers for the settings endpoints that have a typed wire
// shape so far. The paths stay string literals here: control-plane/console_routes_test.go reads
// them as text to prove each one has a route.
//
// `api()` resolves with the parsed body whether the status was 2xx or not, so every reply is
// `T | { error }`; `isOk` is the single place that tells the two apart (a truthy `error`, the
// same test the callers used inline).
import { api, apiJSON, type ApiError } from "../../core/api/client.ts";
import type { CLIRelease } from "./cliRelease.ts";

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
