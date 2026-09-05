// transcript/marks — the marks a reader draws over "this bit" of a conversation
// (docs/log/69 / ADR 0050).
//
// Used by both the mirror (owner) and the shared view (recipient). This module holds only how a
// mark decides WHAT it points at: it is pure (no React, no I/O, callable from model.ts), while
// fetching and writing are wired up in useMarks.ts.
//
// The anchor is the equivalent of the W3C Web Annotation TextQuoteSelector (quoted string +
// occurrence number); the actual DOM painting is features/viewer/quoteMarks.ts (the same tool
// plan comments use).
//
// Get the counting scope wrong and a mark lands one element over on the recipient's side only.
// Details in docs/log/69 §69.3; the two points are:
//   - the transcript row ordinal (idx) moves under compaction, so it cannot anchor anything;
//   - nor can a part number relative to a block (Group). groupTurns() concatenates the parts of
//     consecutive turns, so the number depends on how many rows were folded into that block, and
//     since the mirror and the shared view hold different tail windows, the two sides diverge at
//     a window boundary.
// So a root is "stable key of the original turn # part number within that turn", and nth is
// counted only within that one root's rendered text. A root is exactly one part passed through
// the shared DTO untouched, which guarantees both sides count the same string.

import type { Turn } from "./types.ts";

/** The colours on offer. What they mean is the reader's business; the author is shown on a
 *  separate axis, the underline (ADR 0050 decision 5). */
export const MARK_COLORS = ["yellow", "green", "blue", "pink"] as const;
export type MarkColor = (typeof MARK_COLORS)[number];

/** Part kinds that accept a mark. Same table as markProseKinds on the Agent side
 *  (docs/log/69 §69.4). */
export const MARKABLE_KINDS = new Set(["", "text", "plan", "answer", "output", "prompt"]);

export interface TranscriptMark {
  id: string;
  /** Stable key of the original turn (anchorId, else a hash of the body). */
  turn: string;
  /** Part number within the original turn; -1 for the turn body itself. */
  part: number;
  kind: string;
  quote: string;
  nth: number;
  color: string;
  /** "" = the session's owner. For a recipient's mark, the login id stamped by the CP. */
  author?: string;
  created_at?: number;
}

export type NewMark = Omit<TranscriptMark, "id" | "created_at">;

/** The part number meaning the turn body (the turn's own text, not one of its parts). */
export const BODY_PART = -1;

/** The single key used both as the DOM's data-mark-root and to look marks up. */
export function markRootKey(turn: string, part: number): string {
  return turn + "#" + (part === BODY_PART ? "b" : part);
}

/** Inverse of markRootKey: back from a DOM data-mark-root to the stored shape. */
export function parseRootKey(key: string): { turn: string; part: number } | null {
  const at = key.lastIndexOf("#");
  if (at <= 0) return null;
  const tail = key.slice(at + 1);
  if (tail === "b") return { turn: key.slice(0, at), part: BODY_PART };
  const part = Number(tail);
  if (!Number.isInteger(part) || part < 0) return null;
  return { turn: key.slice(0, at), part };
}

// hash32 — fallback for kinds and versions that carry no anchorId. No cryptographic strength is
// needed (only that both sides derive the same value from the same string), so FNV-1a suffices.
function hash32(s: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(16);
}

/**
 * The stable key of a turn. anchorId is authoritative; kinds without one fall back to a hash of
 * the body.
 *
 * pending (the local echo right after send) and queued turns accept no marks: the key changes
 * the moment the real turn arrives, leaving any mark dangling. Returning "" makes the caller
 * build no root at all.
 */
export function turnKey(t: Turn): string {
  if (t.pending || t.queued) return "";
  if (t.anchorId) return t.anchorId;
  const text = t.text || "";
  return text ? "h:" + hash32(text) : "";
}
