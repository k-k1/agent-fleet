// Operator-interaction graph (docs/log/44 / ADR0027) — FROZEN CONTRACT (Phase 0).
//
// This file is types-only and import-only. It is the seam the three parallel
// implementation sessions build against independently:
//   - S-BE   emits `DispatchList` from GET /api/chat/conversations/{id}/dispatches
//   - S-LOGIC implements `BuildSeqModel` in console/src/lib/opgraph.ts
//   - S-VIEW  renders a `SeqModel` in features/opgraph/OperatorGraphView.tsx
// Keep it body-free (no runtime code) so it compiles standalone and nothing here
// forces edits to the shared pane wiring (that lives wholly in S-VIEW).

import type { Conversation } from "./chat.ts";
import type { Session, SessionKind, SessionState } from "./session.ts";

// ── REST DTO ────────────────────────────────────────────────────────────────
// One outbound instruction from the operator to a session, recorded server-side
// where armSessionReport() already fires (the "dispatch ledger", docs/log/44 §backend).
// The return direction (session → operator) is NOT here — those are the operator
// conversation's role:"report" messages, already fetched via chatGet().
export interface DispatchEntry {
  ts: number; // unix millis the instruction was sent
  session: string; // target session name (Session.name)
  sessionKind: SessionKind; // target session's agent kind (for lane color)
  kind: "launch" | "instruct"; // create_session vs send_to_session
  dir?: string; // working dir the launch targeted (launch only)
  excerpt?: string; // ≤140 chars, single line, DISPLAY-ONLY (never executed)
}

// Body of GET /api/chat/conversations/{id}/dispatches.
export interface DispatchList {
  dispatches: DispatchEntry[];
}

// ── Sequence-diagram render model (buildSeqModel output) ─────────────────────
// "operator" is the reserved id of the operator lifeline; every other id is a
// Session.name.
export type SeqParticipantId = string;

export interface SeqParticipant {
  id: SeqParticipantId; // "operator" | session name
  lane: number; // 0 = operator, 1.. = sessions in first-seen order
  kind?: SessionKind; // undefined for the operator lane
  label: string; // display name for the lifeline head
  state?: SessionState | "exit" | string; // current live state (session lanes)
  alive?: boolean; // session still running (session lanes)
}

export type SeqArrowVariant = "dispatch" | "report";

// A horizontal message arrow between two lifelines at a laid-out vertical y (px).
// dispatch = solid → (operator→session); report = dashed ⟵ (session→operator).
export interface SeqArrow {
  ts: number;
  from: SeqParticipantId;
  to: SeqParticipantId;
  variant: SeqArrowVariant;
  dispatchKind?: "launch" | "instruct"; // variant === "dispatch"
  reportKind?: "answer-ready" | "exit"; // variant === "report"
  exitReason?: string; // oom | crashed | killed (report + exit — render red)
  label?: string; // excerpt / short caption beside the arrow
  y: number; // laid-out vertical px of the arrow line
}

// A session's busy span (dispatch → matching report). y1 === null means the span
// is still open — the view pulses it. `live` reflects the live sessions map, so a
// span can be open-in-history yet not currently live (e.g. runtime lost).
export interface SeqActivation {
  participant: SeqParticipantId;
  kind?: SessionKind;
  y0: number;
  y1: number | null; // null = ongoing
  live: boolean;
}

export interface SeqModel {
  participants: SeqParticipant[];
  arrows: SeqArrow[];
  activations: SeqActivation[];
  height: number; // total laid-out content height (px)
  laneW: number; // horizontal spacing between lifelines (px)
  laneX: (lane: number) => number; // lane index → lifeline x (px)
}

export interface BuildSeqOptions {
  rowH?: number; // vertical px per event row
  laneW?: number; // horizontal px per lane
  topPad?: number; // reserve above the first event for the lifeline heads
}

// Implemented by S-LOGIC in console/src/lib/opgraph.ts. Merges the operator
// conversation (user/assistant/report messages), the dispatch ledger, and the
// live sessions map (name → Session) into a laid-out sequence-diagram model.
// `conv` may be null while the first load is in flight.
export type BuildSeqModel = (
  conv: Conversation | null,
  dispatches: DispatchEntry[],
  sessions: Map<string, Session>,
  opts?: BuildSeqOptions,
) => SeqModel;
