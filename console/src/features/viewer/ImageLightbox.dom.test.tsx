// The lightbox used to close on any click, which is why enlarging an image could never
// zoom: the first half of a double-click closed it. Zoom now lives in the shared
// ImageView, so what has to hold is the split — the image keeps its clicks (and its pan,
// which ends wherever the finger lifts), the backdrop and the controls close.
//
// The bar's buttons are addressed by position, not by their title: the titles come from
// the i18n catalogue and a test that matches on them fails the day a label is reworded.
//
// The component is shared with the gallery (ADR 0080 decision 5), which is why the second
// describe below asserts what the MIRROR must keep: with no paging and no folder callback
// its bar is the same four buttons in the same order it had before the move.
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ImageLightbox } from "./ImageLightbox.tsx";

let host: HTMLDivElement;
let root: Root;
let closed: number;

type Extra = Partial<Parameters<typeof ImageLightbox>[0]>;

const render = async (extra: Extra = {}) => {
  closed = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<ImageLightbox src="blob:shot" onClose={() => closed++} {...extra} />);
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

/** A finger (or a mouse, with pointerType) dragged from x1 to x2 and lifted. */
const swipe = async (
  el: HTMLElement,
  { from = 200, to = 100, dy = 0, pointerType = "touch" as string } = {},
) => {
  await act(async () => {
    el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, pointerId: 2, pointerType, clientX: from, clientY: 100 }));
    el.dispatchEvent(new PointerEvent("pointerup", { bubbles: true, pointerId: 2, pointerType, clientX: to, clientY: 100 + dy }));
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

describe("共有ライトボックスの送りとフォルダ", () => {
  it("並びを渡さないミラーでは、送りも位置も出ず、バーは以前と同じ 4 つ", async () => {
    await render();
    expect(host.querySelector(".mirror-lightbox-prev")).toBeNull();
    expect(host.querySelector(".mirror-lightbox-next")).toBeNull();
    expect(host.querySelector(".mirror-lightbox-pos")).toBeNull();
    expect(host.querySelector(".mirror-lightbox-folder")).toBeNull();
    expect(bar()).toHaveLength(4);
  });

  it("←／→ のボタンと矢印キーで送る", async () => {
    let prev = 0;
    let next = 0;
    await render({ onPrev: () => prev++, onNext: () => next++, index: 3, total: 12 });
    expect(host.querySelector(".mirror-lightbox-pos")?.textContent).toBe("3 / 12");

    await clickAt(host.querySelector(".mirror-lightbox-prev") as HTMLElement);
    await clickAt(host.querySelector(".mirror-lightbox-next") as HTMLElement);
    expect([prev, next]).toEqual([1, 1]);

    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft" }));
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
    });
    expect([prev, next]).toEqual([2, 2]);
    expect(closed).toBe(0); // paging is not the backdrop
  });

  it("端では渡されない側のボタンが無効になり、キーも何も起こさない", async () => {
    let next = 0;
    await render({ onNext: () => next++, index: 1, total: 3 });
    expect((host.querySelector(".mirror-lightbox-prev") as HTMLButtonElement).disabled).toBe(true);
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft" }));
    });
    expect(next).toBe(0);
  });

  it("スマホの左右スワイプで送る（左へ払う＝次、右へ払う＝前）", async () => {
    let prev = 0;
    let next = 0;
    const overlay = await render({ onPrev: () => prev++, onNext: () => next++, index: 2, total: 5 });
    await swipe(overlay, { from: 240, to: 60 });
    expect([prev, next]).toEqual([0, 1]);
    await swipe(overlay, { from: 60, to: 240 });
    expect([prev, next]).toEqual([1, 1]);
    expect(closed).toBe(0); // スワイプは背景クリックとして閉じてはいけない
  });

  it("指が少し動いただけ／縦に流れただけでは送らない", async () => {
    let next = 0;
    const overlay = await render({ onNext: () => next++ });
    await swipe(overlay, { from: 200, to: 170 }); // 30px = しきい値未満
    await swipe(overlay, { from: 200, to: 140, dy: 120 }); // 縦の方が大きい＝スクロール
    expect(next).toBe(0);
  });

  it("マウスの横ドラッグでは送らない（パンと取り違えるため。送りはボタンと ←／→）", async () => {
    let next = 0;
    const overlay = await render({ onNext: () => next++ });
    await swipe(overlay, { from: 240, to: 60, pointerType: "mouse" });
    expect(next).toBe(0);
  });

  it("拡大しているあいだはスワイプを送りに使わない（ドラッグはパンのもの）", async () => {
    let next = 0;
    const overlay = await render({ onNext: () => next++ });
    // 送りが付いたバーは並びが違うので、位置ではなく %表示の隣として拾う。
    await clickAt(level().nextElementSibling as HTMLElement); // ＋ = 1.4 倍
    expect(level().textContent).not.toBe("100%");
    await swipe(overlay, { from: 240, to: 60 });
    expect(next).toBe(0);
  });

  it("並びが渡されていなければ（ミラー）スワイプは何もしない", async () => {
    const overlay = await render();
    await swipe(overlay, { from: 240, to: 60 });
    expect(closed).toBe(0);
  });

  it("「フォルダを開く」は渡されたときだけ出る", async () => {
    let opened = 0;
    await render({ onOpenFolder: () => opened++ });
    const folder = host.querySelector(".mirror-lightbox-folder") as HTMLElement;
    expect(folder).not.toBeNull();
    await clickAt(folder);
    expect(opened).toBe(1);
    expect(closed).toBe(0); // the bar keeps its clicks
  });
});
