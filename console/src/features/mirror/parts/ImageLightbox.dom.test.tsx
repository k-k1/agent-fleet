// The lightbox used to close on any click, which is why enlarging an image could never
// zoom: the first half of a double-click closed it. Zoom now lives in the shared
// ImageView, so what has to hold is the split — the image keeps its clicks (and its pan,
// which ends wherever the finger lifts), the backdrop and the controls close.
//
// The bar's buttons are addressed by position, not by their title: the titles come from
// the i18n catalogue and a test that matches on them fails the day a label is reworded.
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ImageLightbox } from "./ImageLightbox.tsx";

let host: HTMLDivElement;
let root: Root;
let closed: number;

const render = async () => {
  closed = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<ImageLightbox src="blob:shot" onClose={() => closed++} />);
  });
  return host.querySelector(".mirror-lightbox") as HTMLElement;
};

const box = () => host.querySelector(".imgview") as HTMLElement;
const img = () => host.querySelector(".imgview-img") as HTMLElement;
const level = () => host.querySelector(".mirror-lightbox-level") as HTMLButtonElement;
const bar = () => [...host.querySelectorAll<HTMLButtonElement>(".mirror-lightbox-bar button")];
const zoomOut = () => bar()[0];
const zoomIn = () => bar()[2];
const close = () => bar()[3];

/** A press and release at the same point: what a plain click is. */
const clickAt = async (el: HTMLElement, x = 10, y = 10) => {
  await act(async () => {
    el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, pointerId: 1, clientX: x, clientY: y }));
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, clientX: x, clientY: y }));
  });
};

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
});

describe("ミラーの拡大表示（ライトボックス）", () => {
  it("画像の外（背景）のクリックで閉じる", async () => {
    await render();
    await clickAt(box());
    expect(closed).toBe(1);
  });

  it("画像そのもののクリックでは閉じない（ダブルクリックの拡大が届かなくなるため）", async () => {
    await render();
    await clickAt(img());
    expect(closed).toBe(0);
  });

  it("パンして背景の上で指を離しても閉じない", async () => {
    await render();
    await act(async () => {
      img().dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, pointerId: 1, clientX: 200, clientY: 200 }));
      box().dispatchEvent(new MouseEvent("click", { bubbles: true, clientX: 260, clientY: 210 }));
    });
    expect(closed).toBe(0);
  });

  it("＋／−で拡大縮小し、％ボタンでフィットに戻る", async () => {
    await render();
    expect(level().textContent).toBe("100%");
    expect(zoomOut().disabled).toBe(true); // fit is the floor; nothing to zoom out to

    await clickAt(zoomIn());
    expect(box().classList.contains("zoomed")).toBe(true);
    expect(level().textContent).toBe("140%");
    expect(closed).toBe(0); // the controls are not the backdrop

    await clickAt(zoomIn());
    expect(level().textContent).toBe("196%");
    await clickAt(zoomOut());
    expect(level().textContent).toBe("140%");

    await clickAt(level());
    expect(level().textContent).toBe("100%");
    expect(box().classList.contains("zoomed")).toBe(false);
    expect(closed).toBe(0);
  });

  it("ダブルクリックで拡大し、バーの表示もそれに追従する", async () => {
    await render();
    await act(async () => {
      img().dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    });
    expect(level().textContent).toBe("250%");
  });

  it("✕ と Escape で閉じる", async () => {
    await render();
    await clickAt(close());
    expect(closed).toBe(1);
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(closed).toBe(2);
  });

  it("開いている間はスワイプでのセッション切替を見送らせる", async () => {
    const overlay = await render();
    expect(overlay.hasAttribute("data-no-swipe")).toBe(true);
  });
});
