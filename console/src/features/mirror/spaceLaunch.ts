// Launching a successor (handoff proposal, plan review) from a session that runs in a space: it
// starts in that same space through the Start hub's space form, the way a space launch does
// (ADR 0142 decision 7), instead of the repository dialog, which offers a worktree and branch a
// space does not have and would send `dir` where the Agent's space checks only read `space`.
import { api, isTransientErr } from "../../core/api/client.ts";
import { t as tr } from "../../lib/i18n/index.ts";
import { spacesOwner, useSpacesStore } from "../spaces/store.ts";
import { useLaunchSeed } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";

/** What asking the Agent about a space found. Only "gone" stops a launch, and only because THIS
 *  request's own list, read for this workspace, does not hold the folder. "unknown" is a list that
 *  could not be read (a transient failure, an Agent without the route): the form re-checks the
 *  pick and the Agent refuses an unknown `space` (space_not_found), so the launch goes on — it
 *  never starts in home. "unsupported" is an Agent without the spaces route (a plain 404 / 405):
 *  the hub would refuse every launch, so nothing may be changed on the way to it. "stale" is a workspace switch during the request: the answer belongs to
 *  the one left, so nothing is decided. */
export type SpaceCheck = "there" | "gone" | "unknown" | "unsupported" | "stale";

// Only "no such route" means an Agent without spaces; the same test the hub's own probe applies. A
// plain-text 404 / 405 (the Agent's mux) arrives as http_404 / http_405 with no status.
function isNoRoute(e: unknown): boolean {
  const x = e as { status?: number; code?: string } | null;
  return !!x && (x.status === 404 || x.status === 405 || x.code === "http_404" || x.code === "http_405");
}

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
  if (d?.error && !isTransientErr(d) && isNoRoute(d.error)) return "unsupported";
  if (!d || d.error || !Array.isArray(d.spaces)) return "unknown";
  // Hand the list to the store too, so the form that opens next starts from it.
  useSpacesStore.getState().adopt(ticket, d);
  return (d.spaces as { folder?: string }[]).some((s) => s.folder === folder) ? "there" : "gone";
}

/** Settles whether a launch into `folder` may go on, BEFORE anything is changed for it (a plan
 *  rejected, a shared offer withdrawn). Says why when it may not; true = go on. `stillWanted` is
 *  asked after the wait: the card or plan the press belonged to may have moved on meanwhile, and
 *  then nothing is said or done. */
export async function settleSpace(folder: string, stillWanted: () => boolean, toast: (m: string) => void): Promise<boolean> {
  const there = await spaceCheck(folder);
  if (there === "stale" || !stillWanted()) return false;
  if (there === "gone") toast(tr("mirror.launch_space_gone"));
  else if (there === "unsupported") toast(tr("start.space_unsupported"));
  return there === "there" || there === "unknown";
}

/** Opens the Start hub on its space section with `folder` picked. Call it after the launch seed
 *  is set: set() clears the space, so the order is what keeps the pick. */
export function openSpaceLaunch(folder: string): void {
  useLaunchSeed.getState().setSpace(folder);
  useSessionsStore.getState().openStart();
}
