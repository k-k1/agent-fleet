// mcpApi — typed wrappers for the MCP-registry endpoints the member tab (McpTab), the tenant
// distribution view (mcpAdmin) and the egress note call. The paths stay string literals
// (control-plane/console_routes_test.go reads them as text); a caller gets `Reply<T>` and tells
// the outcomes apart with `isOk` or `res.error`. The OAuth sign-in calls have their own
// wrappers in mcpOAuthApi.ts.
//
// api() can resolve with a JSON `null` body. Each wrapper keeps `| null` in its type and the
// caller keeps the null handling it had (`d && d.error`, `d && !d.error`).
import { api, apiJSON, type ApiError } from "../../../core/api/client.ts";
import type { Reply } from "../api.ts";
import type { EgressCheck } from "./egressCheck.ts";
import type { ProbeResult, Registry, TenantServer } from "./mcpWire.ts";

/** What a save, delete or secrets write answers on success: nothing the callers read. */
export type McpAck = { error?: undefined };

/** The answer of POST api/mcp-servers/test: a probe result, or a refusal envelope (`error` an
 *  object). A probe's own failure is a ProbeResult whose `error` is a string. */
export type ProbeAnswer = ProbeResult | { error: ApiError } | null;

/** POST api/mcp-servers/tenant-refresh: the registry plus what the pull dropped. */
export type TenantRefreshReply = Registry & { fetch?: { unreadable?: number; dropped?: number } };

export const getMcpRegistry = (): Promise<Reply<Registry> | null> => api("api/mcp-servers");
/** POST when `id` is empty, PUT otherwise. */
export const saveMcpServer = (id: string, body: unknown): Promise<Reply<McpAck> | null> =>
  id ? apiJSON(`api/mcp-servers/${encodeURIComponent(id)}`, "PUT", body) : apiJSON("api/mcp-servers", "POST", body);
export const testMcpServer = (body: unknown): Promise<ProbeAnswer> => apiJSON("api/mcp-servers/test", "POST", body);
export const deleteMcpServer = (id: string): Promise<Reply<McpAck> | null> =>
  apiJSON(`api/mcp-servers/${encodeURIComponent(id)}`, "DELETE");
export const refreshTenantMcp = (): Promise<Reply<TenantRefreshReply> | null> =>
  apiJSON("api/mcp-servers/tenant-refresh", "POST", {});
export const putMcpSecrets = (id: string, headers: Record<string, string>): Promise<Reply<McpAck> | null> =>
  apiJSON(`api/mcp-servers/${encodeURIComponent(id)}/secrets`, "PUT", { headers });

// --- Tenant distribution (control-plane admin routes) --------------------------------------
export type WorkspaceScopeBundle = "read_only" | "standard";
export type WorkspaceMcpService = Record<WorkspaceScopeBundle, string[]> & {
  id: string;
  name: string;
  label: string;
  host: string;
  url: string;
  preview?: boolean;
};
export type WorkspaceMcpPreset = { redirect_uri?: string; services: WorkspaceMcpService[] };
export type WorkspaceMcpRequest = {
  tenant_slug: string;
  client_id: string;
  client_secret: string;
  services: string[];
  bundle: WorkspaceScopeBundle;
};

export const listTenantMcpServers = (slug: string): Promise<Reply<{ servers?: TenantServer[]; google_workspace?: WorkspaceMcpPreset }> | null> =>
  api("api/admin/mcp-servers?tenant=" + encodeURIComponent(slug));
export const createWorkspaceMcpServers = (body: WorkspaceMcpRequest): Promise<Reply<{ created: number }> | null> =>
  apiJSON("api/admin/mcp-servers/google-workspace", "POST", body);
/** POST when `id` is empty, PUT otherwise. */
export const saveTenantMcpServer = (id: string, body: unknown): Promise<Reply<McpAck> | null> =>
  id ? apiJSON("api/admin/mcp-servers/" + encodeURIComponent(id), "PUT", body) : apiJSON("api/admin/mcp-servers", "POST", body);
export const deleteTenantMcpServer = (id: string, slug: string): Promise<Reply<McpAck> | null> =>
  apiJSON("api/admin/mcp-servers/" + encodeURIComponent(id) + "?tenant=" + encodeURIComponent(slug), "DELETE");

// --- Egress allowlist (docs/log/48 §9) -------------------------------------------------------
/** `query` is the checkQuery() string for the hosts being asked about. */
export const checkEgress = (query: string): Promise<Reply<EgressCheck> | null> => api("api/egress/check?" + query);
export const proposeEgress = (entry: string, reason: string): Promise<Reply<McpAck> | null> =>
  apiJSON("api/egress/propose", "POST", { entry, reason });
