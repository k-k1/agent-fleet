// What the Auto-run view's actions act on. They reuse the repository tree's stop and delete
// plans (features/repos), so a run is handed to them as the same RepoTreeNode shape the rail
// builds, plus a filter that keeps a member's own session in a shared copy out of the scope.
import type { Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";
import { sessionFolder, sessionsInFolder, worktreeOwners, type RepoTreeNode } from "../../lib/project.ts";
import { runRootName, type RunRow } from "./runs.ts";

const node = (repo: Repo, children: RepoTreeNode[] = []): RepoTreeNode => ({ repo, children, spine: "" });

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

/** The run's own worktree for the delete plan; null when it runs in a base clone, its copy is
 *  unknown, or the run did not create it (see createdWorktree for how that is decided). A member's
 *  worktree a run was later pointed at is not the run's to delete. `sessions` is the list the
 *  owner is read from. */
export function runWorktreeNode(run: RunRow, repos: Repo[], sessions: Session[]): RepoTreeNode | null {
  const wt = createdWorktree(run, repos, sessions);
  return wt && !leavesRunSessions(wt.repo.name, sessions) ? wt : null;
}

/** The copy the run created, whatever else of the run lives elsewhere. When the Agent recorded the
 *  creating session (`createdBy`) that is the proof: only the run's root counts. A copy without the
 *  record (made before it existed) falls back to the first session still listed there, which is a
 *  guess (the creator may have been archived), so the delete dialog's confirmation stays the check. */
function createdWorktree(run: RunRow, repos: Repo[], sessions: Session[]): RepoTreeNode | null {
  const folder = sessionFolder(run.root);
  const repo = repos.find((r) => r.name === folder);
  if (repo?.worktree !== true) return null;
  if (repo.createdBy) {
    if (repo.createdBy !== run.root.name) return null;
    // The record can offer a copy the guess would not (a member's session that sorts first at the
    // same timestamp). The delete dialog archives every session in the copy, so then a session of
    // no run keeps the copy out of the offer rather than being swept up by it.
    if (worktreeOwners(sessions).get(folder)?.name !== run.root.name) {
      const byName = new Map(sessions.map((s) => [s.name, s]));
      if (sessionsInFolder(sessions, folder).some((s) => runRootName(s, byName) === "")) return null;
    }
  } else if (worktreeOwners(sessions).get(folder)?.name !== run.root.name) return null;
  return node(repo);
}

/** Whether deleting `folder` would archive a run's session while another session of the same run
 *  stays listed outside it. Those would be unreachable from any run afterwards. Looks at every run
 *  with a session in the copy, not only the one asking. */
function leavesRunSessions(folder: string, sessions: Session[]): boolean {
  const byName = new Map(sessions.map((s) => [s.name, s]));
  const roots = new Set<string>();
  for (const s of sessions) if (sessionFolder(s) === folder) roots.add(runRootName(s, byName));
  roots.delete("");
  return sessions.some((t) => sessionFolder(t) !== folder && roots.has(runRootName(t, byName)));
}

/** "Clear finished runs": per ended run, the stopped sessions to archive (what the run started
 *  first, the run itself last, so a failure part-way leaves the run listed and retryable), and one
 *  delete plan over the worktrees of runs whose PR was merged. A worktree another session also
 *  lives in is not offered, and its sessions are archived like any other copy's. Session state is
 *  read from `sessions`, so call it with the latest list. */
export function planClearFinished(
  runs: RunRow[],
  sessions: Session[],
  repos: Repo[],
): { archive: Session[][]; node: RepoTreeNode | null } {
  const fresh = new Map(sessions.map((s) => [s.name, s]));
  const ended = runs.filter((r) => r.state === "ended");
  const ownedBy = (r: RunRow) => runSessions(r).flatMap((s) => fresh.get(s.name) ?? []);
  const mine = new Set(ended.flatMap(ownedBy).map((s) => s.name));
  // `offered` holds every candidate copy, so its sessions are left to the delete dialog; `heads`
  // only those whose run has nothing left outside the copy. A run with a session elsewhere (not yet
  // archived, alive, locked, or failed) keeps its copy and root until a later clear finds it clear.
  const heads: RepoTreeNode[] = [];
  const offered = new Set<string>();
  for (const r of ended) {
    if (r.pr?.state !== "merged") continue;
    const wt = createdWorktree(r, repos, sessions);
    if (!wt || offered.has(wt.repo.name)) continue;
    if (!sessionsInFolder(sessions, wt.repo.name).every((s) => mine.has(s.name))) continue;
    offered.add(wt.repo.name);
    if (!leavesRunSessions(wt.repo.name, sessions)) heads.push(wt);
  }
  // A session is archived only when it and everything the run started below it can be: an ancestor
  // archived while a descendant stays would leave that descendant unreachable from its run.
  const archive = ended
    .map((r) => {
      const all = ownedBy(r);
      const kids = new Map<string, string[]>();
      for (const s of all) if (s.originSession) kids.set(s.originSession, [...(kids.get(s.originSession) ?? []), s.name]);
      const byName = new Map(all.map((s) => [s.name, s]));
      const ok = (s: Session) => !s.alive && !s.locked && !offered.has(sessionFolder(s));
      const clearable = (name: string, seen = new Set<string>()): boolean => {
        const s = byName.get(name);
        if (!s) return false;
        if (seen.has(name)) return true;
        seen.add(name);
        return ok(s) && (kids.get(name) ?? []).every((k) => clearable(k, seen));
      };
      const depth = (s: Session): number => {
        let d = 0;
        for (let c: Session | undefined = s; c?.originSession && d <= all.length; c = byName.get(c.originSession)) d++;
        return d;
      };
      return all.filter((s) => clearable(s.name)).sort((x, y) => depth(y) - depth(x));
    })
    .filter((g) => g.length > 0);
  const [head, ...rest] = heads;
  return { archive, node: head ? { ...head, children: rest } : null };
}

/** The names above `name` in its run (its parent, that one's parent, ...), for skipping the
 *  ancestors of a session whose archive failed. */
export function ancestorNames(name: string, sessions: Session[]): string[] {
  const byName = new Map(sessions.map((s) => [s.name, s]));
  const out: string[] = [];
  for (let c = byName.get(name)?.originSession; c && !out.includes(c) && out.length < sessions.length; c = byName.get(c)?.originSession) out.push(c);
  return out;
}
