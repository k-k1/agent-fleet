// The zoomed image preview owns the horizontal drag (it pans the image), so it must opt
// out of the phone's swipe-to-rotate — panning a zoomed image used to switch session out
// from under the finger. The pan is a CSS transform, not a scroll container, so
// swipeGuard cannot detect it by measurement; the opt-out is the contract, and it is
// asserted here through swipeBlocked itself rather than by matching the attribute name.
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ImageView } from "./ImageView.tsx";
import { swipeBlocked } from "../../app/swipeGuard.ts";

let host: HTMLDivElement;
let root: Root;

const render = async () => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<ImageView src="/dl/shot.png" alt="shot" />);
  });
  return host.querySelector(".imgview") as HTMLElement;
};

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
