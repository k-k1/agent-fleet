// Linear time → px / row → px mapping (ADR 0096 decision 11, docs/log/101 §101.4).
//
// GraphXOf / GraphLaneY are declared in types/fleetgraph.ts as S-LOGIC exports
// (console/src/lib/fleetgraph.ts). That module does not exist yet in THIS worktree (P1
// runs its three lanes in parallel, each in its own worktree — docs/log/101 §101.6 "P1
// の担当境界"), so this is a same-shape stand-in the view owns until P2 integration swaps
// the import for the real one — features/fleetgraph/stubRemoval.test.ts fails the moment
// lib/fleetgraph.ts exists on disk, so the swap cannot be forgotten silently.
//
// The formulas below match S-LOGIC's own (confirmed against lib/fleetgraph.ts in review):
// laneY returns the ROW'S TOP, never its center — callers that want the midline add
// scale.laneH / 2 themselves (FleetGraphView.tsx's topY) — and xOf clamps its input into
// the window before converting, so a timestamp outside [from, to] never produces a
// pixel outside [0, width]. Getting either of these wrong is silent: the render still
// looks plausible, just measurably off (this file's first version had both wrong).
import type { GraphXOf, GraphLaneY } from "../../types/fleetgraph.ts";

export const xOf: GraphXOf = (scale, ts) => {
  const span = scale.to - scale.from;
  if (span <= 0) return 0;
  const clamped = Math.max(scale.from, Math.min(scale.to, ts));
  return ((clamped - scale.from) / span) * scale.width;
};

export const laneY: GraphLaneY = (scale, row) => row * scale.laneH;
