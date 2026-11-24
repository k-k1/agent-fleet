// features/settings/memory/memoryApi — typed wrappers for the Agent-memory endpoints the
// Memory tab calls. The paths stay string literals (control-plane/console_routes_test.go reads
// them as text); a caller gets `Reply<T>` and tells the two outcomes apart with `isOk`.
//
// Each write repeats its target id in the query as well as the body: the CP audit ledger reads
// the URL only, never the body. Keep both when adding a write.
import { api as rawApi, apiJSON as rawApiJSON } from "../../../core/api/client.ts";
import type { ApiError } from "../../../core/api/client.ts";
import type { Reply } from "../api.ts";
import type {
  AutoSyncPreview,
  AutoSyncStatus,
  BundlePreview,
  BundleResult,
  ChangeDiff,
  ClaudeExportPreview,
  ClaudeExportResult,
  ClaudeExportSource,
  ClaudeImportPreview,
  ClaudeImportResult,
  ClaudeImportSource,
  ConsolProposal,
  ConsolProposalView,
  ConsolStatus,
  LintReport,
  MemoryChange,
  MemoryListed,
  MovePreview,
  MoveResultItem,
  MuseImportPreview,
  MuseImportSource,
  ProjectRow,
  PromoteCandidates,
  PromoteProposal,
  PurgePreview,
  RootsPayload,
  RunRevertReport,
  SecretFinding,
  Snapshot,
  TreeKind,
  TreeProject,
} from "./memoryTypes.ts";

// A JSON `null` body is not an error, and it is not an empty object either. Where the caller
// only reads optional fields (lists, acks, extra refusal data) it meant "empty success", so those
// wrappers fold it to {} here. Where the caller needs a whole object (a preview, a status, a
// commit result) the answer is unknown, so the wrapper keeps null and says so in its type: the
// caller leaves its "not loaded" / "no result" state alone, as it did before the typed wrappers.
const api = (path: string): Promise<never> => rawApi(path).then((r) => (r ?? {}) as never);
const apiJSON = (path: string, method: string, body?: unknown, headers?: Record<string, string>): Promise<never> =>
  // Without headers the call keeps its three arguments: the wrapper's tests pin them exactly.
  (headers ? rawApiJSON(path, method, body, headers) : rawApiJSON(path, method, body)).then((r) => (r ?? {}) as never);

/** A reply whose refusals also carry data (secret findings, what a broken-off move did): one
 *  object with an optional `error`, read with `if (res.error)` instead of `isOk`. */
export type WithError<X> = Partial<X> & { error?: ApiError };

/** A reply with nothing to read on success: only its `error` tells the outcomes apart. */
export type Ack = { error?: undefined };

/** What `committed` writes report: the paths written and removed, when anything changed. */
export type CommitReply = { committed?: boolean; written?: string[]; deleted?: string[] };

// --- Snapshots and restore -----------------------------------------------------------------
export type SnapshotsReply = { snapshots?: Snapshot[] };
export type TreeReply = { kinds?: TreeKind[]; projects?: TreeProject[] };

export const getMemoryRoots = (): Promise<Reply<RootsPayload> | null> => rawApi("api/agents/memory/roots");
export const getMemorySnapshots = (limit: number, before?: string): Promise<Reply<SnapshotsReply>> =>
  api(
    before === undefined
      ? "api/agents/memory/snapshots?limit=" + limit
      : "api/agents/memory/snapshots?limit=" + limit + "&before=" + encodeURIComponent(before),
  );
export const getMemoryDiff = (to: string): Promise<Reply<{ diff?: string }>> =>
  api("api/agents/memory/diff?to=" + encodeURIComponent(to));
export const getMemoryTree = (rev: string): Promise<Reply<TreeReply> | null> =>
  rawApi("api/agents/memory/tree?rev=" + encodeURIComponent(rev));
export const putMemorySettings = (auto: boolean): Promise<Reply<{ auto?: boolean; autoLocked?: boolean }> | null> =>
  rawApiJSON("api/agents/memory/settings", "PUT", { auto });
export const takeMemorySnapshot = (): Promise<Reply<{ committed?: boolean }> | null> =>
  rawApiJSON("api/agents/memory/snapshots", "POST", { trigger: "manual" });
export const restoreMemory = (
  rev: string,
  body: { rev: string; scope: { all?: boolean; kinds?: string[]; projects?: string[] } },
): Promise<Reply<CommitReply> | null> =>
  rawApiJSON("api/agents/memory/restore?rev=" + encodeURIComponent(rev), "POST", body);

// --- Transfer (import/export of the whole store) -------------------------------------------
export const applyMemoryImport = (
  importId: string,
  body: { importId: string; mode: "replace" | "migrate"; scope: { projects: string[]; kinds: string[] } },
): Promise<Reply<CommitReply & { adopted?: boolean }> | null> =>
  rawApiJSON("api/agents/memory/import/apply?importId=" + encodeURIComponent(importId), "POST", body);

/** Fleet-wide codex memories switch; the write lives in codex's own config through /codex/settings. */
export const putCodexMemories = (memories: boolean): Promise<Reply<Ack>> =>
  apiJSON("api/codex/settings", "PUT", { memories });

// --- Auto-sync -----------------------------------------------------------------------------
export const getAutoSync = (): Promise<Reply<AutoSyncStatus> | null> => rawApi("api/agents/memory/auto-sync");
export const getAutoSyncPreview = (to: "on" | "off"): Promise<Reply<AutoSyncPreview> | null> =>
  rawApi("api/agents/memory/auto-sync/preview?to=" + to);
export const wakeAutoSync = (to: "on" | "off"): Promise<Reply<Ack>> =>
  apiJSON("api/agents/memory/auto-sync?action=wake&to=" + to, "POST", { action: "wake" });
export const actAutoSync = (action: "retry" | "abandon"): Promise<Reply<Ack>> =>
  apiJSON("api/agents/memory/auto-sync?action=" + action, "POST", { action });

// --- Entries: list, changes, diff, revert, purge, lint, project move ------------------------
export type ChangesReply = { changes?: MemoryChange[]; next?: string; withheld?: number };

export const listMemoryEntries = (): Promise<Reply<{ entries?: MemoryListed[] }>> =>
  api("api/agents/memory/entries/list");
export const getMemoryChanges = (limit: number, before?: string): Promise<Reply<ChangesReply>> =>
  api(
    before === undefined
      ? `api/agents/memory/entries/changes?limit=${limit}`
      : `api/agents/memory/entries/changes?limit=${limit}&before=${encodeURIComponent(before)}`,
  );
export const getMemoryChangeDiff = (commit: string): Promise<Reply<ChangeDiff> | null> =>
  rawApi("api/agents/memory/entries/diff?commit=" + encodeURIComponent(commit));
export const revertMemoryChange = (
  commit: string,
  forget: boolean,
  ack: boolean,
): Promise<WithError<{ findings: SecretFinding[] }>> =>
  apiJSON("api/agents/memory/entries/revert?commit=" + encodeURIComponent(commit), "POST", { commit, forget, ack });
export const revertReviewRun = (run: string): Promise<Reply<RunRevertReport> | null> =>
  rawApiJSON("api/agents/memory-review/runs/revert?run=" + encodeURIComponent(run), "POST", { run });
export const getPurgePreview = (
  scope: string,
  project: string,
  name: string,
): Promise<Reply<Partial<PurgePreview>>> =>
  api(
    `api/agents/memory/entries/purge-preview?scope=${scope}&project=${encodeURIComponent(project)}&name=${encodeURIComponent(name)}`,
  );
export const purgeMemory = (
  project: string,
  body: { scope: string; project: string; name: string; plan: string; confirm: string },
): Promise<Reply<Ack>> =>
  apiJSON("api/agents/memory/entries/purge" + (project ? "?project=" + encodeURIComponent(project) : ""), "POST", body);

export const getMemoryProjects = (): Promise<Reply<{ projects?: ProjectRow[] }>> =>
  api("api/agents/memory/entries/projects");
export const getProjectMovePreview = (query: string): Promise<Reply<MovePreview> | null> =>
  rawApi("api/agents/memory/entries/project-move-preview?" + query);
/** A move that broke off still reports what it did: `results` and the `pending` / `stopped` names ride on the error. */
export type MoveReply = { results?: MoveResultItem[]; pending?: string[]; stopped?: string };
export const moveMemoryProject = (
  from: string,
  body: { from: string; to: string; collision: string; items: unknown[] },
): Promise<WithError<MoveReply>> =>
  apiJSON("api/agents/memory/entries/project-move?project=" + encodeURIComponent(from), "POST", body);

export type LintTerms = { terms?: string[]; rejected?: string[] };
export const getLintTerms = (): Promise<Reply<LintTerms>> => api("api/agents/memory/entries/lint-terms");
export const putLintTerms = (body: { terms: string[]; rejected?: string[] }): Promise<Reply<LintTerms>> =>
  apiJSON("api/agents/memory/entries/lint-terms", "POST", body);
export const getMemoryLint = (query: string): Promise<Reply<Partial<LintReport>>> =>
  api("api/agents/memory/entries/lint?" + query);

// A memory superseded by a document: its repairs, and the promotion that leads to it. Each repair
// quotes the revision the report showed, so a memory that moved meanwhile is refused.
export type DocRepair = { project: string; name: string; revision: number; path?: string; heading?: string; commit?: string };
export const retargetDocument = (body: DocRepair): Promise<Reply<{ revision?: number }>> =>
  apiJSON("api/agents/memory/entries/document-retarget?project=" + encodeURIComponent(body.project), "POST", body);
export const restoreDocument = (body: DocRepair): Promise<Reply<{ revision?: number }>> =>
  apiJSON("api/agents/memory/entries/document-restore?project=" + encodeURIComponent(body.project), "POST", body);
export const getPromoteCandidates = (project: string): Promise<Reply<Partial<PromoteCandidates>>> =>
  api("api/agents/memory/entries/promotion-candidates?project=" + encodeURIComponent(project));
export const proposePromotion = (body: { project: string; name: string }): Promise<Reply<PromoteProposal>> =>
  apiJSON("api/agents/memory/entries/promotion-proposal?project=" + encodeURIComponent(body.project), "POST", body);
export const supersedeByDocument = (body: {
  project: string;
  name: string;
  revision: number;
  path: string;
  heading: string;
  commit: string;
  bodySha256: string;
}): Promise<Reply<{ revision?: number }>> =>
  apiJSON("api/agents/memory/entries/supersede-document?project=" + encodeURIComponent(body.project), "POST", body);

// --- Claude / Muse / bundle import and Claude export ----------------------------------------
export const getClaudeImportSources = (): Promise<Reply<{ sources?: ClaudeImportSource[]; withheld?: number }>> =>
  api("api/agents/memory/claude-import");
export const getClaudeImportPreview = (slug: string): Promise<Reply<ClaudeImportPreview> | null> =>
  rawApi("api/agents/memory/claude-import/preview?slug=" + encodeURIComponent(slug));
export const applyClaudeImport = (
  project: string,
  body: { project: string; slug: string; items: unknown[] },
): Promise<Reply<{ results?: ClaudeImportResult[] }>> =>
  apiJSON("api/agents/memory/claude-import?project=" + encodeURIComponent(project), "POST", body);

export const getMuseImportSources = (): Promise<Reply<{ sources?: MuseImportSource[]; withheld?: number }>> =>
  api("api/agents/memory/muse-import");
export const getMuseImportPreview = (query: string): Promise<Reply<MuseImportPreview> | null> =>
  rawApi("api/agents/memory/muse-import/preview?" + query);
export const applyMuseImport = (
  project: string,
  body: Record<string, unknown>,
): Promise<Reply<{ results?: ClaudeImportResult[] }>> =>
  apiJSON("api/agents/memory/muse-import?project=" + encodeURIComponent(project), "POST", body);

export const getBundlePreview = (importId: string): Promise<Reply<BundlePreview> | null> =>
  rawApi("api/agents/memory/af-import/preview?importId=" + encodeURIComponent(importId));
export const applyBundleImport = (
  importId: string,
  body: { importId: string; items: unknown[] },
): Promise<Reply<{ results?: BundleResult[] }>> =>
  apiJSON("api/agents/memory/af-import?importId=" + encodeURIComponent(importId), "POST", body);

export const getClaudeExportSources = (): Promise<Reply<{ projects?: ClaudeExportSource[] }>> =>
  api("api/agents/memory/claude-export");
export const getClaudeExportPreview = (project: string): Promise<Reply<ClaudeExportPreview> | null> =>
  rawApi("api/agents/memory/claude-export/preview?project=" + encodeURIComponent(project));
export const applyClaudeExport = (
  project: string,
  body: { project: string; token: string; overwrite: string[] },
): Promise<Reply<{ results?: ClaudeExportResult[]; index?: string; indexKept?: string }>> =>
  apiJSON("api/agents/memory/claude-export?project=" + encodeURIComponent(project), "POST", body);

// --- Consolidation proposals ---------------------------------------------------------------
const CONSOL = "api/agents/memory-consolidation";
export const getConsolStatus = (): Promise<Reply<ConsolStatus> | null> => rawApi(CONSOL);
export const getConsolProposals = (): Promise<Reply<{ proposals?: ConsolProposal[] }>> =>
  api(`${CONSOL}/proposals`);
export const getConsolProposal = (id: string): Promise<Reply<ConsolProposalView> | null> =>
  rawApi(`${CONSOL}/proposal?id=${encodeURIComponent(id)}`);
// `tenant` is the workspace the member confirmed the run for, sent as X-AF-Tenant: the global fetch
// wrapper only fills the header in when the request has none, so without it the write would go to
// whichever tenant is selected when the request leaves.
export const runConsolidation = (all: boolean, tenant: string): Promise<Reply<Ack>> =>
  apiJSON(`${CONSOL}/run`, "POST", all ? { all: true } : {}, tenant ? { "X-AF-Tenant": tenant } : undefined);
export const decideConsolProposal = (verb: "approve" | "reject" | "retry", id: string): Promise<Reply<Ack>> =>
  apiJSON(`${CONSOL}/proposals/${verb}?proposal=${encodeURIComponent(id)}`, "POST", { id });
