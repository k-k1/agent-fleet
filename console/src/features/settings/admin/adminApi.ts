// features/settings/admin/adminApi — typed wrappers for the admin endpoints the Admin tab
// calls. The paths stay string literals (control-plane/console_routes_test.go reads them as
// text); a caller gets `Reply<T>` and tells the two outcomes apart with `isOk` or `res.error`.
//
// A JSON `null` body is not an error and not an empty object. The wrappers whose caller only
// reads optional fields fold it to {} (`api` / `apiJSON` below); the ones whose caller needs a
// whole object keep `| null` in the type, so the caller leaves its "not loaded" state alone.
import { api as rawApi, apiJSON as rawApiJSON } from "../../../core/api/client.ts";
import type { Reply } from "../api.ts";
import type { Tenant } from "../parts/adminShared.ts";
import type { BrandStatus } from "./adminBrand.tsx";
import type { DevIdpStatus } from "./adminDevIdp.tsx";
import type { ComfyLanStatus } from "./adminEngineLan.tsx";
import type { IssuedEngineToken } from "./adminEngineIssueToken.tsx";
import type { CivitaiRedStatus, CivitaiTokenStatus, HfTokenStatus } from "./adminEngineTokens.tsx";
import type { EngineDiscoverResult } from "./adminEngineDiscover.tsx";
import type { TrustedView } from "./adminMcpTrusted.tsx";
import type { EngineUptimeResponse } from "./engineUptime.ts";
import type {
  CompleteAnswer,
  EngineObjectRow,
  EngineRow,
  IngestCandidate,
  IngestJob,
  IngestSearchAnswer,
  IngestSearchRequest,
  IngestVersion,
  ResolvedSource,
} from "./engineTypes.ts";

// Where the caller only reads optional fields, a null body means "empty success".
const api = (path: string): Promise<never> => rawApi(path).then((r) => (r ?? {}) as never);
// Forwards exactly the arguments it was given: the dom tests pin the call shape, and a DELETE has no body.
const apiJSON = (...args: Parameters<typeof rawApiJSON>): Promise<never> =>
  rawApiJSON(...args).then((r) => (r ?? {}) as never);

/** A reply with nothing to read on success: only its `error` tells the outcomes apart. */
export type Ack = { error?: undefined };

// --- Tenants, pool and engine probes -------------------------------------------------------
export type TenantsReply = { tenants?: Tenant[]; super_admin?: boolean; deployment_warnings?: string[] };
export const getAdminTenants = (): Promise<Reply<TenantsReply> | null> => rawApi("api/admin/tenants");
export const getEc2Pool = (): Promise<Reply<{ runtime?: string }> | null> => rawApi("api/admin/ec2-pool");

// --- Brand ----------------------------------------------------------------------------------
export const getBrand = (): Promise<Reply<BrandStatus> | null> => rawApi("api/admin/brand");
export const putBrand = (body: { color: string; label: string }): Promise<Reply<BrandStatus> | null> =>
  rawApiJSON("api/admin/brand", "PUT", body);
export const deleteBrand = (): Promise<Reply<BrandStatus> | null> => rawApiJSON("api/admin/brand", "DELETE");

// --- Text to speech --------------------------------------------------------------------------
export type TtsStatus = {
  managed?: boolean;
  enabled?: boolean;
  mode?: string;
  engine?: { ready?: boolean; state?: string; error?: string; [k: string]: unknown };
  polly?: { ready?: boolean; [k: string]: unknown };
  dict?: string;
  [k: string]: unknown;
};
export const getTts = (): Promise<Reply<TtsStatus> | null> => rawApi("api/admin/tts");
export const putTtsMode = (mode: string): Promise<Reply<TtsStatus> | null> =>
  rawApiJSON("api/admin/tts", "PUT", { mode });
export const putTtsDict = (dict: string): Promise<Reply<TtsStatus> | null> =>
  rawApiJSON("api/admin/tts/dict", "PUT", { dict });

// --- Dev IdP, trusted MCP hosts ---------------------------------------------------------------
export const getDevIdp = (): Promise<Reply<DevIdpStatus> | null> => rawApi("api/admin/dev-idp");
export const putDevIdp = (enabled: boolean): Promise<Reply<DevIdpStatus> | null> =>
  rawApiJSON("api/admin/dev-idp", "PUT", { enabled });

const MCP_TRUSTED = "api/admin/mcp-oauth/trusted-hosts";
export const getMcpTrusted = (): Promise<Reply<TrustedView> | null> => rawApi(MCP_TRUSTED);
export const putMcpTrusted = (body: unknown): Promise<Reply<TrustedView> | null> =>
  rawApiJSON(MCP_TRUSTED, "PUT", body);
export const deleteMcpTrusted = (): Promise<Reply<TrustedView> | null> => rawApiJSON(MCP_TRUSTED, "DELETE");

// --- Model route policy (read; its parsers take the raw body) --------------------------------
export type RoutePolicyBody = Record<string, unknown>;
export const getRoutePolicy = (): Promise<Reply<RoutePolicyBody> | null> => rawApi("api/admin/route-policy");
export const getRoutePolicyTenantCounts = (): Promise<Reply<RoutePolicyBody> | null> =>
  rawApi("api/admin/route-policy/tenant-counts");

// --- Egress writes (the reads are in ../api.ts) ----------------------------------------------
export const putEgressMode = (enforce: boolean): Promise<Reply<Ack>> =>
  apiJSON("api/admin/egress/mode", "PUT", { enforce });
export const addEgressEntry = (entry: string, reason: string): Promise<Reply<Ack>> =>
  apiJSON("api/admin/egress/allowlist", "POST", { entry, reason });
export const setEgressEntryState = (id: string, state: string): Promise<Reply<Ack>> =>
  apiJSON("api/admin/egress/allowlist/" + encodeURIComponent(id) + "/state", "POST", { state });

// --- Engines: list, per-engine settings, tokens ------------------------------------------------
export type EnginesReply = {
  engines?: EngineRow[];
  super_admin?: boolean;
  catalog_sources?: unknown;
  comfy_lan?: ComfyLanStatus | null;
};
export const getEngines = (): Promise<Reply<EnginesReply> | null> => rawApi("api/admin/engines");
export const getEngineHourly = (engineKey: string, query: string): Promise<Reply<EngineUptimeResponse> | null> =>
  rawApi(`api/admin/engines/${encodeURIComponent(engineKey)}/hourly?${query}`);

/** The engine row fields a per-engine write answers, merged over the row on screen. */
export type EnginePatch = Partial<EngineRow>;
export const putEngineMode = (key: string, mode: string): Promise<Reply<EnginePatch>> =>
  apiJSON("api/admin/engines/" + encodeURIComponent(key), "PUT", { mode });
export const replaceEngineBox = (key: string): Promise<Reply<EnginePatch>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/replace-box`, "POST", {});
export const putEngineClass = (key: string, cls: string): Promise<Reply<EnginePatch>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/class`, "PUT", { class: cls });
export const putEngineSpot = (key: string, spot: boolean): Promise<Reply<EnginePatch>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/spot`, "PUT", { spot });
export const putEngineNegative = (key: string, negative: string): Promise<Reply<EnginePatch>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/negative`, "PUT", { negative });
export const putEngineIdle = (key: string, idleSecs: number): Promise<Reply<EnginePatch>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/idle`, "PUT", { idle_secs: idleSecs });
export const putEngineModelWindow = (
  key: string,
  modelId: string,
  body: { context_tokens: number; max_output_tokens: number },
): Promise<Reply<Ack>> => apiJSON(`api/admin/engines/${encodeURIComponent(key)}/models/${encodeURIComponent(modelId)}`, "PUT", body);
export const discoverEngine = (key: string): Promise<Reply<EngineDiscoverResult>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/discover`, "POST", {});

export const issueEngineToken = (tenantSlug: string, userKey: string): Promise<Reply<IssuedEngineToken> | null> =>
  rawApiJSON("api/admin/engines/issue-token", "POST", { tenant_slug: tenantSlug, user_key: userKey });

export const getHfToken = (): Promise<Reply<HfTokenStatus>> => api("api/admin/engines/hf-token");
export const putHfToken = (token: string): Promise<Reply<HfTokenStatus>> =>
  apiJSON("api/admin/engines/hf-token", "PUT", { token });
export const deleteHfToken = (): Promise<Reply<HfTokenStatus>> => apiJSON("api/admin/engines/hf-token", "DELETE");
export const getCivitaiToken = (): Promise<Reply<CivitaiTokenStatus>> => api("api/admin/engines/civitai-token");
export const putCivitaiToken = (token: string): Promise<Reply<CivitaiTokenStatus>> =>
  apiJSON("api/admin/engines/civitai-token", "PUT", { token });
export const deleteCivitaiToken = (): Promise<Reply<CivitaiTokenStatus>> =>
  apiJSON("api/admin/engines/civitai-token", "DELETE");
export const getCivitaiRed = (): Promise<Reply<CivitaiRedStatus>> => api("api/admin/engines/civitai-red");
export const putCivitaiRed = (enabled: boolean): Promise<Reply<CivitaiRedStatus>> =>
  apiJSON("api/admin/engines/civitai-red", "PUT", { enabled });

/** The LAN ComfyUI panel: PUT saves, DELETE forgets, both answer the new status. */
export const sendComfyLan = (method: "PUT" | "DELETE", body?: Record<string, unknown>): Promise<Reply<ComfyLanStatus>> =>
  apiJSON("api/admin/engines/comfy-lan", method, body);

// --- Engines: catalogue search and ingest ------------------------------------------------------
export const searchEngineCatalog = (
  kind: string,
  body: Record<string, unknown>,
): Promise<Reply<IngestSearchAnswer>> => apiJSON(`api/admin/engines/search?kind=${encodeURIComponent(kind)}`, "POST", body);
export const searchEngineIngest = (key: string, body: IngestSearchRequest): Promise<Reply<IngestSearchAnswer>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/ingest/search`, "POST", body);
export const getIngestFiles = (
  key: string,
  source: unknown,
): Promise<Reply<{ files?: IngestCandidate[]; [k: string]: unknown }>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/ingest/files`, "POST", { source });
export const getIngestVersions = (
  key: string,
  body: Record<string, unknown>,
): Promise<Reply<{ versions?: IngestVersion[]; [k: string]: unknown }>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/ingest/versions`, "POST", body);
export const resolveIngest = (key: string, body: Record<string, unknown>): Promise<Reply<ResolvedSource>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/ingest/resolve`, "POST", body);
export const startIngest = (key: string, body: Record<string, unknown>): Promise<Reply<IngestJob>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/ingest`, "POST", body);
export const dismissIngestJob = (key: string, id: string): Promise<Reply<Ack>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/ingest/${encodeURIComponent(id)}`, "DELETE");

// --- Engines: bucket ledger and registered models ----------------------------------------------
export type ObjectsReply = { objects?: EngineObjectRow[]; checked_at?: string };
/** `path` is the ledger route from `objectsPath` (adminEngineAddShared). */
export const getEngineObjects = (path: string): Promise<Reply<ObjectsReply>> => api(path);
export const registerEngineObject = (
  path: string,
  key: string,
): Promise<Reply<{ model_id?: string; complete?: CompleteAnswer }>> =>
  apiJSON(path + "/register", "POST", { key });
export const deleteEngineObject = (path: string, key: string): Promise<Reply<Ack>> =>
  apiJSON(path, "DELETE", { key });
export const callEngineModel = (
  key: string,
  modelId: string,
  method: string,
  body: Record<string, unknown> | undefined,
  purgeBytes: boolean,
): Promise<Reply<Ack>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/models/${encodeURIComponent(modelId)}${purgeBytes ? "?purge=1" : ""}`, method, body);
export const completeEngineModel = (
  key: string,
  modelId: string,
  body: Record<string, unknown>,
): Promise<Reply<CompleteAnswer>> => apiJSON(`api/admin/engines/${encodeURIComponent(key)}/models/${encodeURIComponent(modelId)}/complete`, "POST", body);
export const fillEngineModelMeta = (key: string, modelId: string): Promise<Reply<Ack>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/models/${encodeURIComponent(modelId)}/meta`, "POST");
export const addEngineModel = (key: string, body: Record<string, unknown>): Promise<Reply<{ model_id?: string }>> =>
  apiJSON(`api/admin/engines/${encodeURIComponent(key)}/models`, "POST", body);

// --- Provided routes (deployment source) ------------------------------------------------------
// The read goes through useProvided in the tenant area, which takes the path; only the write is here.
export const putProvidedDeployment = (body: unknown): Promise<Reply<{ warnings?: string[] }>> =>
  apiJSON("api/admin/provided-routes/deployment", "PUT", body);
