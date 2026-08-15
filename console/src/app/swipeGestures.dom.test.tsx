import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installSwipeGestures, LONG_PRESS_MS } from "./swipeGestures.ts";
import type { SwipeSurfaces } from "./swipeGestures.ts";

// jsdom は TouchEvent を実装していないので、ハンドラが実際に読む形（touches[0] の
// 座標と target）だけを持つイベントを組んで dispatch する。
function touchEvent(type: string, x: number, y: number, target: Element): Event {
  const e = new Event(type, { bubbles: true });
  Object.defineProperty(e, "touches", { value: [{ clientX: x, clientY: y }] });
  target.dispatchEvent(e);
  return e;
}

interface Harness {
  surfaces: SwipeSurfaces;
  calls: string[];
  state: { phone: boolean; coarse: boolean; modal: boolean; drawer: boolean; rail: boolean; rotatable: boolean };
  target: HTMLElement;
  uninstall: () => void;
}

let h: Harness;

function setup(over: Partial<Harness["state"]> = {}): Harness {
  const calls: string[] = [];
  const state = { phone: true, coarse: true, modal: false, drawer: false, rail: false, rotatable: true, ...over };
  const target = document.createElement("div");
  document.body.appendChild(target);
  const surfaces: SwipeSurfaces = {
    phone: () => state.phone,
    coarse: () => state.coarse,
    modal: () => state.modal,
    drawerOpen: () => state.drawer,
    railOpen: () => state.rail,
    rotatable: () => state.rotatable,
    setDrawer: (open) => calls.push(open ? "drawer:open" : "drawer:close"),
    openRailOverlay: () => calls.push("rail:open"),
    closeRail: () => calls.push("rail:close"),
    rotateSession: (delta) => calls.push(delta > 0 ? "rotate:next" : "rotate:prev"),
  };
  const uninstall = installSwipeGestures(window, surfaces);
  return { surfaces, calls, state, target, uninstall };
}

/** 起点 (x,y) から (x+dx, y+dy) へ 1 回で動かす。 */
function swipe(from: [number, number], dx: number, dy = 0): void {
  touchEvent("touchstart", from[0], from[1], h.target);
  touchEvent("touchmove", from[0] + dx, from[1] + dy, h.target);
  touchEvent("touchend", from[0] + dx, from[1] + dy, h.target);
}

beforeEach(() => {
  // 幅は phone 相当（左 1/3 判定は min(innerWidth*0.33, 160)）。
  Object.defineProperty(window, "innerWidth", { value: 390, configurable: true });
});

afterEach(() => {
  h?.uninstall();
  document.body.innerHTML = "";
  vi.useRealTimers();
});

describe("左ペインの出し入れ（従来の挙動）", () => {
  it("スマホ: 左端から → で drawer が開く", () => {
    h = setup();
    swipe([10, 300], 80);
    expect(h.calls).toEqual(["drawer:open"]);
  });

  it("スマホ: drawer が開いていれば ← は閉じる（ローテートしない）", () => {
    h = setup({ drawer: true });
    swipe([200, 300], -120);
    expect(h.calls).toEqual(["drawer:close"]);
  });

  it("スマホ: 左端始まりの → は drawer が優先（前のセッションへは戻さない）", () => {
    h = setup();
    swipe([10, 300], 120);
    expect(h.calls).toEqual(["drawer:open"]);
  });

  it("タブレット（スマホ幅でないタッチ端末）はレールを overlay で出し入れする", () => {
    h = setup({ phone: false, coarse: true });
    Object.defineProperty(window, "innerWidth", { value: 1024, configurable: true });
    swipe([20, 300], 80);
    expect(h.calls).toEqual(["rail:open"]);
    h.state.rail = true;
    swipe([500, 300], -80);
    expect(h.calls).toEqual(["rail:open", "rail:close"]);
  });

  it("マウス機（タッチでない広い画面）は不活性", () => {
    h = setup({ phone: false, coarse: false });
    swipe([10, 300], 80);
    swipe([300, 300], -120);
    expect(h.calls).toEqual([]);
  });

  it("モーダルが出ている間は背後のレールを触らない", () => {
    h = setup({ modal: true });
    swipe([10, 300], 80);
    expect(h.calls).toEqual([]);
  });
});

describe("スマホの横スワイプ＝稼働中セッションのローテート", () => {
  it("drawer が閉じているときの ← で次のセッションへ送る", () => {
    h = setup();
    swipe([300, 300], -120);
    expect(h.calls).toEqual(["rotate:next"]);
  });

  it("左端以外からの → は前のセッションへ戻す", () => {
    h = setup();
    swipe([300, 300], 120);
    expect(h.calls).toEqual(["rotate:prev"]);
  });

  it("左端始まりの ← でも（→ 待ちと両立して）ローテートに落ちる", () => {
    h = setup();
    swipe([10, 300], -120);
    expect(h.calls).toEqual(["rotate:next"]);
  });

  it("drawer が開いていれば → も何もしない（閉じる ← だけを受ける）", () => {
    h = setup({ drawer: true });
    swipe([300, 300], 120);
    expect(h.calls).toEqual([]);
  });

  it("70px に届かない横ぶれでは発火しない（レール開閉の 50px より遠い）", () => {
    h = setup();
    swipe([300, 300], -60);
    expect(h.calls).toEqual([]);
    swipe([300, 300], 60);
    expect(h.calls).toEqual([]);
  });

  it("縦優先: 斜めでも縦の方が大きければスクロールに譲る", () => {
    h = setup();
    swipe([300, 300], -120, -200);
    swipe([300, 300], 120, 200);
    expect(h.calls).toEqual([]);
  });

  it("1 ジェスチャで 1 回だけ（そのまま指を滑らせても連続発火しない）", () => {
    h = setup();
    touchEvent("touchstart", 300, 300, h.target);
    touchEvent("touchmove", 180, 300, h.target);
    touchEvent("touchmove", 40, 300, h.target);
    touchEvent("touchend", 40, 300, h.target);
    expect(h.calls).toEqual(["rotate:next"]);
  });

  it("戻したあと同じ指で反対へ振っても、確定は 1 回きり", () => {
    h = setup();
    touchEvent("touchstart", 300, 300, h.target);
    touchEvent("touchmove", 440, 300, h.target);
    touchEvent("touchmove", 100, 300, h.target);
    touchEvent("touchend", 100, 300, h.target);
    expect(h.calls).toEqual(["rotate:prev"]);
  });

  it("長押しの窓を越えたら候補を取り消す（テキスト選択のドラッグを化けさせない）", () => {
    vi.useFakeTimers();
    h = setup();
    touchEvent("touchstart", 300, 300, h.target);
    vi.advanceTimersByTime(LONG_PRESS_MS + 1);
    touchEvent("touchmove", 150, 300, h.target);
    expect(h.calls).toEqual([]);
  });

  it("横操作を持つ面（入力欄など）が起点なら見送る", () => {
    h = setup();
    const input = document.createElement("textarea");
    h.target.appendChild(input);
    touchEvent("touchstart", 300, 300, input);
    touchEvent("touchmove", 150, 300, input);
    expect(h.calls).toEqual([]);
  });

  it("切り離しタブ（rotatable=false）ではローテートしない", () => {
    h = setup({ rotatable: false });
    swipe([300, 300], -120);
    expect(h.calls).toEqual([]);
  });

  it("タブレットでは ← / → でローテートしない（スマホだけの操作）", () => {
    h = setup({ phone: false, coarse: true });
    swipe([500, 300], -120);
    swipe([500, 300], 120);
    expect(h.calls).toEqual([]);
  });

  it("解除するとイベントを拾わなくなる", () => {
    h = setup();
    h.uninstall();
    swipe([300, 300], -120);
    expect(h.calls).toEqual([]);
  });
});
