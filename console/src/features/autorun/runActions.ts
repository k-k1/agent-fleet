// What the Auto-run view's actions act on. They reuse the repository tree's stop and delete
// plans (features/repos), so a run is handed to them as the same RepoTreeNode shape the rail
// builds, plus a filter that keeps a member's own session in a shared copy out of the scope.
import type { Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";
import { sessionFolder, sessionsInFolder, type RepoTreeNode } from "../../lib/project.ts";
import type { RunRow } from "./runs.ts";

const node = (repo: Repo, children: RepoTreeNode[] = []): RepoTreeNode => ({ repo, children, groups: [], spine: "" });

/** The run's own sessions: the run and what it started. */
export const runSessions = (run: RunRow): Session[] => [run.root, ...run.children];

/** The copy a run works in. A copy the repository list does not name (already deleted) yields a
 *  bare stand-in so the plan still finds the sessions by folder. */
const repoOf = (folder: string, repos: Repo[]): Repo => repos.find((r) => r.name === folder) ?? { name: folder };

/** The live sessions of a run and the copies they run in, for the stop plan; null when none is
 *  alive. The head is the run's own copy, any other copy a reviewer uses hangs under it. */
export function runStopScope(run: RunRow, repos: Repo[]): { node: RepoTreeNode; only: Set<string> } | null {
  const live = runSessions(run).filter((s) => s.alive);
  if (live.length === 0) return null;
  const only = new Set(live.map((s) => s.name));
  const rootFolder = sessionFolder(run.root);
  const folders = [...new Set(live.map(sessionFolder))].filter(Boolean);
  const head = folders.includes(rootFolder) ? rootFolder : folders[0];
  if (!head) return null;
  return { node: node(repoOf(head, repos), folders.filter((f) => f !== head).map((f) => node(repoOf(f, repos)))), only };
}

/** The run's own worktree for the delete plan; null when it runs in a base clone (never the
 *  run's to delete) or its copy is unknown. */
export function runWorktreeNode(run: RunRow, repos: Repo[]): RepoTreeNode | null {
  const repo = repos.find((r) => r.name === sessionFolder(run.root));
  return repo?.worktree === true ? node(repo) : null;
}

/** "Clear finished runs": stopped sessions of ended runs to archive, and one delete plan over the
 *  worktrees of runs whose PR was merged. A worktree another session also lives in is not
 *  offered, and its sessions are archived like any other copy's. */
export function planClearFinished(
  runs: RunRow[],
  sessions: Session[],
  repos: Repo[],
): { archive: Session[]; node: RepoTreeNode | null } {
  const ended = runs.filter((r) => r.state === "ended");
  const mine = new Set(ended.flatMap(runSessions).map((s) => s.name));
  const heads: RepoTreeNode[] = [];
  const offered = new Set<string>();
  for (const r of ended) {
    if (r.pr?.state !== "merged") continue;
    const wt = runWorktreeNode(r, repos);
    if (!wt || offered.has(wt.repo.name)) continue;
    if (!sessionsInFolder(sessions, wt.repo.name).every((s) => mine.has(s.name))) continue;
    offered.add(wt.repo.name);
    heads.push(wt);
  }
  const archive = ended
    .flatMap(runSessions)
    .filter((s) => !s.alive && !s.locked && !offered.has(sessionFolder(s)))
    .filter((s, i, all) => all.findIndex((x) => x.name === s.name) === i);
  const [head, ...rest] = heads;
  return { archive, node: head ? { ...head, children: rest } : null };
}
