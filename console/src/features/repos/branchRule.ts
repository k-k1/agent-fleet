// Branch naming resolver (ADR 0103 decision 7): the Agent names branches, the Console asks.
//
// Every call answers null when there is no answer to use: an Agent from before the resolver
// (404), a working copy that is not git, a stopped Workspace, a network error. The caller then
// keeps its own fallback (branchForItem, the fixed rename chips) — a rule is advice, and a
// missing one must never block a launch or a rename.
import { api, apiJSON } from "../../core/api/client.ts";
import { tMaybe } from "../../lib/i18n/index.ts";

export interface BranchWarning {
  code: string;
  message: string;
}

export interface BranchKind {
  kind: string;
  prefix: string;
  base: string;
}

/** The item a name is rendered for. `type` is the tracker's issue type; the resolver maps it,
 * then the labels, to a kind (decision 4). */
export interface BranchItem {
  provider: string;
  key: string;
  title: string;
  type?: string;
  labels?: string[];
}

/** GET /repos/{name}/branch-rule. */
export interface BranchRule {
  repo_id: string;
  name: string;
  base: string;
  base_branch: string;
  kinds: BranchKind[];
  sources: Record<string, unknown>;
  gitflow: string;
  warnings: BranchWarning[];
}

/** POST /repos/{name}/branch-name. `base` is the policy (head / default / a branch) and
 * `base_branch` the branch it resolved to, which is what a launch sends. */
export interface BranchName {
  name: string;
  name_empty: boolean;
  base: string;
  base_branch: string;
  kind: string;
  provisional: boolean;
  warnings: BranchWarning[];
  sources: Record<string, unknown>;
}

export interface BranchNameRequest {
  item?: BranchItem;
  session?: string;
  kind?: string;
  slug?: string;
}

const repoPath = (repo: string) => `api/repos/${encodeURIComponent(repo)}`;

async function answer<T>(p: Promise<unknown>, ok: (j: Record<string, unknown>) => boolean): Promise<T | null> {
  try {
    const j = (await p) as Record<string, unknown> | null;
    if (!j || typeof j !== "object" || j.error || !ok(j)) return null;
    return j as T;
  } catch {
    return null;
  }
}

export function fetchBranchRule(repo: string, refresh = false): Promise<BranchRule | null> {
  return answer<BranchRule>(api(`${repoPath(repo)}/branch-rule${refresh ? "?refresh=1" : ""}`), (j) => Array.isArray(j.kinds));
}

export function fetchBranchName(repo: string, req: BranchNameRequest): Promise<BranchName | null> {
  return answer<BranchName>(apiJSON(`${repoPath(repo)}/branch-name`, "POST", req), (j) => typeof j.name === "string");
}

export async function checkBranchName(repo: string, name: string): Promise<BranchWarning[] | null> {
  const j = await answer<{ warnings: BranchWarning[] }>(
    apiJSON(`${repoPath(repo)}/branch-name/check`, "POST", { name }),
    (x) => Array.isArray(x.warnings),
  );
  return j ? j.warnings : null;
}

/** The work-items settings' template preview. It belongs to no working copy, so the Agent
 * renders over the user and built-in layers only. */
export async function previewBranchNames(
  template: string,
  items: BranchItem[],
): Promise<{ name: string; name_empty: boolean; kind: string }[] | null> {
  const j = await answer<{ names: { name: string; name_empty: boolean; kind: string }[] }>(
    apiJSON("api/branch-rules/preview", "POST", { template, items }),
    (x) => Array.isArray(x.names),
  );
  return j ? j.names : null;
}

/** Bitbucket's branching model had not arrived when the Agent answered (it waits up to 3 s),
 * so the name and base may still change: the modal offers to read it again. */
export function bitbucketPending(sources: Record<string, unknown> | undefined): boolean {
  return sources?.bitbucket === "pending";
}

/** Where the base came from, e.g. "repository: gitflow gitflow.branch.develop". "" for the
 * built-in `head`, which is what every launch did before rules existed and needs no note. */
export function baseSource(sources: Record<string, unknown> | undefined): string {
  const s = sources?.base;
  return typeof s === "string" && s && !s.startsWith("builtin") ? s : "";
}

/** A warning in the reader's language when its code is catalogued; the Agent's own English
 * message otherwise, which also carries the detail (which base, which prefixes). */
export function warningText(w: BranchWarning): string {
  return tMaybe("launch.branch_warn." + w.code) ?? w.message;
}

/** Warnings from several answers, each once. */
export function mergeWarnings(...lists: (BranchWarning[] | null | undefined)[]): BranchWarning[] {
  const seen = new Set<string>();
  const out: BranchWarning[] = [];
  for (const list of lists) {
    for (const w of list ?? []) {
      const k = w.code + "\u0000" + w.message;
      if (seen.has(k)) continue;
      seen.add(k);
      out.push(w);
    }
  }
  return out;
}

/** The item a work-item launch names its branch for, and records in the session meta. */
export function branchItemOf(item: { provider: string; key: string; title: string; type?: string; labels?: string[] }): BranchItem {
  return { provider: item.provider, key: item.key, title: item.title, type: item.type || "", labels: item.labels ?? [] };
}
