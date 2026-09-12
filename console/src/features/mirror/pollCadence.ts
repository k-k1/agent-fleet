// mirror/pollCadence — how long to wait before the next transcript poll.
//
// WHY A LADDER AND NOT A FIXED INTERVAL. The mirror polls GET /sessions/{name}/messages for as
// long as it is on screen: at the flat 1.2 s used while a turn runs, an hour of watching is
// ~3,000 requests, and each response re-sends the whole-session aggregates (files/tasks/answers)
// whether or not they moved — measured against a real 13 MiB transcript: 5–13 KiB raw, 0.9–2.6 KiB
// gzipped, every time. On a phone that is what keeps the radio out of idle, and it buys nothing
// while the agent is silently thinking.
//
// So the interval follows the CONVERSATION, not the clock: every poll whose response is identical
// to the previous one raises `unchanged` by one, and any difference at all — a new line, a status
// flip, a pending question — drops it back to 0 and the fast rung. A reader touching the pane
// resets it too (MirrorView's bump), so "I am watching this" always means the fast rung.
//
// The rungs are capped rather than open-ended: a working session must never look dead, and a
// resting one must still notice a background turn starting. Worst case added latency is the last
// rung — 3 s while working, 15 s at rest — and only after the view has been told the same thing
// many times in a row.

/** First rung while a turn (or background work) runs. The cadence the mirror always had. */
export const MIRROR_POLL_FAST = 1200;
/** First rung at rest. The cadence the mirror always had. */
export const MIRROR_POLL_REST = 3000;

export interface PollCadence {
  /** A turn is running, background work is running, or the idle→reply bridge is open. */
  working: boolean;
  /** Consecutive polls whose response was byte-identical to the one before. */
  unchanged: number;
}

/** Ladder for a running turn: ~10 s of silence before easing off, and never slower than the
 *  resting cadence — a live turn's next line should not wait longer than an idle session's. */
const WORKING = [
  { after: 8, ms: MIRROR_POLL_FAST },
  { after: 25, ms: 2000 },
  { after: Infinity, ms: MIRROR_POLL_REST },
];

/** Ladder at rest: what arrives here is a session waking up on its own (a background task, a
 *  peer message, a scheduled run), so being a few seconds late costs nothing a reader can feel. */
const REST = [
  { after: 5, ms: MIRROR_POLL_REST },
  { after: 20, ms: 8000 },
  { after: Infinity, ms: 15000 },
];

export function pollDelay({ working, unchanged }: PollCadence): number {
  const ladder = working ? WORKING : REST;
  const n = Math.max(0, unchanged);
  for (const rung of ladder) {
    if (n < rung.after) return rung.ms;
  }
  return ladder[ladder.length - 1].ms;
}
