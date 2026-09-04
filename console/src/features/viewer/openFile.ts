// File pane-open helper for callers that name the starting mode — the left
// rail's changed-files row menu (「表示」/「編集」), and anything else that wants more than
// "just show it". The plain "open this file" path stays where it is
// (openTarget with a bare `kind:"file"` content); this only adds the mode.
//
// A file already on screen is RETARGETED in place rather than opened again:
// openTarget's own dedup would activate that pane and silently drop the mode
// (the mode is not part of a pane's identity — see layout/ops.ts sameTarget),
// so choosing edit (「編集」) for a file open in view mode would do nothing visible.
import { useLayoutStore } from "../../layout/store.ts";
import { allPanes } from "../../layout/ops.ts";
import type { PaneContent } from "../../layout/types.ts";

export function openFileMode(filePath: string, mode: "view" | "edit"): void {
  const content: PaneContent = { kind: "file", filePath, mode };
  const st = useLayoutStore.getState();
  const existing = allPanes(st.layout).find((p) => p.content.kind === "file" && p.content.filePath === filePath);
  if (existing) {
    st.setPaneTarget(existing.id, { content });
    st.selectTab(existing.id);
    return;
  }
  st.openTarget({ content });
}
