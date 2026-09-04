// Lookups the mirror uses to find an already-open pane in the layout. Each reads the store once
// at click time rather than subscribing; a subscription would re-render the whole mirror on every
// layout change. These live here rather than in transcript/ because opening a pane is an owner
// action and the shared view has no local layout to open into.
import { useLayoutStore } from "../../../layout/store.ts";

// findPlanPane returns the id of a pane already reviewing THIS session's plan, if any.
// Read straight from the store (not a subscription): it is consulted at click time,
// and subscribing would re-render the whole mirror on every layout change.
// Stays here rather than in transcript/: opening a pane is an owner action, and the
// shared view has no local layout to open a plan into.
export function findPlanPane(session: string): string | null {
  const layout = useLayoutStore.getState().layout;
  for (const col of layout?.cols || []) {
    for (const cell of col.cells) for (const pane of cell.views) {
      if (pane.content.kind === "doc" && pane.content.docSession === session) return pane.id;
    }
  }
  return null;
}

// findDiffPane returns the id of an already-open captured-edit diff pane. Clicking one
// edit trace after another retargets that single pane instead of spawning one each — the
// same reuse the SCM list does for working diffs (features/scm/open.ts).
export function findDiffPane(): string | null {
  const layout = useLayoutStore.getState().layout;
  for (const col of layout?.cols || []) {
    for (const cell of col.cells) for (const pane of cell.views) {
      if (pane.content.kind === "diff") return pane.id;
    }
  }
  return null;
}

/** findPane reads one pane out of the live layout (same no-subscription rationale). */
export function findPane(id: string) {
  const layout = useLayoutStore.getState().layout;
  for (const col of layout?.cols || []) {
    for (const cell of col.cells) for (const pane of cell.views) if (pane.id === id) return pane;
  }
  return null;
}
