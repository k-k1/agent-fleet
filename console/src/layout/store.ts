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
import {
  confirmDirtyNavigation,
  dirtyPanesDestroyedByLayout,
} from "../features/editor/dirtyRegistry.ts";

interface LayoutStore {
  layout: Layout;
  /** True once load() has run — gates persistence so the initial single-pane
   * render can't clobber a saved layout before it's been read. */
  hydrated: boolean;
  /** `defer` coalesces only the history-entry restamp into a single trailing call (for divider
   * drags). */
  commit(next: Layout, push?: boolean, defer?: boolean): void;
  /** Action-route commit whose caller must know whether dirty navigation won. */
  commitAction(next: Layout, push?: boolean): Promise<boolean>;
  /** Restore the tenant's saved split (or reset). Called at boot + tenant switch. */
  load(slug: string): void;
  loadMode(slug: string, mode: "split" | "tabs"): void;
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
  selectTab(id: string): void;
  moveTab(id: string, targetPaneId: string, beforeTabId?: string): void;
  dropSplitTab(id: string, refPaneId: string, dir: "right" | "down"): void;
  resetToTerminal(): void;
}

function persist(layout: Layout): void {
  const key = LKEY_NEW(getUser(), getTenant(), layout.mode === "tabs" ? "tabs" : "split");
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

// Divider drags commit every pointermove (setColRatios/setRowRatio), which would mean
// dozens of JSON.stringify + storage writes per second. Debounce the persist on the
// trailing edge; the timer reads the LATEST store state when it fires (never a stale
// capture — a tenant switch mid-debounce just re-persists the new tenant's layout,
// a harmless duplicate). pagehide/beforeunload flush the pending write so closing or
// reloading the tab right after a drag can't lose the layout.
let persistTimer: number | null = null;
function schedulePersist(): void {
  if (persistTimer != null) window.clearTimeout(persistTimer);
  persistTimer = window.setTimeout(() => {
    persistTimer = null;
    persist(useLayoutStore.getState().layout);
  }, 250);
}
function flushPersist(): void {
  if (persistTimer == null) return;
  window.clearTimeout(persistTimer);
  persistTimer = null;
  persist(useLayoutStore.getState().layout);
}
// Checked as a function because the bare-node test shim makes window ~= globalThis, which has no
// addEventListener.
if (typeof window !== "undefined" && typeof window.addEventListener === "function") {
  window.addEventListener("pagehide", flushPersist);
  window.addEventListener("beforeunload", flushPersist);
}

// ── A history entry is a snapshot of the layout ────────────────────────────
// A commit that pushes adds a new entry, but one that does not (tab selection, pane activation,
// wrap, divider drag) moves the layout without adding one. Left alone, the standing entry goes
// stale and the next popstate restores that stale snapshot. Modals push a back-to-close guard
// entry and consume it with history.back() (lib/backClose), so this always surfaced as: open and
// close a modal and the tabbed grid's selected tab is pushed back. Hence a commit that does not
// push restamps the current entry.
//
// The restamp is immediate by default. Coalescing it swaps the target for someone else's entry
// the moment a modal pushes a guard entry during the wait, leaving the original entry stale, i.e.
// exactly the bug this fixes. The one exception is the divider drag, which commits on every
// pointermove and so hits the replaceState rate limits (Safari 100/30s, Firefox 200/10s); only
// there is the restamp coalesced into a single trailing call.
const STAMP_MS = 200;
let stampTimer: number | null = null;

/** Write the layout back into the standing entry. Entries that are not af layout entries (the
 *  backClose guard, for instance) belong to someone else and must not be touched: giving one a
 *  layout turns a stale layout into "the state to restore" when the user lands back on it. */
function stampHistory(l: Layout): void {
  try {
    const st = history.state;
    if (!st || !st.__af || !st.layout) return;
    history.replaceState({ ...st, layout: l }, "");
  } catch {}
}
function cancelDeferredStamp(): void {
  if (stampTimer == null) return;
  window.clearTimeout(stampTimer);
  stampTimer = null;
}
function stampNow(l: Layout): void {
  cancelDeferredStamp();
  stampHistory(l);
}
/** Trailing single restamp, for divider drags only (the timer always reads the latest layout). */
function stampDeferred(): void {
  if (typeof window === "undefined" || typeof window.setTimeout !== "function") return; // bare-node test shim
  if (stampTimer != null) return;
  stampTimer = window.setTimeout(() => {
    stampTimer = null;
    stampHistory(useLayoutStore.getState().layout);
  }, STAMP_MS);
}

export const useLayoutStore = create<LayoutStore>((set, get) => {
  const commitUnchecked = (next: Layout, push = true, defer = false): void => {
    const cur = get().layout;
    if (next === cur) return; // ops returned the input — a no-op
    if (push && JSON.stringify(next) === JSON.stringify(cur)) return; // no dup history entry
    // Leave the state that was on screen on the entry we are about to leave (a coalesced divider
    // position may not have been stamped onto it yet).
    if (push) stampNow(cur);
    set({ layout: next });
    if (get().hydrated) schedulePersist();
    if (push) {
      try {
        history.pushState({ __af: true, layout: next }, "");
      } catch {}
    } else if (defer) {
      stampDeferred();
    } else {
      stampNow(next);
    }
  };
  const commit = (next: Layout, push = true, defer = false): void => {
    const cur = get().layout;
    const destroyed = dirtyPanesDestroyedByLayout(cur, next);
    if (destroyed.length === 0) {
      commitUnchecked(next, push, defer);
      return;
    }
    void confirmDirtyNavigation("layout", destroyed).then((proceed) => {
      if (proceed) commitUnchecked(next, push, defer);
    });
  };
  return {
    layout: ops.freshLayout(),
    hydrated: false,

    commit,

    async commitAction(next: Layout, push = true) {
      const cur = get().layout;
      const destroyed = dirtyPanesDestroyedByLayout(cur, next);
      if (destroyed.length > 0 && !(await confirmDirtyNavigation("layout", destroyed))) return false;
      // A dialog may have stayed open while another action changed the layout.
      // Never apply an action plan calculated from a stale snapshot.
      if (get().layout !== cur) return false;
      commitUnchecked(next, push);
      return true;
    },

    load(slug: string) {
      const l = loadStoredLayout(getUser(), slug) || ops.freshLayout();
      set({ layout: l, hydrated: true });
      persist(l);
      try {
        history.replaceState({ __af: true, layout: l }, "");
      } catch {}
    },

    loadMode(slug, mode) {
      const l = loadStoredLayout(getUser(), slug, mode) || (mode === "tabs" ? ops.freshTabbedLayout() : ops.freshLayout());
      set({ layout: l, hydrated: true });
      persist(l);
      try { history.replaceState({ __af: true, layout: l }, ""); } catch {}
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
      const cur = get().layout;
      const destroyed = dirtyPanesDestroyedByLayout(cur, l);
      if (destroyed.length === 0) {
        set({ layout: l });
        if (get().hydrated) schedulePersist();
        return;
      }
      void confirmDirtyNavigation("history", destroyed).then((proceed) => {
        // Do not apply if another commit landed while the confirm dialog was open, so a stale
        // layout from history cannot overwrite and lose it (the Cancel-side restore push is
        // unnecessary too: the newer commit pushed its own history entry).
        if (get().layout !== cur) return;
        if (proceed) {
          set({ layout: l });
          if (get().hydrated) schedulePersist();
        } else {
          // popstate already moved the browser cursor. Restore the current layout as
          // a fresh entry so Cancel also cancels the visible navigation.
          try { history.pushState({ __af: true, layout: cur }, ""); } catch {}
        }
      });
    },

    openTarget: (target) => commit(ops.openActive(get().layout, target)),
    // Minimal pop-out tabs have exactly one pane by design: "open in a new
    // pane" callers (scm → commit, mirror → doc/file, …) replace in place
    // there — commit() still pushes history, so the browser Back button
    // restores the previous content. Splitting stays available via Expand (「展開」).
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
    // Divider drags arrive on every pointermove, so only the history restamp is coalesced (defer).
    setColRatios: (ratios) => commit(ops.setColRatios(get().layout, ratios), false, true),
    setRowRatio: (colId, r) => commit(ops.setRowRatio(get().layout, colId, r), false, true),
    setPaneWrap: (paneId, wrap) => commit(ops.setPaneWrap(get().layout, paneId, wrap), false),
    selectTab: (id) => commit(ops.selectTab(get().layout, id), false),
    moveTab: (id, targetPaneId, beforeTabId) => commit(ops.moveTab(get().layout, id, targetPaneId, beforeTabId)),
    dropSplitTab: (id, refPaneId, dir) => commit(ops.dropSplitTab(get().layout, id, refPaneId, dir)),
    resetToTerminal: () => commit(ops.resetLayout(get().layout)),
  };
});

/** wireLayoutHistory installs the popstate listener (browser back/forward
 * restores the layout) and stamps the current entry. Returns the cleanup;
 * called once from the app shell boot effect (StrictMode-safe). */
export function wireLayoutHistory(): () => void {
  const onPop = (e: PopStateEvent) => {
    // An entry without a layout belongs to someone else (a modal's back-to-close guard, say).
    // Reading "no layout" as "initial layout" on landing there wiped the whole grid when two
    // stacked modals had only the top one closed. Restore from layout entries only and keep the
    // current layout otherwise.
    if (!e.state || !e.state.__af || !e.state.layout) return;
    useLayoutStore.getState().setFromHistory(e.state.layout as Layout);
  };
  window.addEventListener("popstate", onPop);
  return () => window.removeEventListener("popstate", onPop);
}
