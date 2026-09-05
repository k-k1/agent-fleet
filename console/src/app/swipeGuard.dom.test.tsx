import { afterEach, describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { swipeBlocked } from "./swipeGuard.ts";

// jsdom computes no layout, so scrollWidth/clientWidth are always 0 — the "overflowing"
// state has to be planted explicitly.
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
    // Even when the gesture starts on an inner element, the ancestor walk finds the
    // editing surface
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

  // Regression guard for "swipe is dead on this one session". In a real browser an
  // element that only sets `overflow-y: auto` still computes overflow-x to "auto" (CSS:
  // if one axis is visible and the other is not, visible becomes auto), so a single
  // unbreakable long string overflowing the transcript by a few tens of px made the whole
  // transcript unresponsive.
  it("[data-swipe-y] を宣言した縦スクロール容器は、横へはみ出していても通す", () => {
    const host = mount(
      "<div id='body' data-swipe-y='' style='overflow-y: auto'>" +
        "<div class='mirror-scroll'><p id='t'>ふつうの段落</p></div></div>",
    );
    const body = host.querySelector("#body") as HTMLElement;
    // jsdom's getComputedStyle does not propagate an overflow-y-only declaration to
    // overflow-x, so reproduce the value measured in a real browser (auto) here.
    body.style.overflowX = "auto";
    widths(body, 633, 390); // measured on a 390px-wide device, transcript with one sha256
    expect(swipeBlocked(host.querySelector("#t"))).toBe(false);
  });

  it("[data-swipe-y] の中でも、本物の横スクローラの上では見送る", () => {
    const host = mount(
      "<div id='body' data-swipe-y='' style='overflow-y: auto'>" +
        "<pre id='pre' style='overflow-x: auto'><code id='code'>x</code></pre></div>",
    );
    const body = host.querySelector("#body") as HTMLElement;
    body.style.overflowX = "auto";
    widths(body, 633, 390);
    widths(host.querySelector("#pre") as HTMLElement, 800, 390);
    expect(swipeBlocked(host.querySelector("#code"))).toBe(true);
  });
});

// The attribute name is a bare string contract like data-no-swipe: forgetting it breaks
// nothing loudly, it just brings back "swipe is dead on this one session". So check where
// it is applied.
describe("data-swipe-y の付け先", () => {
  const read = (rel: string) =>
    readFileSync(fileURLToPath(new URL(rel, import.meta.url)), "utf8");
  const cases: [string, string, string][] = [
    ["ミラーの転写", "../features/mirror/MirrorView.tsx", "mirror-body"],
    ["共有ビューの転写", "../features/sharing/SharedSessionView.tsx", "shared-view-body"],
    ["アシスタントチャット", "../features/chat/ChatView.tsx", "chat-scroll"],
  ];
  for (const [name, file, cls] of cases) {
    it(`${name}（.${cls}）に付いている`, () => {
      const src = read(file);
      const at = src.indexOf(`"${cls}"`);
      expect(at, `${file} に .${cls} が見つからない`).toBeGreaterThan(-1);
      // The declaration must sit in the same element's attribute list (up to the next >).
      expect(src.slice(at, src.indexOf(">", at))).toContain("data-swipe-y");
    });
  }
});
