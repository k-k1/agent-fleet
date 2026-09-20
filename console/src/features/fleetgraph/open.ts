// openFleetGraph — the one way in to the fleet session graph pane (ADR 0096 decision 10).
//
// Its own module, apart from the view, for the same reason overview/open.ts and
// imagegen/open.ts are: the keyboard command table imports this, and pulling the view in
// would drag its SVG rendering and CSS into every bundle that has a menu.
//
// `sameTarget` dedupes on the kind alone (layout/ops.ts) — a second open focuses the pane
// that already exists rather than laying a duplicate beside it.
import { useLayoutStore } from "../../layout/store.ts";

export function openFleetGraph(): void {
  useLayoutStore.getState().openTarget({ content: { kind: "fleetgraph", showArchived: true } });
}
