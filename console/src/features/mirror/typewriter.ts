// mirror/typewriter — how far into the reply claude is still writing the mirror shows right now.
//
// The data is line-granular and arrives by poll (#1250: claude's MessageDisplay hook flushes
// whole lines, the mirror asks for them every ~1.2 s), so the block used to grow in bursts of
// whole lines. Typewriter mode reveals each burst character by character between polls. It is a
// presentation choice made in the Console alone: the Agent and the wire do not change, and the
// text shown is always a prefix of what the last poll returned — nothing is invented, reordered
// or dropped. `advance` is the whole rule, pure so the pacing can be driven with fake timers.

/** Slowest reveal. Below this a two-word addition would take longer than it took to arrive. */
export const TYPEWRITER_FLOOR_CPS = 40;
/** Fastest reveal. Above this the eye reads it as a block landing, and each frame re-parses. */
export const TYPEWRITER_CAP_CPS = 400;
/** The backlog is paced to be gone about when the next poll lands (MIRROR_POLL_FAST is 1.2 s). */
export const TYPEWRITER_CATCH_UP_MS = 1000;
/** Never show text older than this: a reader who opens a session mid-reply, or whose tab was in
 *  the background, gets the tail typed and everything before it at once. */
export const TYPEWRITER_MAX_LAG_MS = 2000;

export interface Typewriter {
  /** The text the last poll returned. */
  target: string;
  /** How many characters of it are on screen. */
  shown: number;
  /** Fraction of a character owed to the next frame (rates below one char per frame). */
  frac: number;
  /** Characters per second for the current backlog; fixed while the target stands, so a burst
   *  types at one steady speed instead of easing out as it runs low. */
  cps: number;
}

export const TYPEWRITER_IDLE: Typewriter = { target: "", shown: 0, frac: 0, cps: 0 };

/** The text on screen. */
export const revealed = (t: Typewriter): string => (t.shown >= t.target.length ? t.target : t.target.slice(0, t.shown));

/** True when nothing is left to type. */
export const caughtUp = (t: Typewriter): boolean => t.shown >= t.target.length;

/** A state showing the whole text at once. */
export const snapped = (target: string): Typewriter => ({ target, shown: target.length, frac: 0, cps: 0 });

/** retarget folds a poll's text in. Text that extends what is on screen keeps its place and is
 *  paced to catch up in about a poll; anything else — a different message, a shorter one — is
 *  shown at once, since typing over a replaced text would read as claude rewriting. Returns the
 *  same object when the target is unchanged, so callers can skip a render. */
export function retarget(t: Typewriter, target: string): Typewriter {
  if (target === t.target) return t;
  if (!target.startsWith(t.target)) return snapped(target);
  let shown = t.shown;
  const maxLag = Math.round((TYPEWRITER_CAP_CPS * TYPEWRITER_MAX_LAG_MS) / 1000);
  if (target.length - shown > maxLag) shown = target.length - maxLag;
  const backlog = target.length - shown;
  const cps = Math.min(TYPEWRITER_CAP_CPS, Math.max(TYPEWRITER_FLOOR_CPS, (backlog * 1000) / TYPEWRITER_CATCH_UP_MS));
  return { target, shown, frac: 0, cps };
}

/** advance types for dtMs. Returns the same object when nothing moved. */
export function advance(t: Typewriter, dtMs: number): Typewriter {
  if (caughtUp(t) || dtMs <= 0) return t;
  const owed = (t.cps * dtMs) / 1000 + t.frac;
  const n = Math.floor(owed);
  if (n <= 0) return { ...t, frac: owed };
  const shown = cutAt(t.target, Math.min(t.target.length, t.shown + n));
  return { ...t, shown, frac: shown >= t.target.length ? 0 : owed - n };
}

/** cutAt keeps a cut off two places where a half-shown string renders as something else: inside
 *  a surrogate pair (a lone half is U+FFFD), and inside a table row — a row typed halfway is a
 *  paragraph, then snaps into a table, and it does that for every row. Other Markdown mid-states
 *  (an open `**`, a `[` before its `](…)`) render literally for a few frames and then flip, which
 *  is what chat UIs do; an unclosed code fence already renders as a code block (CommonMark runs
 *  it to the end of the document), so fences need nothing. */
export function cutAt(text: string, at: number): number {
  if (at >= text.length) return text.length;
  if (at > 0 && isLowSurrogate(text.charCodeAt(at)) && isHighSurrogate(text.charCodeAt(at - 1))) at += 1;
  const lineStart = text.lastIndexOf("\n", at - 1) + 1;
  if (isTableRow(text, lineStart)) {
    const eol = text.indexOf("\n", at);
    return eol < 0 ? text.length : eol;
  }
  return at;
}

const isHighSurrogate = (c: number): boolean => c >= 0xd800 && c <= 0xdbff;
const isLowSurrogate = (c: number): boolean => c >= 0xdc00 && c <= 0xdfff;

function isTableRow(text: string, lineStart: number): boolean {
  let i = lineStart;
  while (i < text.length && (text[i] === " " || text[i] === "\t")) i++;
  return text[i] === "|";
}
