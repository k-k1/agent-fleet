// 返信サジェストのチップ行は「条件付きレンダー」で、ストリーミング中（チャット）や
// AUQ/plan でコンポーザーがロックされている間（ミラー）は DOM から消えて、また戻ってくる。
// ref オブジェクトを見るだけの effect は再マウントを検知できない（ref の代入は再レンダーも
// effect も起こさない）ので、戻ってきた新しい要素にはリスナーが付かず、ホイール横スクロールが
// 死ぬ。ここではその「消えて戻る」を再現して、戻ったあとも効くことを保証する。
import { describe, it, expect, afterEach } from "vitest";
import { useRef } from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useDragScroll } from "./dragScroll.ts";

// jsdom はレイアウトを持たないので、あふれている行を自前で用意する（300px の窓に 900px の中身）。
function makeOverflowing(el: HTMLElement) {
  Object.defineProperty(el, "scrollWidth", { value: 900, configurable: true });
  Object.defineProperty(el, "clientWidth", { value: 300, configurable: true });
  let left = 0;
  Object.defineProperty(el, "scrollLeft", {
    configurable: true,
    get: () => left,
    set: (v: number) => {
      left = v;
    },
  });
}

function Harness({ show }: { show: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const attach = useDragScroll(ref);
  return show ? <div data-testid="row" ref={attach} /> : null;
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

function mount(show: boolean) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<Harness show={show} />));
}

function render(show: boolean) {
  act(() => root!.render(<Harness show={show} />));
}

function row() {
  const el = host!.querySelector<HTMLDivElement>('[data-testid="row"]');
  if (!el) throw new Error("row not rendered");
  return el;
}

function wheel(el: HTMLElement, deltaY: number) {
  el.dispatchEvent(new WheelEvent("wheel", { deltaY, bubbles: true, cancelable: true }));
}

describe("useDragScroll", () => {
  it("turns a vertical wheel into horizontal scroll", () => {
    mount(true);
    const el = row();
    makeOverflowing(el);
    wheel(el, 120);
    expect(el.scrollLeft).toBe(120);
  });

  it("still works after the row unmounts and comes back (streaming / composer lock)", () => {
    mount(true);
    makeOverflowing(row());
    render(false); // ストリーミング中などで行が消える
    render(true); // 戻ってくる（＝別の DOM 要素）
    const el = row();
    makeOverflowing(el);
    wheel(el, 120);
    expect(el.scrollLeft).toBe(120);
  });

  it("attaches even when the row is absent on first mount", () => {
    mount(false); // 初回は行が無い（ロック中に開いた等）
    render(true);
    const el = row();
    makeOverflowing(el);
    wheel(el, 120);
    expect(el.scrollLeft).toBe(120);
  });
});
