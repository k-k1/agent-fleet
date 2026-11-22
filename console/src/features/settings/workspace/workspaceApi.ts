// workspaceApi — typed wrappers for the endpoints the Workspace settings screens call that are
// not already covered by ssmApi / backupApi. The paths stay string literals
// (control-plane/console_routes_test.go reads them as text); a caller gets `Reply<T>` and tells
// the outcomes apart with `isOk` or `res.error`.
//
// api() can resolve with a JSON `null` body. Every wrapper keeps `| null` in its type and the
// caller keeps the null handling it had (`!res || res.error`, `res && res.error`, `res?.error`);
// none of these callers stores a reply before reading it, so no `assertBody` is needed here.
import { api as rawApi, apiJSON as rawApiJSON, type ApiError } from "../../../core/api/client.ts";
import type { ErrorReply } from "../api.ts";
import type { WsMachine } from "../../../lib/machine.ts";
import type { WsSizingView } from "./boost.ts";
import type { DBPayload } from "./DatabaseTab.tsx";
import type { ToolchainInstall } from "./EnvTab.tsx";
import type { HomeStatus } from "./HomeBreakdownSection.tsx";
import type { Blob, Entry } from "./InternalRepoBrowser.tsx";
import type { InternalRepo } from "./InternalReposTab.tsx";
import type { AgeFolder } from "./AfWorkAgeSection.tsx";
import type { CleanupUsage, LeftoverRow } from "./MachineTab.tsx";
import type { GcpProfile } from "./GcpTab.tsx";
import type { ExistingGcpProfile } from "./gcloudConfigImport.ts";
import type { ToolCacheRow } from "./toolCaches.ts";

/** `Reply<T>` with `error` readable on the success arm too: these callers test `res.error` before
 *  anything else, and a truthy `error` narrows the union to the failure. */
export type Rep<T> = (T & { error?: undefined }) | ErrorReply;

/** Throws on a null body: the old `res.field` read that landed in the caller's catch (or, where
 *  there was none, surfaced as an unhandled rejection). Call it right after the `res.error` test. */
export function assertBody<V>(r: V | null): asserts r is V {
  if (r === null) throw new TypeError("empty reply body");
}

/** Nothing to read on success: only `error` tells the outcomes apart. */
export type Ack = { error?: undefined };

// --- Machine and cleanup ------------------------------------------------------------------------
export const getWorkspaceMachine = (): Promise<Rep<WsMachine> | null> => rawApi("api/workspace/machine");
export const getCleanupUsage = (): Promise<Rep<CleanupUsage> | null> => rawApi("api/cleanup/usage");

export type LeftoversReply = { kinds?: LeftoverRow[]; unsupported?: boolean; truncated?: boolean };
export const getCleanupLeftovers = (): Promise<Rep<LeftoversReply> | null> => rawApi("api/cleanup/leftovers");
export type LeftoverDeleted = { count?: number; bytes?: number; failed?: number };
export const deleteCleanupLeftover = (kind: string, confirm: boolean): Promise<Rep<LeftoverDeleted> | null> =>
  rawApi("api/cleanup/leftovers/" + encodeURIComponent(kind) + (confirm ? "?confirm=1" : ""), { method: "DELETE" });

export type ToolCachesReply = { caches?: ToolCacheRow[]; truncated?: boolean };
export const getToolCaches = (): Promise<Rep<ToolCachesReply> | null> => rawApi("api/cleanup/tool-caches");
export type ToolCacheEmptied = { bytes?: number };
export const deleteToolCache = (name: string): Promise<Rep<ToolCacheEmptied> | null> =>
  rawApi("api/cleanup/tool-caches/" + encodeURIComponent(name), { method: "DELETE" });

export type AfWorkListReply = { folders?: AgeFolder[]; unsupported?: boolean; truncated?: boolean };
export const getAfWorkOlderThan = (days: number): Promise<Rep<AfWorkListReply> | null> =>
  rawApi("api/cleanup/af-work?older_than_days=" + days);
export type AfWorkRemoved = { removed?: AgeFolder[]; skipped?: unknown[]; failed?: number };
export const deleteAfWorkFolders = (olderThanDays: number, names: string[]): Promise<Rep<AfWorkRemoved> | null> =>
  rawApiJSON("api/cleanup/af-work", "POST", { older_than_days: olderThanDays, names });

/** GET reads the last walk; the POST (`init` given) starts a new one. Both answer a HomeStatus. */
export const getHomeUsage = (init?: RequestInit): Promise<Rep<HomeStatus> | null> =>
  rawApi(init ? "api/cleanup/home/measure" : "api/cleanup/home", init);

// --- Boost ----------------------------------------------------------------------------------------
export const getWorkspaceSizing = (): Promise<Rep<WsSizingView> | null> => rawApi("api/workspace/sizing");
export const pressBoost = (hours: number): Promise<Rep<Ack> | null> =>
  rawApiJSON("api/workspace/boost", "POST", { hours });
export const cancelBoost = (): Promise<Rep<Ack> | null> => rawApi("api/workspace/boost", { method: "DELETE" });

// --- Databases --------------------------------------------------------------------------------------
export const getDatabases = (): Promise<Rep<DBPayload> | null> => rawApi("api/env/databases");
/** `query` is the already-encoded query string without the `?` (empty for none). */
export const postDatabaseAction = (engine: string, action: string, query = ""): Promise<Rep<Ack> | null> =>
  rawApiJSON(`api/env/databases/${engine}/${action}${query ? "?" + query : ""}`, "POST", {});
export type CreatedSession = { name: string };
export const createShellSession = (initialPrompt: string, title: string): Promise<Rep<CreatedSession> | null> =>
  rawApiJSON("api/sessions", "POST", { kind: "shell", initial_prompt: initialPrompt, title });

// --- Internal git -----------------------------------------------------------------------------------
export type InternalRepoList = { repos?: InternalRepo[] };
export const listInternalRepos = (): Promise<Rep<InternalRepoList> | null> => rawApi("api/internal-git/repos");
export const createInternalRepo = (name: string): Promise<Rep<{ name: string }> | null> =>
  rawApiJSON("api/internal-git/repos", "POST", { name });
export const deleteInternalRepo = (name: string): Promise<Rep<Ack> | null> =>
  rawApi(`api/internal-git/repos/${encodeURIComponent(name)}`, { method: "DELETE" });
export const renameInternalRepo = (oldName: string, newName: string): Promise<Rep<{ name: string }> | null> =>
  rawApiJSON(`api/internal-git/repos/${encodeURIComponent(oldName)}/rename`, "POST", { new_name: newName });

export type RepoBranches = { branches?: string[]; default_branch?: string };
export const getRepoBranches = (repo: string): Promise<Rep<RepoBranches> | null> =>
  rawApi(`api/internal-git/repos/${encodeURIComponent(repo)}/branches`);
export const getRepoTree = (repo: string, ref: string, path: string): Promise<Rep<{ entries?: Entry[] }> | null> =>
  rawApi(
    `api/internal-git/repos/${encodeURIComponent(repo)}/tree?ref=${encodeURIComponent(ref)}&path=${encodeURIComponent(path)}`,
  );
export const getRepoBlob = (repo: string, ref: string, path: string): Promise<Rep<Blob> | null> =>
  rawApi(
    `api/internal-git/repos/${encodeURIComponent(repo)}/blob?ref=${encodeURIComponent(ref)}&path=${encodeURIComponent(path)}`,
  );

// --- Toolchains and host update ------------------------------------------------------------------------
export const applyHostUpdate = (): Promise<Rep<Ack> | null> => rawApiJSON("api/update/apply", "POST", {});

/** What the version watcher answers for a major (`endpoint` is the toolchain's own route). */
export type LatestPatch = { current?: string; latest?: string; update_available?: boolean; error?: ApiError };
export const getToolchainLatest = (endpoint: string, major: string): Promise<LatestPatch | null> =>
  rawApi(`api/env/${endpoint}?major=${encodeURIComponent(major)}`);

/** An install job. An API failure carries `error` as an object; the installer's own failure is
 *  `state: "error"` with `error` a plain string. */
export type InstallJob = { state?: string; major?: string; error?: string | ApiError };
export const startJdkInstall = (major: string): Promise<InstallJob | null> =>
  rawApiJSON("api/env/jdk-install", "POST", { major });
export const getJdkInstall = (): Promise<InstallJob | null> => rawApi("api/env/jdk-install");
export const startNodeInstall = (major: string): Promise<InstallJob | null> =>
  rawApiJSON("api/env/node-install", "POST", { major });
export const getNodeInstall = (): Promise<InstallJob | null> => rawApi("api/env/node-install");

export type ToolchainInstalls = { java: ToolchainInstall[]; node: ToolchainInstall[] };
export const getToolchainInstalls = (): Promise<Rep<ToolchainInstalls> | null> =>
  rawApi("api/env/toolchain-installs");
/** `kind` is the route's own word: "jdk" or "node". Answers the refreshed list. */
export const deleteToolchain = (kind: "jdk" | "node", major: string): Promise<Rep<ToolchainInstalls> | null> =>
  rawApiJSON(`api/env/${kind}/${encodeURIComponent(major)}`, "DELETE");

// --- Cloud profiles --------------------------------------------------------------------------------------
export type GcpLoginStates = {
  profiles?: { name?: unknown; state?: unknown; account?: unknown }[];
  error?: ApiError;
};
export const getGcpLoginStates = (): Promise<GcpLoginStates | null> => rawApi("api/gcp-login/profiles");
export const listGcpProfileRows = (): Promise<Rep<GcpProfile[]> | null> => rawApi("api/gcp/profiles");
export const listExistingGcpProfiles = (): Promise<Rep<ExistingGcpProfile[]> | null> =>
  rawApi("api/gcp/profiles");
