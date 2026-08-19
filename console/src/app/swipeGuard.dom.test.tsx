import { afterEach, describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
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

  // ここが「特定のセッションだけスワイプが効かない」の再発防止。実ブラウザでは
  // `overflow-y: auto` だけ指定した要素でも overflow-x の計算値が "auto" になるので
  // （CSS: 片方が visible なら visible → auto）、転写に折り返せない長い文字列が
  // 1 つ混ざって数十 px はみ出しただけで、転写全体が不感になっていた。
  it("[data-swipe-y] を宣言した縦スクロール容器は、横へはみ出していても通す", () => {
    const host = mount(
      "<div id='body' data-swipe-y='' style='overflow-y: auto'>" +
        "<div class='mirror-scroll'><p id='t'>ふつうの段落</p></div></div>",
    );
    const body = host.querySelector("#body") as HTMLElement;
    // jsdom の getComputedStyle は overflow-y だけの指定を overflow-x へ波及させないため、
    // 実ブラウザで測った計算値（auto）をここで再現する。
    body.style.overflowX = "auto";
    widths(body, 633, 390); // 幅 390px の実機相当（sha256 が 1 つ混ざった転写の実測値）
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

// 属性名は data-no-swipe と同じ文字列契約で、付け忘れても何も壊れず「そのセッションだけ
// スワイプが効かない」に戻るだけなので、付け先を突き合わせておく。
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
      // クラス名から同じ要素の属性列（次の > まで）に宣言があること。
      expect(src.slice(at, src.indexOf(">", at))).toContain("data-swipe-y");
    });
  }
});
