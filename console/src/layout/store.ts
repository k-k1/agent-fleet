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
  /** `defer` は history entry の貼り直しだけを末尾 1 回に間引く（仕切りドラッグ用）。 */
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
// bare-node のテスト shim は window≒globalThis で addEventListener を持たないため関数チェック。
if (typeof window !== "undefined" && typeof window.addEventListener === "function") {
  window.addEventListener("pagehide", flushPersist);
  window.addEventListener("beforeunload", flushPersist);
}

// ── history entry は layout の「スナップショット」 ──────────────────────────
// push する commit は新しい entry を積むが、積まない commit（タブ選択・ペイン活性・
// 折返し・仕切りドラッグ）は entry を増やさずに layout を動かす。放置すると今立って
// いる entry が古いままになり、次の popstate がその古いスナップショットを復元する。
// モーダルは「戻るで閉じる」guard entry を積み、閉じるときに history.back() でそれを
// 消費する（lib/backClose）ので、モーダルを開いて閉じただけでタブ付きグリッドの選択
// タブが押し戻される、という形で必ず表に出ていた。だから push しない commit のあとは
// 今の entry を貼り直す。
//
// 貼り直しは即時が既定。「あとでまとめて」にすると、その待ち時間の中でモーダルが
// guard entry を積んだ瞬間に貼り直し先が他人の entry へすり替わり、元の entry が古い
// まま取り残される（＝直そうとしたバグがそのまま残る）。例外は仕切りドラッグだけで、
// これは pointermove ごとに commit するため replaceState の回数制限（Safari 100回/
// 30秒・Firefox 200回/10秒）に当たる。そこだけ末尾 1 回に間引く。
const STAMP_MS = 200;
let stampTimer: number | null = null;

/** 今立っている entry に layout を書き戻す。af の layout entry 以外（backClose の
 *  guard など）は他人の entry なので触らない — layout を持たせてしまうと、その entry
 *  へ戻ったときに古い layout が「復元すべき状態」に化ける。 */
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
/** 仕切りドラッグ専用の末尾 1 回（タイマーは常に最新の layout を読む）。 */
function stampDeferred(): void {
  if (typeof window === "undefined" || typeof window.setTimeout !== "function") return; // bare-node のテスト shim
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
    // これから離れる entry には「画面に出ていた状態」を残す（間引かれた仕切り位置が
    // まだ乗っていないことがある）。
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
        // 確認ダイアログ待ちの間に別の commit が挟まっていたら適用しない — 履歴由来の
        // 古い layout でその commit を上書き喪失しないため（Cancel 側の復元 push も、
        // 新しい commit が自分の履歴 entry を積んでいるので不要）。
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
    // 仕切りドラッグは pointermove ごとに来るので history の貼り直しだけ間引く（defer）。
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
    // layout を持たない entry は他人のもの（モーダルの「戻るで閉じる」guard など）。
    // そこへ着地したときに「layout 無し＝初期レイアウト」と解釈すると、モーダルを 2 枚
    // 重ねて上だけ閉じただけでグリッドが丸ごと消えていた。復元するのは layout entry
    // だけにして、それ以外は今の layout を維持する。
    if (!e.state || !e.state.__af || !e.state.layout) return;
    useLayoutStore.getState().setFromHistory(e.state.layout as Layout);
  };
  window.addEventListener("popstate", onPop);
  return () => window.removeEventListener("popstate", onPop);
}
