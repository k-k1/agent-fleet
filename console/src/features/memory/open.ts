// openMemoryView — the one way in to the Memory view pane (#3720): the memory viewer, the change
// list and the consolidator's proposals at pane size. Settings, the daily notice and the command
// palette all call this, so the opening convention lives here.
//
// One Memory view per workspace (sameTarget is the kind alone). A second open therefore has to
// RETARGET the pane that exists: openTarget alone would focus it and silently drop the view the
// caller asked for, which is how a notice for proposals would land on the viewer.
import { useLayoutStore } from "../../layout/store.ts";
import { allViews } from "../../layout/ops.ts";
import type { PaneContent } from "../../layout/types.ts";
import { useSettingsUI } from "../settings/store.ts";

export type MemoryViewKey = "memories" | "changes" | "proposals";
export const MEMORY_VIEW_KEYS: readonly MemoryViewKey[] = ["memories", "changes", "proposals"];

export interface OpenMemoryViewOptions {
  /** A memory to pick on arrival, as memoryTarget() spells it. Handed over through the settings
   *  store's one-shot target, which the memory viewer already consumes once its list has landed. */
  target?: string;
}

export function openMemoryView(view: MemoryViewKey = "memories", opts: OpenMemoryViewOptions = {}): void {
  const content: PaneContent = { kind: "memory", memoryView: view };
  // Settings is a modal over the layout: left open it would hide the pane this call opens. Closed
  // first, because closing also drops a pending target.
  useSettingsUI.getState().closeSettings();
  if (opts.target) useSettingsUI.setState({ settingsTarget: opts.target });
  const st = useLayoutStore.getState();
  const existing = allViews(st.layout).find((p) => p.content.kind === "memory");
  if (existing) {
    st.setPaneTarget(existing.id, { content });
    st.selectTab(existing.id);
    return;
  }
  st.openTarget({ content });
}
