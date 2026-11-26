// Which spaces already have an agent at work (#5230). A space has no branch or worktree to split
// work into, so a second agent started there edits the same files as the first; the Start hub
// marks those spaces and asks before launching into one. The Agent refuses the same launch with
// 409 space_busy, so this is the early, friendly half of the guard, not the guard itself.
import { isTerminalSession } from "../../lib/project.ts";
import type { Session } from "../../types/session.ts";

/** Whether s is an AI session running right now in a space. A terminal is a person's, not a
 *  second agent, and a stopped session writes nothing. */
const runningAgent = (s: Session): boolean => !!s.space && !!s.alive && !isTerminalSession(s);

/** The first AI session running in the space `folder`, if any. */
export function runningInSpace(sessions: Session[], folder: string): Session | undefined {
  if (!folder) return undefined;
  return sessions.find((s) => s.space === folder && runningAgent(s));
}

/** The folders of every space an AI session is running in. */
export function busySpaces(sessions: Session[]): Set<string> {
  const out = new Set<string>();
  for (const s of sessions) if (runningAgent(s)) out.add(s.space!);
  return out;
}
