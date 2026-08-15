// History ↔ layout の同期。history entry は layout の「スナップショット」なので、
// entry を積まない commit（タブ選択・ペイン活性・折返し・仕切りドラッグ）のあとは
// 立っている entry を貼り直さないと、次の popstate が古いスナップショットを復元する。
// モーダルは閉じるために history.back() を撃つ（lib/backClose の戻るで閉じる guard）
// ので、「モーダルを開いて閉じるとタブが勝手に切り替わる」形で必ず表に出る。
import { describe, expect, it, beforeEach } from "vitest";
import { useLayoutStore, wireLayoutHistory } from "./store.ts";
import type { Layout } from "./types.ts";

const tabbedLayout = (selected: string): Layout => ({
  version: 3,
  mode: "tabs",
  cols: [{
    id: "c0",
    rowRatio: 0.5,
    cells: [{
      id: "g0",
      selectedViewId: selected,
      views: [
        { id: "p0", session: "alpha", content: { kind: "terminal", chat: false }, wrap: null },
        { id: "p1", session: "beta", content: { kind: "terminal", chat: false }, wrap: null },
      ],
    }],
  }],
  colRatios: [1],
  activeCellId: "g0",
});

const selectedTab = (): string | null =>
  useLayoutStore.getState().layout.cols[0].cells[0].selectedViewId;

/** popstate は jsdom でも非同期に届く（history.back() はタスクとして queue される）。 */
const back = (): Promise<void> =>
  new Promise((resolve) => {
    window.addEventListener("popstate", () => setTimeout(resolve, 0), { once: true });
    history.back();
  });

/** モーダルの「戻るで閉じる」guard（lib/backClose が積むのと同じ entry）。 */
const openModal = (): void => {
  history.pushState({ __af: true, afModal: true }, "");
};

describe("layout history", () => {
  let unwire: (() => void) | null = null;

  beforeEach(() => {
    unwire?.();
    const l = tabbedLayout("p0");
    useLayoutStore.setState({ layout: l, hydrated: true });
    history.replaceState({ __af: true, layout: l }, "");
    unwire = wireLayoutHistory();
    return () => {
      unwire?.();
      unwire = null;
    };
  });

  it("keeps the selected tab when a modal is opened and closed", async () => {
    useLayoutStore.getState().selectTab("p1");
    expect(selectedTab()).toBe("p1");

    openModal();
    await back(); // ✕ / Esc / 背景クリックで閉じる = guard entry を戻るで消費

    expect(selectedTab()).toBe("p1");
  });

  it("keeps the selected tab when the modal opens right after the tab switch", async () => {
    useLayoutStore.getState().selectTab("p1");
    openModal();
    await back();

    expect(selectedTab()).toBe("p1");
  });

  it("does not reset the layout when popping onto a modal guard entry", async () => {
    // モーダルが 2 枚重なった状態から上だけ閉じる: 着地先の guard entry は layout を
    // 持たないので、そこを「layout 無し = 初期レイアウト」と解釈すると全部消える。
    openModal();
    openModal();
    await back();

    expect(useLayoutStore.getState().layout.cols[0].cells[0].views).toHaveLength(2);
    expect(selectedTab()).toBe("p0");
  });

  it("restores the tab that was on screen when the browser back button is used", async () => {
    // モーダルに限らない: 「entry を積む操作」が置き去りにする entry も同じく古い。
    useLayoutStore.getState().selectTab("p1");
    useLayoutStore.getState().splitRight();
    await back(); // 分割を戻る → 直前に画面へ出ていた状態（p1 選択）へ

    expect(useLayoutStore.getState().layout.cols).toHaveLength(1);
    expect(selectedTab()).toBe("p1");
  });
});
