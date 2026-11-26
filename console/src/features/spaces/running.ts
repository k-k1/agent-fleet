// Which spaces already have an agent at work (#5230). A space has no branch or worktree to split
// work into, so a second agent started there edits the same files as the first; the Start hub
// marks those spaces and asks before launching into one. The Agent refuses the same launch with
// 409 space_busy, so this is the early, friendly half of the guard, not the guard itself.
import { isTerminalSession, sessionSpace } from "../../lib/project.ts";
import type { Session } from "../../types/session.ts";

/** Whether s is an AI session running right now in a space. A terminal is a person's, not a
 *  second agent, and a stopped session writes nothing. */
const runningAgent = (s: Session): boolean => !!sessionSpace(s) && !!s.alive && !isTerminalSession(s);

/** The first AI session running in the space `folder`, if any. */
export function runningInSpace(sessions: Session[], folder: string): Session | undefined {
  if (!folder) return undefined;
  return sessions.find((s) => sessionSpace(s) === folder && runningAgent(s));
}

/** The folders of every space an AI session is running in. */
export function busySpaces(sessions: Session[]): Set<string> {
  const out = new Set<string>();
  for (const s of sessions) if (runningAgent(s)) out.add(sessionSpace(s));
  return out;
}

/** Whether s belongs to the space `folder` whose directory is `dir` ("" when unknown), as near as
 *  the Console can tell the Agent's own rule (gitx.SessionLivesIn), which a delete settles by: its
 *  stored place (or, on an older row, the Agent's `space`) names it; the Agent's current `space`
 *  names it (decided from where the directory resolves now); or its directory, or the folder its
 *  subdir names beneath it, is at or under the space's directory (so a link inside the space keeps
 *  its row here whatever it points at, and a session started at home in a subfolder of the space is
 *  counted). A stored place is judged once, so a link re-pointed since leaves it stale: when the
 *  Agent's current `space` names another space, a STOPPED row is not counted for the stored one
 *  (#5278). A running row keeps its stored space, since its process still holds the directory it
 *  started in and the delete must stay blocked; a subdir session keeps it too, as its launch root is
 *  what the place was judged on. A directory reaching into the space through a link below its top
 *  is not seen here: the Agent resolves it, the Console cannot. */
export function sessionInSpace(s: Session, folder: string, dir: string): boolean {
  const stale = !!s.space && s.space !== folder && !s.subdir && !s.alive;
  const stored = sessionSpace(s) === folder && !stale;
  if (stored || (!!folder && s.space === folder)) return true;
  if (!dir || !s.dir) return false;
  const under = (p: string) => p === dir || p.startsWith(dir + "/");
  return under(s.dir) || (!!s.subdir && under(`${s.dir.replace(/\/+$/, "")}/${s.subdir}`));
}
