// What the Terminal view lists, and what a count chip counts: one rule for both, so a chip never
// promises a group the view does not show.
import type { Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";
import { isTerminalSession, placeRepos, sessionFolder, shellPlaceKey, shellPlaceOf } from "../../lib/project.ts";
import { repoInSet, sessionInSet } from "../../lib/workingSets.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";

// A shell in a working copy is in the set when the copy is, by the same parent-aware rule the tree
// uses for its roots (repoInSet): the folder name alone (sessionInSet) cannot place a worktree
// whose folder does not carry its base's name. Everything else is repo-less and is in the set by
// direct assignment, or through the folder it names.
export function visibleTerminals(sessions: Session[], repos: Repo[], set: WorkingSet | null): Session[] {
  const terminals = sessions.filter(isTerminalSession);
  if (!set) return terminals;
  const byName = new Map(repos.map((r) => [r.name, r]));
  const known = placeRepos(repos);
  return terminals.filter((s) => {
    if (set.sessions.includes(s.name)) return true;
    const place = s.kind === "shell" ? shellPlaceOf(s, known) : null;
    if (place?.kind === "repo") {
      const r = byName.get(place.folder);
      return !!r && repoInSet(set, r);
    }
    // A shell that names a copy's folder but runs somewhere else (a `repos/app` of its own under
    // home) is not that copy's: only a direct assignment (above) puts it in the set.
    if (place?.kind === "home" && sessionFolder(s) && byName.has(sessionFolder(s))) return false;
    return sessionInSet(set, s);
  });
}

/** Shells per group key (the chips link to a shell group; SSM logins are not in it). */
export function shellCounts(visible: Session[], repos: Repo[]): Map<string, number> {
  const known = placeRepos(repos);
  const out = new Map<string, number>();
  for (const s of visible) {
    if (s.kind !== "shell") continue;
    const key = shellPlaceKey(shellPlaceOf(s, known));
    out.set(key, (out.get(key) ?? 0) + 1);
  }
  return out;
}
