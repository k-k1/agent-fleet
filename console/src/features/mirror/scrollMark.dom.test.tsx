// scrollMark（ミラーの表示位置をセッション単位で覚える）の単体テスト。
//
// jsdom にはレイアウトが無いので、スクロール容器とターンの矩形は「スクロール位置の関数」
// として自前で作る（domSetup.ts の注記どおり、実寸そのものは本物のブラウザでしか測れない —
// ここで検証するのは矩形から scrollTop を出す計算のほう）。
import { describe, it, expect, beforeEach } from "vitest";
import { applyMark, captureMark, clearMarks, saveMark, scrollTopForTurn, loadMark } from "./scrollMark.ts";

/** ターン列を持つスクロール容器。矩形は content 座標 − el.scrollTop で作る＝ブラウザと同じ。 */
function fixture(turns: { idx: number; top: number; h: number }[], view = 100, content = 1000): HTMLElement {
  const el = document.createElement("div");
  Object.defineProperty(el, "clientHeight", { value: view, configurable: true });
  Object.defineProperty(el, "scrollHeight", { value: content, configurable: true });
  Object.defineProperty(el, "scrollTop", { value: 0, writable: true, configurable: true });
  el.getBoundingClientRect = () => new DOMRect(0, 0, 200, view);
  for (const t of turns) {
    const d = document.createElement("div");
    d.setAttribute("data-turn-idx", String(t.idx));
    d.getBoundingClientRect = () => new DOMRect(0, t.top - el.scrollTop, 200, t.h);
    el.appendChild(d);
  }
  document.body.appendChild(el);
  return el;
}

const TURNS = [
  { idx: 1, top: 0, h: 200 },
  { idx: 2, top: 200, h: 400 },
  { idx: 3, top: 600, h: 300 },
];

beforeEach(() => {
  document.body.innerHTML = "";
  clearMarks();
});

describe("captureMark", () => {
  it("上端に掛かっているターンと、そのズレを採る", () => {
    const el = fixture(TURNS);
    el.scrollTop = 250; // ターン2 の 50px 目
    expect(captureMark(el, false)).toEqual({ atBottom: false, idx: 2, offset: -50 });
  });

  it("ちょうど境目ならその下のターン（上のターンは 1px も見えていない）", () => {
    const el = fixture(TURNS);
    el.scrollTop = 200;
    expect(captureMark(el, false)).toEqual({ atBottom: false, idx: 2, offset: 0 });
  });

  it("末尾追従していたかをそのまま持つ — 復元するかどうかの判断は呼び手", () => {
    const el = fixture(TURNS);
    expect(captureMark(el, true)?.atBottom).toBe(true);
  });

  it("合成ターン（楽観エコー / キュー済み）はアンカーにしない", () => {
    const el = fixture([{ idx: 1e9 + 3, top: 0, h: 100 }]);
    expect(captureMark(el, false)).toBeNull();
  });

  it("ターンが無ければ null（容器が無いときも）", () => {
    expect(captureMark(fixture([]), false)).toBeNull();
    expect(captureMark(null, false)).toBeNull();
  });
});

describe("applyMark", () => {
  it("採った位置へ戻す — 往復して同じ scrollTop", () => {
    const el = fixture(TURNS);
    el.scrollTop = 250;
    const mark = captureMark(el, false)!;
    el.scrollTop = 0; // セッションを持ち替えて戻ってきた直後の状態
    expect(applyMark(el, mark)).toBe(true);
    expect(el.scrollTop).toBe(250);
  });

  it("アンカーのターンが載っていなければ false（呼び手は末尾へ落とす）", () => {
    const el = fixture(TURNS);
    expect(applyMark(el, { atBottom: false, idx: 99, offset: 0 })).toBe(false);
    expect(el.scrollTop).toBe(0);
  });

  it("スクロール範囲を超えない", () => {
    const el = fixture(TURNS, 100, 1000);
    expect(applyMark(el, { atBottom: false, idx: 3, offset: -5000 })).toBe(true);
    expect(el.scrollTop).toBe(900); // scrollHeight - clientHeight
    expect(applyMark(el, { atBottom: false, idx: 1, offset: 5000 })).toBe(true);
    expect(el.scrollTop).toBe(0);
  });
});

describe("scrollTopForTurn", () => {
  it("そのターンの上端が画面上端に来る位置を返す（余白ぶん引ける）", () => {
    const el = fixture(TURNS);
    el.scrollTop = 850; // 末尾のあたりから頭出しする
    expect(scrollTopForTurn(el, 3)).toBe(600);
    expect(scrollTopForTurn(el, 3, 8)).toBe(592);
  });

  it("載っていないターンは null", () => {
    expect(scrollTopForTurn(fixture(TURNS), 42)).toBeNull();
    expect(scrollTopForTurn(null, 1)).toBeNull();
  });
});

describe("マークの持ち回り", () => {
  it("セッションごとに独立して覚え、null で消える", () => {
    const a = { atBottom: false, idx: 2, offset: -50 };
    saveMark("s-a", a);
    expect(loadMark("s-a")).toEqual(a);
    expect(loadMark("s-b")).toBeNull(); // 他のセッションへは漏れない
    saveMark("s-a", null);
    expect(loadMark("s-a")).toBeNull();
  });

  it("セッション名が空なら何もしない（起動直後のペイン）", () => {
    saveMark("", { atBottom: false, idx: 1, offset: 0 });
    expect(loadMark("")).toBeNull();
  });
});
