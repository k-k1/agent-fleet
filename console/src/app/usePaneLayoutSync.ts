// Keeps the main area's layout mode in step with the paneLayout preference (and with the
// pop-out mode, which forces split while minimal). The preference chooses a profile, not a
// conversion: each profile retains its own tab-local layout so switching never destroys
// terminals or drafts.
import { useEffect, useRef } from "react";
import { confirmDirtyNavigation } from "../features/editor/dirtyRegistry.ts";
import { relabelSingleCell } from "../layout/ops.ts";
import { useLayoutStore } from "../layout/store.ts";
import { layoutModeFor, type PopoutMode } from "../lib/popoutMode.ts";
import { setSetting } from "../lib/settings.ts";

export function usePaneLayoutSync(
  booted: boolean,
  tenant: string,
  paneLayout: "split" | "tabs",
  popout: PopoutMode,
): void {
  const layoutMode = useLayoutStore((s) => s.layout.mode);
  const prevPaneLayoutRef = useRef(paneLayout);
  useEffect(() => {
    const wanted = layoutModeFor(popout, paneLayout);
    const preferenceChanged = prevPaneLayoutRef.current !== paneLayout;
    prevPaneLayoutRef.current = paneLayout;
    // Read the store, not the render's snapshot: App's boot effect has already loaded the
    // preferred mode in this same commit, and a stale `layout.mode` would load it a second time.
    const current = () => useLayoutStore.getState().layout.mode;
    if (!booted || current() === wanted) return;
    // A pop-out tab converts its one pane in place — on Expand, and when Back restores an entry
    // recorded before it — instead of loading the profile, which would replace that pane. A change
    // of the preference itself still switches profiles: each mode keeps its own layout.
    const relabelled = popout && !preferenceChanged ? relabelSingleCell(useLayoutStore.getState().layout, wanted) : null;
    if (relabelled) {
      useLayoutStore.getState().commit(relabelled, false);
      return;
    }
    void confirmDirtyNavigation("layout").then((proceed) => {
      if (current() === wanted) return;
      if (proceed) useLayoutStore.getState().loadMode(tenant, wanted);
      else setSetting("paneLayout", current() === "tabs" ? "tabs" : "split");
    });
  }, [booted, tenant, paneLayout, popout, layoutMode]);
}
