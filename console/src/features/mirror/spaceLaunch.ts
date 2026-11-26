// Launching a successor (handoff proposal, plan review) from a session that runs in a space: it
// starts in that same space through the Start hub's space form, the way a space launch does
// (ADR 0142 decision 7), instead of the repository dialog, which offers a worktree and branch a
// space does not have and would send `dir` where the Agent's space checks only read `space`.
import { spacesFor, spacesOwner, useSpacesStore } from "../spaces/store.ts";
import { useLaunchSeed } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";

/** Whether the space can still be launched in. False only when this workspace's own list was read
 *  just now and no longer holds it; a list that could not be read does not stop the launch, because
 *  the form re-checks the pick and the Agent refuses an unknown `space` (space_not_found). The
 *  caller says so and starts nothing: the successor must never fall back to home. */
export async function spaceStillThere(folder: string): Promise<boolean> {
  await useSpacesStore.getState().refresh();
  const st = useSpacesStore.getState();
  const owner = spacesOwner();
  if (st.owner !== owner) return true;
  return spacesFor(st, owner).some((s) => s.folder === folder);
}

/** Opens the Start hub on its space section with `folder` picked. Call it after the launch seed
 *  is set: set() clears the space, so the order is what keeps the pick. */
export function openSpaceLaunch(folder: string): void {
  useLaunchSeed.getState().setSpace(folder);
  useSessionsStore.getState().openStart();
}
