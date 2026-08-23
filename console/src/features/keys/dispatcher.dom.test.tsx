// 日常操作の Alt 直キーが「実イベント → chord 正規化 → コマンド実行」まで通ることを、
// 本物のディスパッチャ（wireKeys の capture リスナ）に本物の KeyboardEvent を投げて確かめる。
//
// commands.test.ts はレジストリ DATA の不変条件（重複・予約衝突）を見るだけで、`.code` が
// 意図した chord に化けるか（Comma → ","、PageDown → "pagedown"、Shift の順序）や、`when`
// ゲートが閉じているときに**キーを握らず端末へ流す**か、までは分からない。そこが本番で効く
// 差なので、ここで押さえる。
//
// 記録するのは layout ストアへの副作用だけ（新規セッション等はネットワークに触るので対象外）。
import { describe, expect, it, beforeEach, afterEach } from "vitest";
import { useLayoutStore } from "../../layout/store.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import type { Layout } from "../../layout/types.ts";
import { wireKeys } from "./dispatcher.ts";
import { effectiveCommands } from "./bindings.ts";
import { matchDirect } from "../../lib/keys/registry.ts";
import { buildContext } from "./dispatcher.ts";

const view = (id: string, session: string) => ({
  id,
  session,
  content: { kind: "terminal" as const, chat: false },
  wrap: null,
});

/** タブモード: セル g0 が 3 枚、g1 が 1 枚。 */
const tabsLayout = (): Layout => ({
  version: 3,
  mode: "tabs",
  cols: [
    {
      id: "c0",
      rowRatio: 0.5,
      cells: [
        { id: "g0", selectedViewId: "p0", views: [view("p0", "alpha"), view("p1", "beta"), view("p2", "gamma")] },
        { id: "g1", selectedViewId: "p9", views: [view("p9", "solo")] },
      ],
    },
  ],
  colRatios: [1],
  activeCellId: "g0",
});

/** 実キーを capture リスナへ。preventDefault されたか＝アプリが握ったか を返す。 */
const press = (code: string, mods: { alt?: boolean; shift?: boolean; mod?: boolean } = {}): boolean => {
  const e = new KeyboardEvent("keydown", {
    code,
    key: code,
    altKey: !!mods.alt,
    shiftKey: !!mods.shift,
    ctrlKey: !!mods.mod,
    bubbles: true,
    cancelable: true,
  });
  window.dispatchEvent(e);
  return e.defaultPrevented;
};

const layout = () => useLayoutStore.getState().layout;
const viewIds = (): string[] => layout().cols.flatMap((c) => c.cells.flatMap((g) => g.views.map((v) => v.id)));
const selected = (cellId: string): string | null =>
  layout().cols[0].cells.find((c) => c.id === cellId)?.selectedViewId ?? null;

describe("Alt accelerators (real dispatcher, real KeyboardEvents)", () => {
  let unwire: (() => void) | null = null;
  beforeEach(() => {
    useLayoutStore.setState({ layout: tabsLayout() });
    unwire = wireKeys();
  });
  afterEach(() => {
    unwire?.();
    unwire = null;
  });

  it("Alt+W closes only the ACTIVE TAB — not the whole cell (the bug this fixes)", () => {
    expect(press("KeyW", { alt: true })).toBe(true);
    expect(viewIds()).toEqual(["p1", "p2", "p9"]);
    // 残ったタブが選択され、セルは生きたまま。
    expect(selected("g0")).toBe("p1");
  });

  it("Alt+Shift+W is a DIFFERENT chord and closes everything", () => {
    expect(press("KeyW", { alt: true, shift: true })).toBe(true);
    expect(viewIds()).toEqual([]);
    expect(layout().cols[0].cells).toHaveLength(1);
  });

  it("Alt+PageDown / Alt+PageUp cycle tabs inside the active cell, wrapping", () => {
    expect(press("PageDown", { alt: true })).toBe(true);
    expect(selected("g0")).toBe("p1");
    expect(press("PageDown", { alt: true })).toBe(true);
    expect(press("PageDown", { alt: true })).toBe(true);
    expect(selected("g0")).toBe("p0"); // wrapped
    expect(press("PageUp", { alt: true })).toBe(true);
    expect(selected("g0")).toBe("p2");
  });

  it("leaves Alt+PageDown to the terminal when the active cell has nothing to cycle", () => {
    useLayoutStore.setState({ layout: { ...tabsLayout(), activeCellId: "g1" } });
    expect(press("PageDown", { alt: true })).toBe(false); // 未登録扱い＝素通し
    expect(selected("g1")).toBe("p9");
  });

  it("resolves the punctuation and letter accelerators to the intended commands", () => {
    // run() が外部に触れるコマンド（設定・メモ・読み上げ…）は、実行せず照合だけ見る。
    // レールの絞り込みはワークスペース起動中しか描画されないので、そのゲートも一緒に見る。
    const ctxNow = { ...buildContext(), region: "main" as const, focusedKind: "other" as const };
    expect(matchDirect(effectiveCommands(), "alt+/", ctxNow)).toBeUndefined(); // 停止中は握らない
    useWorkspaceStore.setState({ state: "running" });

    const cases: [string, boolean, string][] = [
      ["Comma", false, "settings.open"],
      ["Slash", false, "rail.filter"],
      ["KeyN", false, "session.new"],
      ["KeyA", false, "memo.add"],
      ["KeyG", false, "workspace.workingSet"],
      ["KeyQ", false, "tts.toggle"],
      ["KeyZ", false, "viewer.wrap"],
      ["BracketLeft", false, "pane.prev"],
      ["BracketRight", false, "pane.next"],
    ];
    const ctx = { ...buildContext(), region: "main" as const, focusedKind: "other" as const };
    for (const [code, shift, id] of cases) {
      const chord = "alt+" + (shift ? "shift+" : "") + chordKey(code);
      const cmd = matchDirect(effectiveCommands(), chord, ctx);
      expect(`${chord} → ${cmd?.id ?? "(none)"}`).toBe(`${chord} → ${id}`);
    }
  });
});

// `.code` → 正規 chord のベースキー（chords.ts の codeToKey と同じ規則。ここでは
// 期待値の組み立てに使うだけなので、テスト側で素直に書き下す）。
function chordKey(code: string): string {
  if (code === "Comma") return ",";
  if (code === "Slash") return "/";
  if (code === "BracketLeft") return "[";
  if (code === "BracketRight") return "]";
  return code.replace(/^Key/, "").toLowerCase();
}
