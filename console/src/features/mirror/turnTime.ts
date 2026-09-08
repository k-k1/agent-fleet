// Rules for the timestamp shown on a mirror turn. Kept in its own module so the rule can be
// tested without rendering: the footer used to show when the turn *started* instead of when the
// answer landed.
//
// How transcript rows map onto a turn differs per agent:
//   - claude / codex ... one turn = many rows (thinking, each tool call, the final text). Each
//     row carries its own ts, so the ts of the last folded row is the end of the turn.
//   - opencode / copilot ... one turn = one row (a span). Its ts is only the start, so the Agent
//     attaches endTs (opencode's time.completed / copilot's turn_end).
//   - cursor / kiro / agy ... the assistant side carries no time at all (nothing is shown).
// Either shape folds into "end of a row = endTs if present, else ts".

export interface TurnTimeLike {
  ts?: string;
  endTs?: string;
}

// endOf is the end time of one transcript row. Agents that express a span as a single row carry
// endTs; agents that split a turn across rows do not (the row's own ts is its end).
export function endOf(t: TurnTimeLike): string {
  return t.endTs || t.ts || "";
}

// carryEnd advances the end time as rows are folded into a block. It must not touch the block's
// ts (the first row): chronological insertion of handover cards (chronoInsertIndex) reads the
// start side.
export function carryEnd(block: TurnTimeLike, row: TurnTimeLike): void {
  const end = endOf(row);
  if (end) block.endTs = end;
}

// footTime is the timestamp shown in a block's footer: the end when it is known, otherwise the
// start (an opencode turn still in progress, for instance).
export function footTime(block: TurnTimeLike): string {
  return block.endTs || block.ts || "";
}

// authResolved answers, for a block that died on the agent's login, whether that login has been
// renewed since — the test the mirror's error block uses to stop asking for a fix the reader has
// already made (docs/log/47 §4-11).
//
// It is a comparison of two moments, never a "is the login healthy now" reading: after a
// server-side revocation the local credentials go on looking valid, so the healthy reading is
// true from the first instant and would mark every failure fixed the moment it appeared. Both
// values come from the same Workspace (the Agent stats the credential file, the agent writes the
// transcript), so there is no clock to reconcile.
//
// Unparsable or missing on either side ⇒ false: the block keeps offering the fix. Failing that
// way costs a reader one look at a settings card that turns out to be fine; the other way tells
// them a broken login is dealt with.
export function authResolved(authOkAt: string | undefined, turnEnd: string | undefined): boolean {
  if (!authOkAt || !turnEnd) return false;
  const ok = Date.parse(authOkAt);
  const at = Date.parse(turnEnd);
  return Number.isFinite(ok) && Number.isFinite(at) && ok > at;
}
