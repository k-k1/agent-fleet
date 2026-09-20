// Linear time → px / row → px mapping (ADR 0096 decision 11, docs/log/101 §101.4).
//
// GraphXOf / GraphLaneY are declared in types/fleetgraph.ts as S-LOGIC exports
// (console/src/lib/fleetgraph.ts). That module does not exist yet (P1 runs its three
// lanes in parallel — docs/log/101 §101.6 "P1 の担当境界"), so this is a same-shape
// stand-in the view owns until P2 integration swaps the import for the real one. The
// formulas are exactly what the frozen contract specifies (linear, decision 8), so the
// swap is expected to be a one-line import change, not a rewrite.
import type { GraphScale, GraphXOf, GraphLaneY } from "../../types/fleetgraph.ts";

export const xOf: GraphXOf = (scale, ts) => {
  const span = scale.to - scale.from;
  if (span <= 0) return 0;
  return ((ts - scale.from) / span) * scale.width;
};

// Row centers, not row tops: every line/arrow this view draws wants the midline.
export const laneY: GraphLaneY = (scale, row) => row * scale.laneH + scale.laneH / 2;

// Clamp a timestamp into the visible window before converting — an open segment's t1
// (or a run that started before the window) must not push the drawn rect past the edge.
export const clampToScale = (scale: GraphScale, ts: number): number =>
  Math.max(scale.from, Math.min(scale.to, ts));
