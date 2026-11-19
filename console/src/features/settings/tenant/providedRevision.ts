// The events stream's "provided_routes" frame (ADR 0129 D3 gate 4): an opaque revision of this
// tenant's provided-route grant, policy and member overrides. The frame carries no values, so a
// change is only a signal to read again through the REST view the viewer is allowed to see.
//
// The first frame after load is a baseline, not a change: the page has just read the same values.

type Listener = () => void;
const listeners = new Set<Listener>();
let last: string | null = null;
let lastAdmin: string | null = null;

// The tenant frame and the super_admin's frame usually arrive in the same chunk (one PUT moves
// both digests). Each one waking the listeners would start a read and then flag "again" on it:
// two reads per view, a pair per grant cell. Wakes in the same tick are folded into one.
let wakePending = false;
function wake(): void {
  if (wakePending) return;
  wakePending = true;
  queueMicrotask(() => {
    wakePending = false;
    [...listeners].forEach((l) => l());
  });
}

/** noteProvidedRev takes one pushed frame and wakes the listeners when the revision moved. */
export function noteProvidedRev(d: unknown): void {
  const rev = (d as { rev?: unknown } | null | undefined)?.rev;
  if (typeof rev !== "string" || rev === "") return;
  const prev = last;
  last = rev;
  if (prev !== null && prev !== rev) wake();
}

/** noteProvidedAdminRev takes the super_admin's frame: one digest over every tenant and the
 *  deployment source (#4371). It wakes the same listeners, so a page of another tenant, the tenant
 *  table's grant cells and the source editor read again; its baseline is kept apart from the
 *  tenant frame's. */
export function noteProvidedAdminRev(d: unknown): void {
  const rev = (d as { rev?: unknown } | null | undefined)?.rev;
  if (typeof rev !== "string" || rev === "") return;
  const prev = lastAdmin;
  lastAdmin = rev;
  if (prev !== null && prev !== rev) wake();
}

/** onProvidedRoutesChanged registers a listener. Returns the unsubscriber. */
export function onProvidedRoutesChanged(l: Listener): () => void {
  listeners.add(l);
  return () => void listeners.delete(l);
}

/** resetProvidedRev forgets the baseline (tests). */
export function resetProvidedRev(): void {
  last = null;
  lastAdmin = null;
  listeners.clear();
}

/** forgetProvidedRev drops the baseline, keeping the listeners: after a tenant switch the next
 *  frame is another tenant's baseline, not a change. */
export function forgetProvidedRev(): void {
  last = null;
}
