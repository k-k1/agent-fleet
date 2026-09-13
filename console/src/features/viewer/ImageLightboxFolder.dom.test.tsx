// The lightbox's "open the folder" item (ADR 0080 decision 7, entry 3).
//
// The transcript's image card is already full — the body enlarges, the corner opens the pane —
// so the way to a picture's folder is a button on the bar the lightbox already draws
// (decision 5). What has to hold is that the item follows the DATA: a shared file has a path
// and gets it, a pasted image has only a blob URL and must not be offered a folder that does
// not exist.
//
// The item is found by elimination rather than by label or position: the labels come from the
// catalogue, and pinning an index here would make every future bar button a failing test.
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ImageLightbox } from "./ImageLightbox.tsx";

let host: HTMLDivElement;
let root: Root;

const render = async (onOpenFolder?: () => void): Promise<HTMLButtonElement[]> => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<ImageLightbox src="blob:shot" onClose={() => {}} onOpenFolder={onOpenFolder} />);
  });
  return [...host.querySelectorAll<HTMLButtonElement>(".mirror-lightbox-bar button")];
};

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
});

describe("ライトボックスの「フォルダを開く」", () => {
  it("onOpenFolder を渡したときだけバーに増える", async () => {
    const plain = (await render()).length;
    await act(async () => root.unmount());
    document.body.innerHTML = "";
    const withFolder = (await render(() => {})).length;
    expect(withFolder).toBe(plain + 1);
  });

  it("その項目だけが onOpenFolder を呼ぶ", async () => {
    let opened = 0;
    const bar = await render(() => opened++);
    for (const b of bar) {
      await act(async () => {
        b.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
    }
    expect(opened).toBe(1);
  });
});
