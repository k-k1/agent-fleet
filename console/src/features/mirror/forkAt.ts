// Rules for "fork from here" (docs/log/55): which mirror blocks offer the control, and how many
// exchanges a fork carries over. Kept out of MirrorView because offering it too widely always
// ends in a 400, and a miscount makes the confirmation dialog lie.

// Structurally typed to just what the decision needs; MirrorView's Group is private and this
// logic must not depend on rendering.
export interface BranchableTurn {
  role: string;
  anchorId?: string;
  compact?: boolean;
  pending?: boolean;
  queued?: boolean;
}

// canBranchInSession reports whether forking may be offered in this session at all (per-block
// is canBranchFrom). The condition differs per agent kind: opencode/codex can only be given a
// fork point through the runtime API, so they require managed; claude has no managed driver and
// cuts its own transcript, so it works under the TUI. Requiring managed for everything would
// hide the control for claude forever.
export function canBranchInSession(
  caps: { forkAt: boolean; forkAtManagedOnly: boolean },
  opts: { managed: boolean; readOnly: boolean },
): boolean {
  if (!caps.forkAt || opts.readOnly) return false;
  return !caps.forkAtManagedOnly || opts.managed;
}

// canBranchFrom reports whether this block can be forked from. It must be:
// - a user message (forking from an agent reply changes the meaning; v1 is for re-typing)
// - anchored (pointing at an unanchored block forks the whole conversation instead)
// - neither an unlanded echo nor queued (anchors exist only on landed lines)
// - not a compaction summary (a summary of the conversation, not the conversation)
export function canBranchFrom(t: BranchableTurn): boolean {
  return t.role === "user" && !!t.anchorId && !t.pending && !t.queued && !t.compact;
}

// carriedUserTurns counts the user messages carried into the fork. The fork point itself is not
// carried (the cut is made before it so it can be re-typed), so it is excluded. This is the
// number the confirmation dialog reports.
export function carriedUserTurns(groups: BranchableTurn[], at: BranchableTurn): number {
  const i = groups.indexOf(at);
  if (i < 0) return 0;
  return groups.slice(0, i).filter((g) => g.role === "user" && !g.compact).length;
}
