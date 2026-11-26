// Launching a successor (handoff proposal, plan review) from a session that runs in a space: it
// starts in that same space through the Start hub's space form, the way a space launch does
// (ADR 0142 decision 7), instead of the repository dialog, which offers a worktree and branch a
// space does not have and would send `dir` where the Agent's space checks only read `space`.
import { api } from "../../core/api/client.ts";
import { spacesOwner, useSpacesStore } from "../spaces/store.ts";
import { useLaunchSeed } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";

/** What asking the Agent about a space found. Only "gone" stops a launch, and only because THIS
 *  request's own list, read for this workspace, does not hold the folder. "unknown" is a list that
 *  could not be read (a transient failure, an Agent without the route): the form re-checks the
 *  pick and the Agent refuses an unknown `space` (space_not_found), so the launch goes on — it
 *  never starts in home. "stale" is a workspace switch during the request: the answer belongs to
 *  the one left, so nothing is decided. */
export type SpaceCheck = "there" | "gone" | "unknown" | "stale";

/** Reads GET /api/spaces afresh for the folder. The cached list is not consulted: it may predate a
 *  space made elsewhere, and a cached "owner" proves nothing about this request. */
export async function spaceCheck(folder: string): Promise<SpaceCheck> {
  const owner = spacesOwner();
  const ticket = useSpacesStore.getState().begin();
  let d: { spaces?: unknown; error?: unknown } | null | undefined;
  try {
    d = await api("api/spaces");
  } catch {
    return spacesOwner() === owner ? "unknown" : "stale";
  }
  if (spacesOwner() !== owner) return "stale";
  if (!d || d.error || !Array.isArray(d.spaces)) return "unknown";
  // Hand the list to the store too, so the form that opens next starts from it.
  useSpacesStore.getState().adopt(ticket, d);
  return (d.spaces as { folder?: string }[]).some((s) => s.folder === folder) ? "there" : "gone";
}

/** Opens the Start hub on its space section with `folder` picked. Call it after the launch seed
 *  is set: set() clears the space, so the order is what keeps the pick. */
export function openSpaceLaunch(folder: string): void {
  useLaunchSeed.getState().setSpace(folder);
  useSessionsStore.getState().openStart();
}
