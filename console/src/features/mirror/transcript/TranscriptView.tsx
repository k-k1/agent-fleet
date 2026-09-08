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
        />
      ) : (
        <TranscriptTurn
          key={g.idx}
          turn={g}
          caps={caps}
          foldWork={!working || i < liveFrom}
          // Keep the work process open on completion ONLY for the live exchange (the reply
          // after the last user prompt): a reader who scrolled up into its streaming tool
          // trace shouldn't have it yanked closed when it folds. Every earlier turn — and
          // crucially any older page the infinite-scroll loader prepends while you're
          // scrolled up (atBottom=false) — must default CLOSED, or it mounts expanded with
          // no click and the reflow jumps the scroll. Only the value at the moment the turn
          // first folds is used; later changes never re-open or re-close it.
          defaultWorkOpen={!autoCollapseWork && i >= liveFrom}
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
