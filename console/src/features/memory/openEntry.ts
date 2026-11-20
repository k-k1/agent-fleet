// openMemoryEntry — the one way in to a memory's own pane (ADR 0141 decision 4). The left pane's
// Memory view, the command palette and the "+" button all open through here.
import { useLayoutStore } from "../../layout/store.ts";
import { allViews } from "../../layout/ops.ts";
import type { PaneContent } from "../../layout/types.ts";
import { useSettingsUI } from "../settings/store.ts";

export interface MemoryEntryTarget {
  scope: string;
  project?: { id: string; display?: string };
  /** "" opens a new memory in edit mode. */
  name: string;
}

/** The pane content for a memory, as the layout keys it. */
export function memoryEntryContent(m: MemoryEntryTarget, gone = false): Extract<PaneContent, { kind: "memoryEntry" }> {
  const user = m.scope === "user";
  const display = user ? "" : (m.project?.display ?? "");
  return {
    kind: "memoryEntry",
    scope: user ? "user" : "project",
    project: user ? "" : (m.project?.id ?? ""),
    ...(display ? { projectName: display } : {}),
    name: m.name,
    ...(gone && m.name ? { gone: true } : {}),
  };
}

export function openMemoryEntry(m: MemoryEntryTarget, opts: { gone?: boolean } = {}): void {
  // Settings is a modal over the layout: left open it would hide the pane this call opens.
  useSettingsUI.getState().closeSettings();
  const content = memoryEntryContent(m, !!opts.gone);
  const st = useLayoutStore.getState();
  // A memory forgotten or brought back since its pane was opened is the same memory (sameTarget
  // ignores `gone`): openTarget alone would focus that pane and keep its stale state, so the pane
  // is retargeted in place.
  const existing = content.name
    ? allViews(st.layout).find(
        (p) => p.content.kind === "memoryEntry" && p.content.scope === content.scope && p.content.project === content.project && p.content.name === content.name,
      )
    : undefined;
  if (existing) {
    st.setPaneTarget(existing.id, { content });
    st.selectTab(existing.id);
    return;
  }
  st.openTarget({ content });
}
