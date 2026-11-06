// transcript/TranscriptView — lays grouped blocks out as a conversation.
//
// Inserts a context strip (branch · cwd) above a block whenever either changes from the
// previously shown one, so a branch switch or cd is marked once rather than repeated on
// every turn. Empty context leaves the marker as-is. Durable inline cards (handoff
// proposals) are placed at their own chronological moment.
//
// This is the single entry point both readers use: MirrorView (owner) and
// SharedSessionView (recipient) differ only in the TranscriptCaps they hand in.

import type { ReactNode } from "react";
import { CompactBlock, ContextLine } from "./blocks.tsx";
import { MarkLayer } from "./MarkLayer.tsx";
import { TranscriptTurn } from "./TranscriptTurn.tsx";
import { ctxSizeAfter, ctxSizeBefore } from "./model.ts";
import type { Group } from "./types.ts";
import type { TranscriptCaps } from "./capabilities.ts";
import { liveExchangeFrom } from "../mirrorParts.ts";
import { chronoInsertIndex } from "../handoffPlacement.ts";

export interface TranscriptViewProps {
  groups: Group[];
  caps: TranscriptCaps;
  /**
   * The session is mid-turn: the live exchange keeps its work trace unfolded. Pass the
   * WIDEST notion of busy available (the mirror adds background runs and the idle→reply
   * bridge to the polled status) — a turn folding and unfolding as this flaps is what
   * moves the text under a reader. TranscriptTurn latches the fold as a backstop, so a
   * flap costs at most one fold, never a fold/unfold cycle.
   */
  working?: boolean;
  /**
   * A turn is running or its reply is still landing — `working` without background work (a
   * `run_in_background` shell, a subagent). Decides only when a reply counts as finished for
   * translation: background work can linger for the life of the session, and with `working`
   * alone the reply in front of it was never offered the translate button, so the automatic
   * press never fired either. Defaults to `working`.
   */
  turnWorking?: boolean;
  /**
   * `groups` still belong to the session this view showed before `caps.session` (a pane that
   * switches session renders the outgoing transcript once under the incoming name). Nothing
   * about the incoming session may be learned from such a render — see arrivedSinceOpen.
   */
  stale?: boolean;
  /**
   * Which jsonl `groups` were read from (the mirror's poll `jsonlPath`). Row numbers restart in
   * a new file, so automatic translation's watch boundary is re-armed when this changes. Absent
   * = unknown; a drop of the newest row below anything seen before is then the only signal.
   */
  logId?: string;
  /**
   * Fold completed work even for the live exchange. The mirror passes its "am I pinned to
   * the bottom" flag: someone who scrolled up to read a streaming tool trace shouldn't have
   * it yanked closed when the turn completes. Read once, when a turn's work trace first
   * folds — afterwards only the reader's own click opens or closes it.
   */
  autoCollapseWork?: boolean;
  /**
   * Durable cards to place at their own moment in the conversation (handoff proposals —
   * a session may have more than one outstanding at once). Each is appended last only
   * while nothing newer exists — never pinned there, which is what used to hide every
   * later message (see handoffPlacement).
   */
  inlineCards?: Array<{ at: number; node: ReactNode }>;
}

export function TranscriptView({
  groups,
  caps,
  working = false,
  turnWorking = working,
  stale = false,
  logId = "",
  autoCollapseWork = false,
  inlineCards = [],
}: TranscriptViewProps) {
  const els: ReactNode[] = [];
  let prevCtx = "";
  const times = groups.map((g) => g.ts);
  // Sorted by `at` so cards landing at the same insertion slot (e.g. several proposed in
  // one turn, all newer than every group) still render oldest-first.
  const cards = inlineCards
    .slice()
    .sort((a, b) => a.at - b.at)
    .map((c) => ({ ...c, insertAt: chronoInsertIndex(times, c.at) }));
  // The current work boundary, counting only prompts that have LANDED — neither a queued one
  // nor a just-sent optimistic echo.
  //
  // Counting the echo (which this used to do) hands the live exchange to a prompt the agent has
  // not started: type a follow-up while a long reply streams and the echo appends after it, so
  // that still-streaming reply falls before the boundary and folds mid-stream — permanently,
  // because folding is one-way. The echo becomes `queued` a poll later (only once the agent
  // reports its queue), but the latch has already closed and the reply spends the rest of its
  // life folded.
  //
  // Excluding the echo does NOT unfold the previous, already-finished reply the moment you hit
  // send (the reason it was counted here). That reply folded when it completed, and TranscriptTurn
  // latches the fold: foldWork going false again never re-opens anything.
  //
  // A window with no prompt in it at all is its own case, and not a rare one on a long autonomous
  // stretch — see liveExchangeFrom, which is what keeps "no prompt in sight" from meaning "all of
  // this is live".
  const liveFrom = liveExchangeFrom(groups);
  const arrivedAfter = arrivedSinceOpen(groups, caps.session || "", logId, stale);
  for (let i = 0; i < groups.length; i++) {
    for (const c of cards) if (c.insertAt === i) els.push(c.node);
    const g = groups[i];
    const ctx = g.branch || g.cwd ? (g.branch || "") + "\x1f" + (g.cwd || "") : "";
    if (ctx && ctx !== prevCtx) {
      els.push(<ContextLine key={"ctx-" + g.idx} branch={g.branch} cwd={g.cwd} />);
    }
    if (ctx) prevCtx = ctx;
    els.push(
      g.compact ? (
        <CompactBlock
          key={g.idx}
          turn={g}
          before={ctxSizeBefore(groups, i)}
          after={ctxSizeAfter(groups, i)}
          repo={caps.repo}
          onOpenFile={caps.openFile}
          workItemRefs={caps.workItemRefs}
          previewLinks={caps.previewLinks}
        />
      ) : (
        <TranscriptTurn
          key={g.idx}
          turn={g}
          caps={caps}
          foldWork={!working || i < liveFrom}
          settled={!turnWorking || i < liveFrom}
          // Keep the work process open on completion ONLY for the live exchange (the reply
          // after the last user prompt): a reader who scrolled up into its streaming tool
          // trace shouldn't have it yanked closed when it folds. Every earlier turn — and
          // crucially any older page the infinite-scroll loader prepends while you're
          // scrolled up (atBottom=false) — must default CLOSED, or it mounts expanded with
          // no click and the reflow jumps the scroll. Only the value at the moment the turn
          // first folds is used; later changes never re-open or re-close it.
          defaultWorkOpen={!autoCollapseWork && i >= liveFrom}
          // Eligible to have the translate button pressed for the reader when it completes —
          // only the reply that was live when the session was first opened and the turns that
          // landed after it (arrivedSinceOpen).
          autoTranslate={arrivedAfter(g)}
        />
      ),
    );
  }
  // Nothing in the transcript is newer than these cards (the normal case right after a
  // session proposes a handoff): they go last — until the next turn arrives.
  for (const c of cards) if (c.insertAt >= groups.length) els.push(c.node);
  // One floating layer for the whole conversation (the selection pill and the mark cards). The
  // marks themselves are painted by each turn over its own body; only document-level controls
  // belong here.
  if (caps.marks) els.push(<MarkLayer key="marklayer" marks={caps.marks} />);
  return <>{els}</>;
}

// The watch boundary of each session, for the life of the browser tab. Kept outside the
// component because the mirror is unmounted on a tab switch (and on any trip to the terminal):
// a boundary held in a ref was re-armed at whatever was on screen on the way back, so a reply
// that finished while the reader looked at another tab — or was still streaming when they came
// back — was never translated. useTranslate's autoFired is what keeps a turn from being pressed
// twice; this only decides which turns may be pressed at all.
//
// `log` and `seen` are what tell a boundary that no longer applies: rows are numbered per jsonl,
// so one armed on another file — or one the newest row has since dropped below (a replaced or
// truncated file, from an Agent that does not name it) — would make that file's history newer
// than the boundary, and every older answer in it would be pressed.
const watchStart = new Map<string, { log: string; boundary: number; seen: number }>();

/**
 * "Did this turn land after the session was opened?" — the boundary that bounds automatic
 * translation (docs/log/97 §97.12, §97.13).
 *
 * It has to be a row number rather than a clock or a mounted flag. A block is named by `idx`, a
 * line ordinal into that session's jsonl, and useStableBlockIds keeps that name fixed while the
 * window it is read through moves (a backward page prepends OLDER, i.e. smaller, rows) — so
 * "newer than the boundary" is exactly "arrived since". Without such a boundary, opening a long
 * session with automatic translation on would press the button once for every foreign-looking
 * answer in the whole history.
 *
 * The boundary is armed once per session and tab, on the first non-empty render, just below the
 * live exchange's first block: that reply is what a reader opening the session comes to read, so
 * it is eligible — still streaming or already finished — and everything above it, including any
 * older page loaded later, is history that is never pressed. Synthetic rows (an optimistic echo
 * at 1e9+, a queued prompt at 2e9+) are not rows of the jsonl: they take no part, including in
 * finding the live exchange, or a window whose prompt is out of view would hand the "live" slot
 * to a queued bubble and file the real reply under history. A block is judged by the real rows
 * it holds (Group.lastRow), since groupTurns folds an echo into the landed prompt before it.
 *
 * Never armed on an empty render: an empty transcript is what a session switch looks like for a
 * paint, and arming there would fix the boundary at -1 (translate everything); nor on a `stale`
 * one. It re-arms when the rows come from another jsonl (`logId`), or when the newest row drops
 * below the newest one seen under this boundary.
 */
function arrivedSinceOpen(groups: Group[], session: string, logId: string, stale: boolean): (g: Group) => boolean {
  // Arming on the outgoing session's rows would store THEIR history as this session's boundary,
  // and it outlives the render: every older answer of this session above it would be pressed.
  if (stale) return () => false;
  // The blocks that hold a row of the file. An echo or a queued prompt alone is not one; folded
  // into a landed prompt's block it does not make that prompt any less landed.
  const rows = groups
    .filter((g) => typeof g.idx === "number" && typeof g.lastRow === "number")
    .map((g) => (g.pending || g.queued ? { ...g, pending: false, queued: false } : g));
  if (rows.length) {
    // The newest ROW, not the newest block id: a block is named after its first row (and
    // useStableBlockIds pins that name), so a reply growing from row 2 to row 1000 stays "2" and
    // a later replaced file numbered 1..8 would not look like a step back.
    let max = -1;
    for (const g of rows) if ((g.lastRow as number) > max) max = g.lastRow as number;
    const held = watchStart.get(session);
    if (!held || held.log !== logId || max < held.seen) {
      const live = rows[liveExchangeFrom(rows)];
      // No reply yet (the prompt is the newest block): whatever comes next is above `max`.
      const boundary = live ? (live.idx as number) - 1 : max;
      watchStart.set(session, { log: logId, boundary, seen: max });
    } else if (max > held.seen) {
      held.seen = max;
    }
  }
  const boundary = watchStart.get(session)?.boundary;
  return (g: Group) => boundary !== undefined && typeof g.idx === "number" && g.idx > boundary;
}

/** Test seam: forget every session's watch boundary, as a fresh page load would. */
export function resetWatchStarts(): void {
  watchStart.clear();
}
