// Tab completion for reply suggestions - a shell-style completion cycle.
//
// The chip row is prefix-filtered by the draft being typed (rankQuickReplies in
// lib/quickReplies). Once "ok" has been typed only the candidates starting with "ok" remain,
// so from there it is faster to pick one from the keyboard: Tab puts a candidate into the
// input, another Tab moves to the next, exactly like shell completion.
//
// The ring is [what the user typed, candidate 1, candidate 2, ...] and wraps back to the typed
// text, so Tab alone is enough to undo a completion. Shift+Tab walks backwards. The candidate
// list is frozen the moment Tab is pressed: completion replaces the input with the candidate
// itself, and recomputing from that would narrow the filter to "things prefixed by candidate
// 1" and break the ring. The frozen base is also what the chip row filters on, so the chip row
// stays still while cycling and only the candidate currently in the input is highlighted.
//
// Tab on an empty input keeps its old meaning - move focus to the chips (handled in MirrorView
// / ChatView). This path exists only for walking the candidates narrowed by what was typed.

// Whitespace folding, full-width/half-width folding and lowercasing reuse the same function as
// the rankQuickReplies prefix filter (quickReplyKey in lib/quickReplies). The matching rule is
// deliberately not reimplemented here, so "the candidates visible in the chip row" and "the
// candidates Tab walks" can never diverge.
import { quickReplyKey as norm } from "./quickReplies.ts";

export type SuggestCycle = {
  /** What the user typed - the origin of the ring and the chip row's filter key. */
  base: string;
  /** Candidates prefixed by base, frozen when Tab was pressed. */
  items: string[];
  /** Ring position: 0 = base, 1..items.length = items[idx - 1]. */
  idx: number;
  /** The text last placed in the input. Once it stops matching the draft the user has typed
   *  over it, which ends the cycle. */
  text: string;
};

/** Candidates prefixed by base, in their display spelling, deduplicated, excluding base. */
export function suggestMatches(base: string, chips: string[]): string[] {
  const b = norm(base);
  const seen = new Set<string>();
  const out: string[] = [];
  for (const c of chips) {
    const k = norm(c);
    if (!k || k === b) continue; // completing to exactly what was typed is pointless
    if (b && !k.startsWith(b)) continue;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(c);
  }
  return out;
}

/**
 * Advance the cycle by one Tab / Shift+Tab. Returns null when the cycle cannot continue, in
 * which case the caller lets Tab through to its usual behaviour (moving focus).
 */
export function stepSuggestCycle(
  cur: SuggestCycle | null,
  draft: string,
  chips: string[],
  backward: boolean,
): SuggestCycle | null {
  // Cycle in progress: continue while the input still holds the candidate we last placed.
  if (cur && cur.text === draft) {
    const n = cur.items.length;
    const idx = (cur.idx + (backward ? -1 : 1) + n + 1) % (n + 1);
    return { ...cur, idx, text: idx === 0 ? cur.base : cur.items[idx - 1] };
  }
  // New cycle. Empty (or whitespace-only) and multi-line drafts are excluded: the first
  // belongs to empty-input Tab (focus the chips), the second is no longer the start of a
  // short reply.
  if (!norm(draft) || /[\r\n]/.test(draft)) return null;
  const items = suggestMatches(draft, chips);
  if (!items.length) return null;
  const idx = backward ? items.length : 1; // Shift+Tab starts from the end
  return { base: draft, items, idx, text: items[idx - 1] };
}

/** The cycle still in effect, or null once the input was edited by hand. */
export function activeSuggestCycle(cur: SuggestCycle | null, draft: string): SuggestCycle | null {
  return cur && cur.text === draft ? cur : null;
}

/** The string the chip row filters on - the frozen base while cycling. */
export function suggestFilterDraft(cur: SuggestCycle | null, draft: string): string {
  return activeSuggestCycle(cur, draft)?.base ?? draft;
}

/** The candidate currently in the input, i.e. the chip to highlight. Null when the ring is
 *  back on base or no cycle is running. */
export function cycledSuggestion(cur: SuggestCycle | null, draft: string): string | null {
  const a = activeSuggestCycle(cur, draft);
  return a && a.idx > 0 ? a.items[a.idx - 1] : null;
}
