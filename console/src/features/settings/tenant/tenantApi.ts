// features/settings/tenant/tenantApi — typed wrappers for the endpoints the tenant screens call.
// The paths stay string literals (control-plane/console_routes_test.go reads them as text); a
// caller gets `Reply<T>` and tells the two outcomes apart with `isOk` or `res.error`.
//
// A JSON `null` body is neither an error nor an empty object. A wrapper whose caller guarded the
// reply (`res?.error`, `d && !d.error`) keeps `| null` in its type and the caller keeps that
// handling. A caller that dereferenced the reply inside a try/catch (so a null body surfaced as
// its "failed" message, after it had already stored the null) does the same: store the reply,
// then `assertBody` to throw into that catch. getTenantMembers has no state to store first and
// rejects on null through `needBody`.
import { api as rawApi, apiJSON as rawApiJSON, type ApiError } from "../../../core/api/client.ts";
import type { Reply } from "../api.ts";
import type { HomeResize, Member } from "../parts/adminShared.ts";
import type { Session } from "../../../types/session.ts";
import type { MemberStats } from "../api.ts";
import type { TenantBranchRules } from "./tenantBranchRules.tsx";
import type { EngineAccessView } from "./tenantEngineAccess.tsx";
import type { NetworkView } from "./tenantNetwork.tsx";
import type { MachineView } from "./tenantMachine.tsx";
import type { BoostPolicy } from "./tenantBoost.tsx";
import type { HomeBackupsView } from "./tenantMemberDetail.tsx";
import type { TenantIdP } from "./tenantLoginTypes.ts";
import type { DeployProvider } from "./tenantLoginRules.tsx";
import type { GitOAuthApp } from "./tenantGitOAuth.tsx";
import type { TenantMemberDefaultsView } from "./tenantMemberDefaults.tsx";
import type { BulkReply, ForceStopReply, PoolBudget, PoolStatus } from "./ec2Pool.tsx";
import type { RouteRule } from "./routePolicyRule.ts";

/** A reply with nothing to read on success: only its `error` tells the outcomes apart. */
export type Ack = { error?: undefined };

const needBody = <T>(p: Promise<T | null>): Promise<T> =>
  p.then((r) => {
    if (r === null) throw new TypeError("empty reply body");
    return r;
  });

/** A body the caller only parses (parseTenantPolicy, parseProvidedTenant take `unknown`) and reads `error` from. */
export type Loose = Record<string, unknown> & { error?: ApiError };

/** Throws on a null body: the old `res.field` read that landed in the caller's catch. */
export function assertBody<V>(r: V | null): asserts r is V {
  if (r === null) throw new TypeError("empty reply body");
}

const T = (slug: string) => encodeURIComponent(slug);

// --- Tenant roster and the per-tenant switches -------------------------------------------------
export const getTenantMembers = (slug: string): Promise<Reply<{ members?: Member[] }>> =>
  needBody(rawApi(`api/admin/tenants/${T(slug)}/members`));

export type EnabledReply = { enabled?: boolean };
export const getTenantWatchSources = (slug: string): Promise<Reply<EnabledReply> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/watch-sources`);
export const putTenantWatchSources = (slug: string, enabled: boolean): Promise<Reply<EnabledReply> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/watch-sources`, "PUT", { enabled });
export const getTenantMemoQueues = (slug: string): Promise<Reply<EnabledReply> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/memo-queues`);
export const putTenantMemoQueues = (slug: string, enabled: boolean): Promise<Reply<EnabledReply> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/memo-queues`, "PUT", { enabled });
export const getTenantExternalMemory = (slug: string): Promise<Reply<EnabledReply> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/external-memory`);
export const putTenantExternalMemory = (slug: string, enabled: boolean): Promise<Reply<EnabledReply> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/external-memory`, "PUT", { enabled });

export type McpOAuthView = { enabled: boolean; served: boolean };
export const getTenantMcpOAuth = (slug: string): Promise<Reply<McpOAuthView> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/mcp-oauth`);
export const putTenantMcpOAuth = (slug: string, enabled: boolean): Promise<Reply<McpOAuthView> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/mcp-oauth`, "PUT", { enabled });

export const getTenantBranchRules = (slug: string): Promise<Reply<TenantBranchRules> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/branch-rules`);
export const putTenantBranchRules = (slug: string, rules: unknown[]): Promise<Reply<TenantBranchRules> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/branch-rules`, "PUT", { rules });

export const getTenantEngineAccess = (slug: string): Promise<Reply<EngineAccessView> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/engine-access`);
/** `sub` is the suffix after `engine-access` (a role or a member route). */
export const putTenantEngineAccess = (slug: string, sub: string, body: object): Promise<Reply<Ack> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/engine-access` + sub, "PUT", body);

export const getTenantNetwork = (slug: string): Promise<Reply<NetworkView> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/network`);
export const putTenantNetwork = (slug: string, allowedCidrs: string): Promise<Reply<NetworkView> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/network`, "PUT", { allowed_cidrs: allowedCidrs });

export const getTenantMachine = (slug: string): Promise<Reply<MachineView> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/slot-class`);
export const putTenantMachine = (slug: string, slotClass: string): Promise<Reply<Ack> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/slot-class`, "PUT", { slot_class: slotClass });

export const getTenantBoost = (slug: string): Promise<Reply<BoostPolicy> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/boost`);
export const putTenantBoost = (slug: string, body: object): Promise<Reply<BoostPolicy> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/boost`, "PUT", body);

export const getTenantMemberDefaults = (slug: string): Promise<Reply<TenantMemberDefaultsView> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/member-defaults`);
export const putTenantMemberDefaults = (slug: string, body: unknown): Promise<Reply<TenantMemberDefaultsView> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/member-defaults`, "PUT", body);

export const getTenantGitOAuth = (slug: string): Promise<Reply<{ providers?: GitOAuthApp[] }> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/git-oauth`);
export const putGitOAuthApp = (slug: string, provider: string, body: unknown): Promise<Reply<Ack> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/git-oauth/${encodeURIComponent(provider)}`, "PUT", body);

// --- Limits, login rules, deletion -------------------------------------------------------------
export const putTenantLimits = (slug: string, body: object): Promise<Reply<{ pool_budget?: PoolBudget }> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/limits`, "PUT", body);
export const putTenantLogin = (slug: string, body: object): Promise<Reply<Ack> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/login`, "PUT", body);
export const deleteTenant = (slug: string): Promise<Reply<Ack> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}`, "DELETE", {});

// --- Sign-in methods -----------------------------------------------------------------------------
export type IdpListReply = { providers?: TenantIdP[] };
export const getTenantIdps = (slug: string): Promise<Reply<IdpListReply> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/idp`);
export const getAllIdps = (): Promise<Reply<IdpListReply> | null> => rawApi("api/admin/idp");
export const getDeploymentProviders = (): Promise<Reply<{ providers?: DeployProvider[] }> | null> =>
  rawApi("api/admin/providers");
/** Creates the method when `id` is empty, updates it otherwise. */
export const saveTenantIdp = (slug: string, id: string | undefined, body: object): Promise<Reply<Ack> | null> =>
  id
    ? rawApiJSON(`api/admin/tenants/${T(slug)}/idp/${encodeURIComponent(id)}`, "PUT", body)
    : rawApiJSON(`api/admin/tenants/${T(slug)}/idp`, "POST", body);
export const deleteTenantIdp = (slug: string, id: string): Promise<Reply<Ack> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/idp/${encodeURIComponent(id)}`, "DELETE");
/** A refusal that needs confirming carries `error.code` and the member count next to it. */
export type IdpStatusReply = { error?: ApiError; members?: number };
export const setTenantIdpStatus = (
  slug: string,
  id: string,
  status: string,
  confirm?: boolean,
): Promise<IdpStatusReply | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/idp/${encodeURIComponent(id)}/status${confirm ? "?confirm=1" : ""}`, "POST", { status });

// --- Provided routes (the caller owns the base path; see providedRoutes.ts) ---------------------
export const getProvided = (path: string): Promise<Loose | null> => rawApi(path);
export const putProvidedGrant = (path: string, body: unknown): Promise<Loose | null> =>
  rawApiJSON(`${path}/grant`, "PUT", body);
export const putProvidedPolicy = (path: string, body: unknown): Promise<Loose | null> =>
  rawApiJSON(`${path}/policy`, "PUT", body);
export const putProvidedMembers = (path: string, body: unknown): Promise<Loose | null> =>
  rawApiJSON(`${path}/members`, "PUT", body);

// --- Model route policy ---------------------------------------------------------------------------
export const getTenantRoutePolicy = (slug: string): Promise<Loose | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/route-policy`);
/** `path` is the layer's route; the caller sends the revision it loaded (If-Match). */
export const putRoutePolicyRule = (path: string, body: RouteRule, revision: number): Promise<Reply<Ack> | null> =>
  rawApiJSON(path, "PUT", body, { "If-Match": `"${revision}"` });

// --- One member --------------------------------------------------------------------------------------
export const getMemberLiveStats = (slug: string, key: string, signal: AbortSignal): Promise<Loose | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/members/${T(key)}/stats?disk=0`, { signal });
export const getMemberStats = (slug: string, key: string): Promise<Reply<MemberStats> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/members/${T(key)}/stats`);
export const getMemberSessions = (
  slug: string,
  key: string,
): Promise<Reply<{ sessions?: (Session & { started?: string })[] }> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/members/${T(key)}/sessions`);
export const getMemberHomeBackups = (slug: string, key: string): Promise<Reply<HomeBackupsView> | null> =>
  rawApi(`api/admin/tenants/${T(slug)}/members/${T(key)}/home-backups`);
export const deleteMemberHomeBackups = (slug: string, key: string): Promise<Reply<{ deleted?: number }> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/members/${T(key)}/home-backups`, "DELETE", {});
export const deleteMemberRow = (slug: string, key: string): Promise<Reply<Ack> | null> =>
  rawApiJSON(`api/admin/tenants/${T(slug)}/members/${T(key)}`, "DELETE", {});

const who = (slug: string, key: string) => ({ tenant_slug: slug, user_key: key });
export const stopMemberWorkspace = (slug: string, key: string): Promise<Reply<Ack> | null> =>
  rawApiJSON("api/admin/stop-workspace", "POST", who(slug, key));
export const rotateMemberGitToken = (slug: string, key: string): Promise<Reply<{ workspace?: string }> | null> =>
  rawApiJSON("api/admin/rotate-git-token", "POST", who(slug, key));
export const cleanMemberHome = (slug: string, key: string): Promise<Reply<{ pending?: boolean }> | null> =>
  rawApiJSON("api/admin/clean-home", "POST", who(slug, key));
export const destroyMemberWorkspace = (
  slug: string,
  key: string,
): Promise<Reply<{ pending?: boolean; leftovers?: string[] }> | null> =>
  rawApiJSON("api/admin/workspaces", "DELETE", who(slug, key));
export const removeMembership = (slug: string, key: string, purge: boolean): Promise<Reply<{ pending?: boolean }> | null> =>
  rawApiJSON("api/admin/memberships", "DELETE", { ...who(slug, key), purge });
export type UserLimitsReply = { slot_class?: unknown; slot_class_effective?: unknown; home_resize?: HomeResize | null };
export const putUserLimits = (body: object): Promise<Reply<UserLimitsReply> | null> =>
  rawApiJSON("api/admin/user-limits", "PUT", body);
export const putMembershipRole = (slug: string, key: string, role: string): Promise<Reply<Ack> | null> =>
  rawApiJSON("api/admin/membership-role", "PUT", { user_key: key, tenant_slug: slug, role });

// --- EC2 slot pool --------------------------------------------------------------------------------------
const SLOT = "api/admin/ec2-pool/slots/";
export const getEc2PoolStatus = (): Promise<Reply<PoolStatus> | null> => rawApi("api/admin/ec2-pool");
export const terminatePoolSlot = (id: string): Promise<Reply<Ack> | null> =>
  rawApi(SLOT + encodeURIComponent(id), { method: "DELETE" });
export const returnPoolSlot = (id: string, token: string | undefined): Promise<Reply<Ack> | null> =>
  rawApiJSON(SLOT + encodeURIComponent(id) + "/return", "POST", { token });
export const forcePoolSlotStop = (id: string, overrideHealthy: boolean): Promise<Reply<ForceStopReply> | null> =>
  rawApiJSON(SLOT + encodeURIComponent(id) + "/force-stop", "POST", overrideHealthy ? { override_healthy: true } : {});
export const setPoolSlotReserved = (id: string, reserve: boolean): Promise<Reply<Ack> | null> =>
  rawApi(SLOT + encodeURIComponent(id) + "/replace", { method: reserve ? "PUT" : "DELETE" });
export const reserveOutdatedSlots = (ids: string[]): Promise<Reply<BulkReply> | null> =>
  rawApiJSON("api/admin/ec2-pool/reserve-outdated", "POST", { instance_ids: ids });
