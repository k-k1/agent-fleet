// The zoomed image preview owns the horizontal drag (it pans the image), so it must opt
// out of the phone's swipe-to-rotate — panning a zoomed image used to switch session out
// from under the finger. The pan is a CSS transform, not a scroll container, so
// swipeGuard cannot detect it by measurement; the opt-out is the contract, and it is
// asserted here through swipeBlocked itself rather than by matching the attribute name.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ImageView } from "./ImageView.tsx";
import { swipeBlocked } from "../../app/swipeGuard.ts";

let host: HTMLDivElement;
let root: Root;

const render = async (props: Partial<Parameters<typeof ImageView>[0]> = {}) => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<ImageView src="/dl/shot.png" alt="shot" {...props} />);
  });
  return host.querySelector(".imgview") as HTMLElement;
};

const shown = () => host.querySelector<HTMLImageElement>(".imgview-img")!;

/** Double-click toggles fit <-> 2.5x, the cheapest way into the zoomed state. */
const doubleClick = async (el: HTMLElement) => {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
  });
};

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
});

describe("ImageView と横スワイプの取り合い", () => {
  it("等倍では横ドラッグを消費しないので、スワイプでのセッション切替を通す", async () => {
    const box = await render();
    expect(box.hasAttribute("data-no-swipe")).toBe(false);
    expect(swipeBlocked(box.querySelector("img"))).toBe(false);
  });

  it("ズームすると、画像の上から始まる横スワイプは見送られる", async () => {
    const box = await render();
    await doubleClick(box);
    expect(box.classList.contains("zoomed")).toBe(true);
    expect(swipeBlocked(box.querySelector("img"))).toBe(true);
  });

  it("等倍に戻すとスワイプはまた通る", async () => {
    const box = await render();
    await doubleClick(box);
    await doubleClick(box);
    expect(box.classList.contains("zoomed")).toBe(false);
    expect(swipeBlocked(box.querySelector("img"))).toBe(false);
  });
});

// An original is megabytes (~1 MB per generated picture here), so enlarging one used to be
// a blank frame for the length of the download. The card's thumbnail is already decoded in
// this tab, so it stands in until the real bytes arrive.
describe("読み込み中の代役", () => {
  // jsdom never loads an image, so the off-screen probe is stubbed: the test decides when
  // "the original arrived".
  const probes: FakeImage[] = [];
  class FakeImage {
    src = "";
    decoding = "";
    complete = false;
    private on: Record<string, (() => void)[]> = {};
    constructor() {
      probes.push(this);
    }
    addEventListener(type: string, fn: () => void) {
      (this.on[type] ||= []).push(fn);
    }
    removeEventListener(type: string, fn: () => void) {
      this.on[type] = (this.on[type] || []).filter((f) => f !== fn);
    }
    fire(type: string) {
      for (const fn of this.on[type] || []) fn();
    }
  }

  beforeEach(() => {
    probes.length = 0;
    vi.stubGlobal("Image", FakeImage);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("原寸が届くまではサムネイルを出し、届いたら差し替える", async () => {
    await render({ placeholder: "/dl/shot.png?thumb=512" });
    expect(shown().getAttribute("src")).toBe("/dl/shot.png?thumb=512");
    expect(shown().className).toContain("placeholder");
    expect(probes[0].src).toBe("/dl/shot.png"); // 原寸は裏で読んでいる

    await act(async () => probes[0].fire("load"));
    expect(shown().getAttribute("src")).toBe("/dl/shot.png");
    expect(shown().className).not.toContain("placeholder");
  });

  it("原寸が壊れていても代役は残す（真っ白にしない）", async () => {
    await render({ placeholder: "/dl/shot.png?thumb=512" });
    await act(async () => probes[0].fire("error"));
    expect(shown().getAttribute("src")).toBe("/dl/shot.png");
  });

  it("代役の寸法を W×H として報告しない（情報バーが縮小版の値になる）", async () => {
    const sizes: { w: number; h: number }[] = [];
    await render({ placeholder: "/dl/shot.png?thumb=512", onLoad: (s) => sizes.push(s) });
    await act(async () => {
      shown().dispatchEvent(new Event("load"));
    });
    expect(sizes).toEqual([]);
  });

  it("代役が無ければ今までどおり原寸をそのまま出す", async () => {
    await render();
    expect(shown().getAttribute("src")).toBe("/dl/shot.png");
    expect(probes).toHaveLength(0);
  });
});
