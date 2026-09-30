// Initialize Git Flow (ADR 0103 decision 9): the dialog's opening state and the write. The
// Agent writes git-flow's own keys into the clone's shared config, so every worktree of the
// repository reads them at once.
import { api, apiJSON } from "../../core/api/client.ts";

/** The dialog's fields: Fork's six plus the optional bugfix prefix. */
export interface GitflowValues {
  production: string;
  development: string;
  feature: string;
  bugfix: string;
  release: string;
  hotfix: string;
  versiontag: string;
}

/** GET /repos/{name}/gitflow. */
export interface GitflowState {
  /** The gitflow.* keys the dialog owns that are set now; the save sends them back. */
  current: Record<string, string>;
  prefill: GitflowValues;
  local: string[];
  origin: string[];
  /** git-flow-next's native config is present and outranks the keys written here. */
  native: boolean;
  /** Committed declarations (.agent-fleet/branches, .gitflow) that outrank them too. */
  committed: string[];
}

export type GitflowSaveResult =
  | { ok: true; written: string[] }
  | { ok: false; code: string; message: string; field?: string; written?: string[] };

/** The config key each field writes, in the dialog's order. */
export const GITFLOW_KEY: Record<keyof GitflowValues, string> = {
  production: "gitflow.branch.master",
  development: "gitflow.branch.develop",
  feature: "gitflow.prefix.feature",
  bugfix: "gitflow.prefix.bugfix",
  release: "gitflow.prefix.release",
  hotfix: "gitflow.prefix.hotfix",
  versiontag: "gitflow.prefix.versiontag",
};

const path = (repo: string) => `api/repos/${encodeURIComponent(repo)}/gitflow`;

/** null when there is nothing to show: an Agent without the route, a working copy that is not
 * git, a stopped Workspace. */
export async function fetchGitflow(repo: string): Promise<GitflowState | null> {
  try {
    const j = await api(path(repo));
    if (!j || j.error || !j.prefill || typeof j.current !== "object") return null;
    return j as GitflowState;
  } catch {
    return null;
  }
}

export async function saveGitflow(repo: string, expected: Record<string, string>, values: GitflowValues): Promise<GitflowSaveResult> {
  try {
    const j = await apiJSON(`${path(repo)}/init`, "POST", { expected, values });
    if (j && Array.isArray(j.written) && !j.error) return { ok: true, written: j.written };
    const e = (j?.error ?? {}) as { code?: string; message?: string; field?: string };
    return {
      ok: false,
      code: e.code || "failed",
      message: e.message || String(j?.error ?? ""),
      field: e.field,
      written: Array.isArray(j?.written) ? j.written : undefined,
    };
  } catch (err) {
    return { ok: false, code: "network", message: String(err) };
  }
}

/** Where a branch is: both, only on origin, only local, or nowhere. The git flow CLI and Fork
 * accept a branch only when it is local (measured with gitflow-avh 1.12.4-dev), and Initialize
 * Git Flow never creates one. */
export type BranchPlace = "both" | "origin" | "local" | "missing";

export function branchPlace(st: GitflowState, name: string): BranchPlace {
  const n = name.trim();
  const l = st.local.includes(n);
  const o = st.origin.includes(n);
  return l && o ? "both" : o ? "origin" : l ? "local" : "missing";
}

/** The keys already set whose value the save would change. An empty bugfix field leaves its
 * key alone, and support is written only when absent, so neither shows up here. */
export function gitflowChanges(st: GitflowState, v: GitflowValues): { key: string; from: string; to: string }[] {
  const out: { key: string; from: string; to: string }[] = [];
  for (const f of Object.keys(GITFLOW_KEY) as (keyof GitflowValues)[]) {
    const key = GITFLOW_KEY[f];
    const to = v[f].trim();
    if (f === "bugfix" && to === "") continue;
    if (key in st.current && st.current[key] !== to) out.push({ key, from: st.current[key], to });
  }
  return out;
}
