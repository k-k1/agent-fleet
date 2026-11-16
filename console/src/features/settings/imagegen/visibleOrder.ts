// Writing a reorder of the image providers the member can see back into the stored
// imageProviderOrder, without touching what the credential policy hides (ADR 0138 D10a).

// The kind names a preference saved before ADR 0082 used for the fleet's own engines. The Agent's
// normalizeStoredProviderOrder expands one, at its slot, to every declared row of that kind not
// already ranked earlier.
export const LEGACY_ALIASES: readonly string[] = ["comfy", "openai-compat"];

// mergeVisibleOrder returns the stored order with the visible providers in the member's new order.
//
// - Every hidden entry keeps its exact slot, and the visible entries are written into the slots
//   the visible ones held: stored [hidden, A, B] reordered to [B, A] becomes [hidden, B, A].
//   Moving hidden entries anywhere else would change which payer runs first once the policy
//   allows them again.
// - A visible id the stored list does not name yet (the Agent placed it by its built-in rule) is
//   first inserted right after the visible id it follows in `before`.
// - A legacy alias whose kind has visible rows (kinds: row key → kind, from orderKinds) is
//   replaced by those rows, as the Agent would read it, and put back right after the last of them
//   in the result. Left at its old slot it would pull a visible row ranked after it back up to
//   that slot; after them it only places the rows of its kind the member cannot see.
export function mergeVisibleOrder(
  stored: readonly string[],
  before: readonly string[],
  after: readonly string[],
  kinds: Readonly<Record<string, string>> = {},
): string[] {
  const visible = new Set(before);
  // Only a permutation of what was shown can be written back.
  if (after.length !== before.length || !after.every((id) => visible.has(id))) return [...stored];

  const rowsOf = (kind: string) => before.filter((id) => kinds[id] === kind);
  const folded = new Set<string>();
  const seen = new Set<string>();
  const base: string[] = [];
  for (const id of stored) {
    if (LEGACY_ALIASES.includes(id) && !visible.has(id) && rowsOf(id).length > 0) {
      folded.add(id);
      for (const row of rowsOf(id)) {
        if (!seen.has(row)) {
          seen.add(row);
          base.push(row);
        }
      }
      continue;
    }
    if (visible.has(id) && seen.has(id)) continue;
    seen.add(id);
    base.push(id);
  }

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
  const out = base.map((id) => (visible.has(id) ? after[k++] : id));
  for (const alias of folded) {
    const last = Math.max(...rowsOf(alias).map((row) => out.indexOf(row)));
    out.splice(last + 1, 0, alias);
  }
  return out;
}
