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
import { latestWorkPromptIndex } from "../mirrorParts.ts";
import { chronoInsertIndex } from "../handoffPlacement.ts";

export interface TranscriptViewProps {
  groups: Group[];
  caps: TranscriptCaps;
  /** The session is mid-turn: the live exchange keeps its work trace unfolded. */
  working?: boolean;
  /**
   * Fold completed work even for the live exchange. The mirror passes its "am I pinned to
   * the bottom" flag: someone who scrolled up to read a streaming tool trace shouldn't have
   * it yanked closed when the turn completes.
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
  // The current work boundary — INCLUDING a just-sent optimistic echo (pending). If we
  // skipped pending here, sending a new prompt would leave lastUser on the PREVIOUS user
  // turn, so the previous (already-finished) reply counts as "the live exchange" below and
  // its work trace unfolds the moment you hit send. latestWorkPromptIndex treats pending as
  // the boundary (but not un-run queued prompts), which keeps the old reply folded.
  const lastUser = latestWorkPromptIndex(groups);
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
          foldWork={!working || i < lastUser}
          // Keep the work process open on completion ONLY for the live exchange (the reply
          // after the last user prompt): a reader who scrolled up into its streaming tool
          // trace shouldn't have it yanked closed when it folds. Every earlier turn — and
          // crucially any older page the infinite-scroll loader prepends while you're
          // scrolled up (atBottom=false) — must default CLOSED, or it mounts expanded with
          // no click and the reflow jumps the scroll.
          defaultWorkOpen={!autoCollapseWork && i > lastUser}
        />
      ),
    );
  }
  // Nothing in the transcript is newer than these cards (the normal case right after a
  // session proposes a handoff): they go last — until the next turn arrives.
  for (const c of cards) if (c.insertAt >= groups.length) els.push(c.node);
  // 会話ぜんぶで 1 つの浮遊レイヤー（選択ピルとマーカーのカード）。印そのものは各ターンが
  // 自分の本文へ被せる — ここに置くのは document 単位の操作だけ。
  if (caps.marks) els.push(<MarkLayer key="marklayer" marks={caps.marks} />);
  return <>{els}</>;
}
