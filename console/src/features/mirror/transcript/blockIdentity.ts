// transcript/blockIdentity — keep a conversation block's identity fixed while the window it is
// read through moves.
//
// groupTurns folds consecutive same-role rows into one block and names it after its FIRST row's
// idx. That name is the React key, the data-turn-idx, and therefore the target of every scroll
// anchor (scrollMark, the prepend hold, "start of the reply", the read-aloud cursor).
//
// It is not stable. A reply is many jsonl rows (claude/codex write the narration, each tool call
// and the answer as separate lines), so the edge of a 400-line window normally falls INSIDE a
// block; "load earlier messages" then prepends rows of that same block and its first row becomes
// an older one. Measured against the real bundle (mirror-scroll's `readup`): one page turned the
// block the reader was sitting in from turn 182 into turn 122, which
//
//   * unmounts it. React sees a new key, throws the subtree away and builds a new one, so
//     TranscriptTurn's per-turn ref goes with it — the one-way fold latch, the settled WorkSplit
//     boundary, and whether the READER had opened or closed the work trace. It is then re-decided
//     from defaultWorkOpen, and a reader who is scrolled up (which they are — that is what
//     triggered the load) gets the live block's treatment: open. Measured: a closed 作業過程 came
//     back open and the transcript went 4,427px → 91,296px.
//   * takes the anchors with it. holdPrependAnchor looks its target up as [data-turn-idx="182"]
//     and finds nothing, so the hold that exists precisely for this moment is dropped, and the
//     reader is left wherever the growth above them puts them (measured: the top of the
//     transcript).
//
// So the id has to be chosen against what the client has already rendered rather than recomputed
// from the current window. Rows only ever join a block at its FRONT (a backward page) or its BACK
// (streaming), and blocks are disjoint ranges of rows in transcript order — so "the same block"
// is simply "its row range overlaps the range this block had last time", and the id it was given
// then is the id it keeps.
import { useRef } from "react";
import type { Group } from "./types.ts";

interface Block {
  first: number;
  last: number;
  id: number;
}

/** The row range a block currently covers. endIdx is absent on a block folded from turns that
 *  carry no idx at all (an Agent old enough not to send one) — then the range is a point. */
const rangeOf = (g: Group): Block | null =>
  typeof g.idx === "number" ? { first: g.idx, last: typeof g.endIdx === "number" ? g.endIdx : g.idx, id: g.idx } : null;

/**
 * Re-stamp `idx` on each block with the id it was rendered under before, when it is recognisably
 * the same block. Returns the SAME array when nothing was re-stamped (the overwhelmingly common
 * case), so no consumer sees a new `groups` identity for free.
 *
 * `session` resets the memory: a pane keeps this component across a session switch (the mirror is
 * not remounted, only its props change) and row numbers restart per session.
 */
export function useStableBlockIds(groups: Group[], session: string): Group[] {
  const held = useRef<{ session: string; blocks: Block[] }>({ session: "", blocks: [] });
  if (held.current.session !== session) held.current = { session, blocks: [] };

  const prev = held.current.blocks;
  const blocks: Block[] = [];
  let out: Group[] | null = null;
  // Both lists are in transcript order, so one pass over each is enough. A remembered block is
  // consumed once: if two blocks now overlap one old one (a row that used to merge them stopped
  // being noise, say), only the first inherits the id — two blocks under one key would be far
  // worse than a re-mount.
  let p = 0;
  for (let i = 0; i < groups.length; i++) {
    const g = groups[i];
    const range = rangeOf(g);
    if (!range) {
      if (out) out.push(g); // no idx to match on (an Agent old enough not to send one) — pass through
      continue;
    }
    while (p < prev.length && prev[p].last < range.first) p++;
    const hit = p < prev.length && prev[p].first <= range.last ? prev[p] : null;
    if (hit) {
      range.id = hit.id;
      p++;
    }
    blocks.push(range);
    if (range.id !== g.idx) {
      if (!out) out = groups.slice(0, i);
      out.push({ ...g, idx: range.id });
    } else if (out) {
      out.push(g);
    }
  }
  held.current.blocks = blocks;
  return out ?? groups;
}
