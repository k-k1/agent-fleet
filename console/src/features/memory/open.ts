// openMemoryView — the one way in to the Memory pane (#3720): the change list and the
// consolidator's proposals at pane size. The left pane's Memory view, the daily notice and Settings
// call this, so the opening convention lives here. One memory opens through openMemoryEntry.
//
// One Memory view per workspace (sameTarget is the kind alone). A second open therefore has to
// RETARGET the pane that exists: openTarget alone would focus it and silently drop the view the
// caller asked for, which is how a notice for proposals would land on the viewer.
import { useLayoutStore } from "../../layout/store.ts";
import { allViews } from "../../layout/ops.ts";
import type { PaneContent } from "../../layout/types.ts";
import { useSettingsUI } from "../settings/store.ts";

export type MemoryViewKey = "changes" | "proposals";
export const MEMORY_VIEW_KEYS: readonly MemoryViewKey[] = ["changes", "proposals"];

export interface OpenMemoryViewOptions {
  /** The one proposal to show alone (proposals only), as the left pane's Memory view opens it. */
  proposalId?: string;
}

export function openMemoryView(view: MemoryViewKey = "proposals", opts: OpenMemoryViewOptions = {}): void {
  const content: PaneContent =
    view === "proposals" && opts.proposalId
      ? { kind: "memory", memoryView: view, proposalId: opts.proposalId }
      : { kind: "memory", memoryView: view };
  // Settings is a modal over the layout: left open it would hide the pane this call opens.
  useSettingsUI.getState().closeSettings();
  const st = useLayoutStore.getState();
  const existing = allViews(st.layout).find((p) => p.content.kind === "memory");
  if (existing) {
    st.setPaneTarget(existing.id, { content });
    st.selectTab(existing.id);
    return;
  }
  st.openTarget({ content });
}
