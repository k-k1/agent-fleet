// mergeVisibleOrder writes a reorder of the providers the member can see back into the stored
// imageProviderOrder without touching the ones the credential policy hides (ADR 0138 D10a).
//
// Every hidden entry keeps its exact slot, and the visible entries are written into the slots the
// visible ones held, in their new order: stored [hidden, A, B] reordered to [B, A] becomes
// [hidden, B, A]. Moving hidden entries anywhere else would change which payer runs first once
// the policy allows them again. A visible id the stored list does not name yet (the Agent placed
// it by its built-in rule) is first inserted right after the visible id it follows in `before`,
// so the stored list states the order the member saw before the move.
export function mergeVisibleOrder(stored: readonly string[], before: readonly string[], after: readonly string[]): string[] {
  const visible = new Set(before);
  // Only a permutation of what was shown can be written back.
  if (after.length !== before.length || !after.every((id) => visible.has(id))) return [...stored];
  const base = stored.filter((id, i) => !visible.has(id) || stored.indexOf(id) === i);
  let prev = -1;
  for (const id of before) {
    const at = base.indexOf(id);
    if (at >= 0) {
      prev = at;
      continue;
    }
    base.splice(prev + 1, 0, id);
    prev += 1;
  }
  let k = 0;
  return base.map((id) => (visible.has(id) ? after[k++] : id));
}
