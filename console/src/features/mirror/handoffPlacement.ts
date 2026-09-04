// Where a durable card belongs in the transcript.
//
// The handoff proposal used to render AFTER every group, as the scroller's last child.
// A card that never goes away then permanently owns the mirror's landing position:
// auto-follow scrolls to the bottom, lands on the card, and the just-sent prompt plus
// the reply streaming in above it are pushed off-screen, so what you just sent never appears
// (observed in production). Placing it at the moment it was proposed makes it flow with the
// conversation:
// it is last only until the next turn, exactly like any other event.
//
// Times come from the group's transcript timestamp; a group without one is an
// optimistic echo (just typed), i.e. newer than any stored proposal.

/** Index in `times` of the first group NEWER than `at` (ms epoch) — the slot a card
 *  stamped `at` belongs in. Returns times.length when the card is newer than
 *  everything, which is the normal case right after proposing. */
export function chronoInsertIndex(times: Array<string | undefined>, at: number): number {
  if (!Number.isFinite(at)) return times.length;
  for (let i = 0; i < times.length; i++) {
    const t = times[i];
    // No timestamp = a local echo the user just sent: newer than a stored proposal.
    if (t === undefined || t === "") return i;
    const ms = Date.parse(t);
    // An unparseable stamp must not swallow the card: skip the group and keep looking
    // (falling through to the end is the safe, pre-existing behaviour).
    if (Number.isNaN(ms)) continue;
    if (ms > at) return i;
  }
  return times.length;
}
