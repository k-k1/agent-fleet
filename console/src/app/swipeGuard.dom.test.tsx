import { afterEach, describe, expect, it } from "vitest";
import { swipeBlocked } from "./swipeGuard.ts";

// jsdom はレイアウトを計算しないので scrollWidth/clientWidth は常に 0 —
// 「はみ出している」状態は明示的に生やす。
function widths(el: HTMLElement, scrollWidth: number, clientWidth: number): void {
  Object.defineProperty(el, "scrollWidth", { value: scrollWidth, configurable: true });
  Object.defineProperty(el, "clientWidth", { value: clientWidth, configurable: true });
}

function mount(html: string): HTMLElement {
  const host = document.createElement("div");
  host.innerHTML = html;
  document.body.appendChild(host);
  return host;
}

afterEach(() => {
  document.body.innerHTML = "";
});

describe("swipeBlocked", () => {
  it("ふつうの本文の上では横スワイプを通す", () => {
    const host = mount("<div class='mirror'><p id='t'>hello</p></div>");
    expect(swipeBlocked(host.querySelector("#t"))).toBe(false);
  });

  it("Element でない target（window 等）は通す", () => {
    expect(swipeBlocked(null)).toBe(false);
    expect(swipeBlocked(window)).toBe(false);
  });

  it("入力欄・contenteditable の上では見送る（キャレット操作を奪わない）", () => {
    const host = mount(
      "<div><textarea id='ta'></textarea><input id='in'><div id='ce' contenteditable='true'><span id='inner'>x</span></div></div>",
    );
    expect(swipeBlocked(host.querySelector("#ta"))).toBe(true);
    expect(swipeBlocked(host.querySelector("#in"))).toBe(true);
    // 起点が中の要素でも、祖先を辿って編集面だと分かる
    expect(swipeBlocked(host.querySelector("#inner"))).toBe(true);
  });

  it("ブラウザペイン（.browser-stage）の中では見送る — タッチは中の Chromium へ転送している", () => {
    const host = mount("<div class='browser-stage'><canvas id='c'></canvas></div>");
    expect(swipeBlocked(host.querySelector("#c"))).toBe(true);
  });

  it("[data-no-swipe] を付けた面では見送る", () => {
    const host = mount("<div data-no-swipe=''><span id='x'>x</span></div>");
    expect(swipeBlocked(host.querySelector("#x"))).toBe(true);
  });

  it("横スクロールできる祖先（コードブロック等）の中では見送る", () => {
    const host = mount("<pre id='pre' style='overflow-x: auto'><code id='code'>x</code></pre>");
    const pre = host.querySelector("#pre") as HTMLElement;
    widths(pre, 800, 300);
    expect(swipeBlocked(host.querySelector("#code"))).toBe(true);
  });

  it("はみ出していても overflow-x が hidden なら横操作は無いので通す", () => {
    const host = mount("<div id='v' style='overflow-x: hidden'><span id='x'>x</span></div>");
    widths(host.querySelector("#v") as HTMLElement, 800, 300);
    expect(swipeBlocked(host.querySelector("#x"))).toBe(false);
  });

  it("overflow-x: auto でも内容がはみ出していなければ通す（xterm の縦スクロール域など）", () => {
    const host = mount("<div id='v' style='overflow-x: auto'><span id='x'>x</span></div>");
    widths(host.querySelector("#v") as HTMLElement, 300, 300);
    expect(swipeBlocked(host.querySelector("#x"))).toBe(false);
  });
});
