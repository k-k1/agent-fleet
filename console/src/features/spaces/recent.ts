// Which spaces the Start hub's place chip offers first (ADR 0142 decisions 2 and 7). A space is
// used again by starting a session in it, so "recently used" is the newest session in the space;
// a space nobody started a session in yet ranks by its own creation time. The Agent's list is
// ordered by creation alone, which would bury a month-old space the member returns to every day.
import { sessionSpace } from "../../lib/project.ts";
import type { Session } from "../../types/session.ts";
import type { Space } from "./store.ts";

/** How many spaces the chip lists under "Recent"; the rest sit behind "All spaces". */
export const RECENT_SPACES = 5;

/** The spaces, most recently used first (ties by folder, so the order is stable). */
export function rankSpaces(spaces: Space[], sessions: Session[]): Space[] {
  const lastUse = new Map<string, number>();
  for (const s of sessions) {
    const space = sessionSpace(s);
    if (!space || !s.createdAt) continue;
    const at = Date.parse(s.createdAt);
    if (Number.isNaN(at)) continue;
    if (at > (lastUse.get(space) ?? -Infinity)) lastUse.set(space, at);
  }
  // A session's time wins even when it is older than the space (a clock set back): the creation
  // time stands in only for a space no session was started in.
  const used = (sp: Space): number => {
    const seen = lastUse.get(sp.folder);
    if (seen !== undefined) return seen;
    const made = sp.createdAt ? Date.parse(sp.createdAt) : NaN;
    return Number.isNaN(made) ? -Infinity : made;
  };
  return [...spaces].sort((a, b) => used(b) - used(a) || (a.folder < b.folder ? -1 : a.folder > b.folder ? 1 : 0));
}
