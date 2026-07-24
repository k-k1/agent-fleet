// Layout store (zustand): owns the pane layout + the single commit funnel.
//
// commit() is the ONLY mutation path (ops are pure; actions wrap them). It
// mirrors the layout into browser history (state-only pushState — the URL never
// changes: the Console lives behind a path-stripping proxy, so paths in the URL
// would break reloads) and persists per tenant so a reload restores the split.
// The terminal service subscribes to this store and reconciles xterm instances
// (terminal/service.ts) — no effects scattered through components.
import { create } from "zustand";
import type { Layout, OpenTarget, PaneContent } from "./types.ts";
import * as ops from "./ops.ts";
import { loadStoredLayout, LKEY_NEW } from "./migrate.ts";
import { getTenant, getUser } from "../core/api/client.ts";
import { mobileMatches } from "../lib/device.ts";
import { popoutMode } from "../lib/popoutMode.ts";

interface LayoutStore {
  layout: Layout;
  /** True once load() has run — gates persistence so the initial single-pane
   * render can't clobber a saved layout before it's been read. */
  hydrated: boolean;
  commit(next: Layout, push?: boolean): void;
  /** Restore the tenant's saved split (or reset). Called at boot + tenant switch. */
  load(slug: string): void;
  /** Seed a 1-pane layout from a pop-out descriptor (instead of load()) on the
   * pop-out tab's first boot. Persists immediately so a reload restores it. */
  initSinglePane(content: PaneContent, session: string | null, wrap: boolean | null): void;
  /** popstate: adopt a history-restored layout without pushing/persisting anew. */
  setFromHistory(l: Layout): void;
  // navigation
  openTarget(target: OpenTarget): void;
  openTargetInNew(target: OpenTarget, force?: boolean): void;
  setPaneTarget(paneId: string, target: OpenTarget): void;
  // pane controls
  splitRight(): void;
  splitDown(paneId: string): void;
  closePane(paneId: string, removeOutright?: boolean): void;
  closeSessionPanes(name: string): void;
  swapPanes(aId: string, bId: string): void;
  dropSplit(srcId: string, refId: string, dir: "right" | "down"): void;
  setActive(id: string): void;
  setColRatios(ratios: number[]): void;
  setRowRatio(colId: string, r: number): void;
  setPaneWrap(paneId: string, wrap: boolean | null): void;
  resetToTerminal(): void;
}

function persist(layout: Layout): void {
  const key = LKEY_NEW(getUser(), getTenant());
  const json = JSON.stringify(layout);
  // Per-tab copy: sessionStorage survives this tab's reloads but is private to the
  // tab and GC'd on close, so two tabs of the same account keep different layouts.
  // Shared copy: localStorage is the cross-tab / legacy seed a fresh tab restores
  // from (it holds whatever layout was last active in any tab). loadStoredLayout
  // reads the per-tab copy first, then this one.
  try { sessionStorage.setItem(key, json); } catch {}
  // A minimal pop-out tab is a satellite view — its 1-pane layout must not
  // become the shared seed other/new tabs restore from. Once expanded to a
  // full console ("full") it behaves like any other tab again.
  if (popoutMode() !== "popout") {
    try { localStorage.setItem(key, json); } catch {}
  }
}

export const useLayoutStore = create<LayoutStore>((set, get) => {
  const commit = (next: Layout, push = true): void => {
    const cur = get().layout;
    if (next === cur) return; // ops returned the input — a no-op
    if (push && JSON.stringify(next) === JSON.stringify(cur)) return; // no dup history entry
    set({ layout: next });
    if (get().hydrated) persist(next);
    if (push) {
      try {
        history.pushState({ __af: true, layout: next }, "");
      } catch {}
    }
  };
  return {
    layout: ops.freshLayout(),
    hydrated: false,

    commit,

    load(slug: string) {
      const l = loadStoredLayout(getUser(), slug) || ops.freshLayout();
      set({ layout: l, hydrated: true });
      persist(l);
      try {
        history.replaceState({ __af: true, layout: l }, "");
      } catch {}
    },

    initSinglePane(content, session, wrap) {
      const l = ops.singlePaneLayout(content, session, wrap);
      set({ layout: l, hydrated: true });
      persist(l);
      try {
        history.replaceState({ __af: true, layout: l }, "");
      } catch {}
    },

    setFromHistory(l: Layout) {
      set({ layout: l });
      if (get().hydrated) persist(l);
    },

    openTarget: (target) => commit(ops.openActive(get().layout, target)),
    // Minimal pop-out tabs have exactly one pane by design: "open in a new
    // pane" callers (scm → commit, mirror → doc/file, …) replace in place
    // there — commit() still pushes history, so the browser Back button
    // restores the previous content. Splitting stays available via 展開.
    openTargetInNew: (target, force = false) =>
      commit(
        popoutMode() === "popout"
          ? ops.openActive(get().layout, target)
          : ops.openInNew(get().layout, target, { mobile: mobileMatches(), force }),
      ),
    setPaneTarget: (paneId, target) => commit(ops.setPaneTarget(get().layout, paneId, target), false),

    splitRight: () => commit(ops.splitRight(get().layout)),
    splitDown: (paneId) => commit(ops.splitDown(get().layout, paneId)),
    closePane: (paneId, removeOutright = false) =>
      commit(ops.closePane(get().layout, paneId, removeOutright)),
    closeSessionPanes: (name) => commit(ops.closeSessionPanes(get().layout, name)),
    swapPanes: (aId, bId) => commit(ops.swapPanes(get().layout, aId, bId)),
    dropSplit: (srcId, refId, dir) => commit(ops.dropSplit(get().layout, srcId, refId, dir)),
    // Activation / divider drags aren't history-worthy navigations (no push).
    setActive: (id) => commit(ops.setActive(get().layout, id), false),
    setColRatios: (ratios) => commit(ops.setColRatios(get().layout, ratios), false),
    setRowRatio: (colId, r) => commit(ops.setRowRatio(get().layout, colId, r), false),
    setPaneWrap: (paneId, wrap) => commit(ops.setPaneWrap(get().layout, paneId, wrap), false),
    resetToTerminal: () => commit(ops.freshLayout()),
  };
});

/** wireLayoutHistory installs the popstate listener (browser back/forward
 * restores the layout) and stamps the current entry. Returns the cleanup;
 * called once from the app shell boot effect (StrictMode-safe). */
export function wireLayoutHistory(): () => void {
  const onPop = (e: PopStateEvent) => {
    const l = e.state && e.state.__af && e.state.layout ? (e.state.layout as Layout) : ops.freshLayout();
    useLayoutStore.getState().setFromHistory(l);
  };
  window.addEventListener("popstate", onPop);
  return () => window.removeEventListener("popstate", onPop);
}
