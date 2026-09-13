// Whether the image-generation pane has anything to generate WITH — the one fact the ops bar
// and the layout map need before they show their "image generation" button (ADR 0081
// decision 1: the pane exists when `GET /imagegen/status` reports a ready fleet provider).
//
// Why a module store and not a fetch in each button: two buttons would mean two identical
// requests on every mount, and the view already reads the same status every 2 s while a job
// runs — so the view FEEDS this store (`noteImagegenStatus`) and the buttons only fetch when
// nobody else has recently. No interval of its own: an engine an administrator switches off
// is noticed when the tab comes back, when the workspace (re)starts, or the next time the
// view reads — never by polling from a bar that is on every screen.
//
// "Available" is false until a status has said otherwise. That hides the button for the
// first round trip after a load; a button that appears and then vanishes when the answer is
// "no engine" would be worse, because it is the answer most fleets give.
import { useEffect, useSyncExternalStore } from "react";
import { isTransientErr } from "../../core/api/client.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { imagegenStatus } from "./api.ts";
import { fleetProvider, type ImagegenStatus } from "./wire.ts";

let available = false;
let fetchedAt = 0;
let inflight: Promise<void> | null = null;
const listeners = new Set<() => void>();

/** How long one answer stands before a button asks again (a tab switch or a workspace start). */
const FRESH_MS = 60_000;

const emit = () => listeners.forEach((l) => l());

/** Record a status somebody already fetched (the view does, every poll) so the buttons follow it. */
export function noteImagegenStatus(s: ImagegenStatus | null | undefined): void {
  fetchedAt = Date.now();
  const next = fleetProvider(s || null) !== null;
  if (next === available) return;
  available = next;
  emit();
}

/** Forget the answer — the workspace stopped, so the next running state asks again. */
export function resetImagegenAvailability(): void {
  fetchedAt = 0;
  if (!available) return;
  available = false;
  emit();
}

function refresh(force: boolean): Promise<void> {
  if (inflight) return inflight;
  if (!force && Date.now() - fetchedAt < FRESH_MS) return Promise.resolve();
  inflight = imagegenStatus()
    .then((s) => {
      // A transient failure (the Agent not up yet) is not "no engine": keep what we had and
      // let the next trigger ask again.
      if (!s || isTransientErr(s)) return;
      noteImagegenStatus(s);
    })
    .catch(() => {})
    .finally(() => {
      inflight = null;
    });
  return inflight;
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
};
const snapshot = () => available;

/**
 * True when the fleet has an image engine to offer. Mounting a subscriber asks once per
 * running workspace (and again when the tab becomes visible after the answer went stale);
 * a stopped workspace resets the answer, since the Agent that would answer is gone with it.
 */
export function useImagegenAvailable(): boolean {
  const running = useWorkspaceStore((s) => wsRunning(s.state));
  useEffect(() => {
    if (!running) {
      resetImagegenAvailability();
      return;
    }
    void refresh(true);
    const onVis = () => {
      if (!document.hidden) void refresh(false);
    };
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, [running]);
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}

/** Test seam: the module state, without a component. */
export const _imagegenAvailability = { get: () => available, refresh, reset: resetImagegenAvailability };
