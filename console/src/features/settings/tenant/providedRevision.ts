// The events stream's "provided_routes" frame (ADR 0129 D3 gate 4): an opaque revision of this
// tenant's provided-route grant, policy and member overrides. The frame carries no values, so a
// change is only a signal to read again through the REST view the viewer is allowed to see.
//
// The first frame after load is a baseline, not a change: the page has just read the same values.

type Listener = () => void;
const listeners = new Set<Listener>();
let last: string | null = null;

/** noteProvidedRev takes one pushed frame and wakes the listeners when the revision moved. */
export function noteProvidedRev(d: unknown): void {
  const rev = (d as { rev?: unknown } | null | undefined)?.rev;
  if (typeof rev !== "string" || rev === "") return;
  const prev = last;
  last = rev;
  if (prev !== null && prev !== rev) [...listeners].forEach((l) => l());
}

/** onProvidedRoutesChanged registers a listener. Returns the unsubscriber. */
export function onProvidedRoutesChanged(l: Listener): () => void {
  listeners.add(l);
  return () => void listeners.delete(l);
}

/** resetProvidedRev forgets the baseline (tests). */
export function resetProvidedRev(): void {
  last = null;
  listeners.clear();
}

/** forgetProvidedRev drops the baseline, keeping the listeners: after a tenant switch the next
 *  frame is another tenant's baseline, not a change. */
export function forgetProvidedRev(): void {
  last = null;
}
