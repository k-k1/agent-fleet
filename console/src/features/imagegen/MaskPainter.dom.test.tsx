// The mask canvas's controls (docs/log/111 §10.7). jsdom has no canvas and no layout, so the 2D
// context below is a small software rasteriser — enough that "painted", "erased", "inverted" and
// the exported PNG's pixels are real numbers a test can read — and the picture's box is stubbed.
// Orientation and real pointer input are proven in headless Chromium, not here.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";

const upload = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  uploadFiles: (...a: unknown[]) => upload(...a),
}));

import { MASK_DIR, MaskPainter, type ImageLoader } from "./parts/MaskPainter.tsx";
import { MaskCanvasModal } from "./parts/MaskCanvasModal.tsx";
import { GenerateForm } from "./parts/GenerateForm.tsx";
import { emptyDraft, type ImagegenDraft } from "./draft.ts";
import type { ImagegenModel, ImagegenProvider } from "./wire.ts";

// ---- a software 2D context --------------------------------------------------------------------

type Px = (x: number, y: number) => [number, number, number, number];
interface Buf {
  w: number;
  h: number;
  d: Uint8ClampedArray;
}
const bufs = new WeakMap<HTMLCanvasElement, Buf>();
const bufOf = (c: HTMLCanvasElement): Buf => {
  let b = bufs.get(c);
  if (!b || b.w !== c.width || b.h !== c.height) {
    b = { w: c.width, h: c.height, d: new Uint8ClampedArray(c.width * c.height * 4) };
    bufs.set(c, b);
  }
  return b;
};
const rgb = (s: string): [number, number, number] =>
  s === "#fff" ? [255, 255, 255] : s === "#000" ? [0, 0, 0] : [parseInt(s.slice(1, 3), 16), parseInt(s.slice(3, 5), 16), parseInt(s.slice(5, 7), 16)];

function fakeCtx(canvas: HTMLCanvasElement) {
  let path: { x: number; y: number }[] = [];
  let disc: { x: number; y: number; r: number } | null = null;
  const stack: unknown[] = [];
  const ctx = {
    fillStyle: "#000",
    strokeStyle: "#000",
    lineWidth: 1,
    lineCap: "butt",
    lineJoin: "miter",
    globalCompositeOperation: "source-over",
    set(i: number, c: [number, number, number]) {
      const d = bufOf(canvas).d;
      const op = ctx.globalCompositeOperation;
      // Opaque sources only, so each Porter-Duff mode reduces to a rule per pixel.
      if (op === "destination-out") {
        d.fill(0, i, i + 4);
        return;
      }
      if (op === "xor") {
        if (d[i + 3] > 0) d.fill(0, i, i + 4);
        else d.set([c[0], c[1], c[2], 255], i);
        return;
      }
      if (op === "source-in") {
        if (d[i + 3] > 0) d.set([c[0], c[1], c[2], d[i + 3]], i);
        else d.fill(0, i, i + 4);
        return;
      }
      if (op === "difference") {
        d[i] = Math.abs(d[i] - c[0]);
        d[i + 1] = Math.abs(d[i + 1] - c[1]);
        d[i + 2] = Math.abs(d[i + 2] - c[2]);
      } else if (ctx.globalCompositeOperation === "multiply") {
        d[i] = (d[i] * c[0]) / 255;
        d[i + 1] = (d[i + 1] * c[1]) / 255;
        d[i + 2] = (d[i + 2] * c[2]) / 255;
      } else {
        d[i] = c[0];
        d[i + 1] = c[1];
        d[i + 2] = c[2];
      }
      d[i + 3] = 255;
    },
    dot(cx: number, cy: number, r: number, c: [number, number, number]) {
      const b = bufOf(canvas);
      for (let y = Math.max(0, Math.floor(cy - r)); y <= Math.min(b.h - 1, Math.ceil(cy + r)); y++)
        for (let x = Math.max(0, Math.floor(cx - r)); x <= Math.min(b.w - 1, Math.ceil(cx + r)); x++)
          if ((x + 0.5 - cx) ** 2 + (y + 0.5 - cy) ** 2 <= r * r) ctx.set((y * b.w + x) * 4, c);
    },
    fillRect(x0: number, y0: number, w: number, h: number) {
      const b = bufOf(canvas);
      const c = rgb(String(ctx.fillStyle));
      for (let y = Math.max(0, y0); y < Math.min(b.h, y0 + h); y++)
        for (let x = Math.max(0, x0); x < Math.min(b.w, x0 + w); x++) ctx.set((y * b.w + x) * 4, c);
    },
    clearRect(x0 = 0, y0 = 0, w = Infinity, h = Infinity) {
      const b = bufOf(canvas);
      for (let y = Math.max(0, y0); y < Math.min(b.h, y0 + h); y++) b.d.fill(0, (y * b.w + Math.max(0, x0)) * 4, (y * b.w + Math.min(b.w, x0 + w)) * 4);
    },
    beginPath() {
      path = [];
      disc = null;
    },
    moveTo(x: number, y: number) {
      path = [{ x, y }];
    },
    lineTo(x: number, y: number) {
      path.push({ x, y });
    },
    arc(x: number, y: number, r: number) {
      disc = { x, y, r };
    },
    fill() {
      if (disc) ctx.dot(disc.x, disc.y, disc.r, rgb(String(ctx.fillStyle)));
    },
    stroke() {
      const c = rgb(String(ctx.strokeStyle));
      const r = ctx.lineWidth / 2;
      for (let i = 0; i < path.length; i++) {
        const a = path[Math.max(0, i - 1)];
        const p = path[i];
        const n = Math.max(1, Math.ceil(Math.hypot(p.x - a.x, p.y - a.y)));
        for (let k = 0; k <= n; k++) ctx.dot(a.x + ((p.x - a.x) * k) / n, a.y + ((p.y - a.y) * k) / n, r, c);
      }
    },
    save() {
      stack.push(ctx.globalCompositeOperation);
    },
    restore() {
      ctx.globalCompositeOperation = stack.pop() as string;
    },
    drawImage(src: HTMLCanvasElement | (HTMLImageElement & { px?: Px }), dx: number, dy: number, dw?: number, dh?: number) {
      const b = bufOf(canvas);
      const w = dw ?? b.w;
      const h = dh ?? b.h;
      const sb = src instanceof HTMLCanvasElement ? bufOf(src) : null;
      const px = (src as { px?: Px }).px;
      for (let y = 0; y < h && y + dy < b.h; y++)
        for (let x = 0; x < w && x + dx < b.w; x++) {
          const i = ((y + dy) * b.w + x + dx) * 4;
          let c: [number, number, number, number];
          if (sb) {
            const j = (Math.floor((y * sb.h) / h) * sb.w + Math.floor((x * sb.w) / w)) * 4;
            c = [sb.d[j], sb.d[j + 1], sb.d[j + 2], sb.d[j + 3]];
          } else if (px) c = px((x + 0.5) / w, (y + 0.5) / h);
          else continue;
          if (ctx.globalCompositeOperation === "copy") {
            b.d.set(c, i);
          } else if (c[3] > 0) {
            // source-over of a 0/255 alpha source; a partial alpha keeps its value (underlay probe).
            b.d.set(c[3] === 255 ? c : [c[0], c[1], c[2], Math.max(b.d[i + 3], c[3])], i);
          }
        }
    },
    getImageData(x: number, y: number, w: number, h: number) {
      const b = bufOf(canvas);
      expect([x, y, w, h]).toEqual([0, 0, b.w, b.h]);
      return { data: new Uint8ClampedArray(b.d), width: w, height: h };
    },
    putImageData(img: { data: Uint8ClampedArray }) {
      bufOf(canvas).d.set(img.data);
    },
  };
  return ctx;
}

let exported: Buf | null = null;
const ctxs = new WeakMap<HTMLCanvasElement, ReturnType<typeof fakeCtx>>();

beforeEach(() => {
  upload.mockReset();
  upload.mockResolvedValue({ status: 200 });
  exported = null;
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(function (this: HTMLCanvasElement) {
    let c = ctxs.get(this);
    if (!c) ctxs.set(this, (c = fakeCtx(this)));
    return c as never;
  });
  HTMLCanvasElement.prototype.toBlob = function (this: HTMLCanvasElement, cb: BlobCallback) {
    const b = bufOf(this);
    exported = { w: b.w, h: b.h, d: new Uint8ClampedArray(b.d) };
    cb(new Blob(["png"], { type: "image/png" }));
  };
});

// ---- rendering ---------------------------------------------------------------------------------

let host: HTMLDivElement;
let root: Root;

/** The picture's natural size; the stage is the same size, so the fitted picture is 400×200 and
 *  one display pixel is one export pixel. `sideways` lays the <img> out with the other aspect, as
 *  `image-orientation: none` would on an Orientation 6 file. */
const PIC = { w: 400, h: 200 };
const STAGE = { w: 400, h: 200 };
let sideways = false;

const rect = (w: number, h: number) =>
  ({ left: 0, top: 0, x: 0, y: 0, width: w, height: h, right: w, bottom: h, toJSON() {} }) as DOMRect;

beforeEach(() => {
  sideways = false;
  const isStage = (el: Element) => el.classList.contains("igen-mask-stage");
  const isPic = (el: Element) => el.classList.contains("igen-mask-picture");
  const picW = (el: HTMLElement) => parseFloat(el.style.width) || 0;
  const picH = (el: HTMLElement) => (sideways ? picW(el) * 2 : (picW(el) * PIC.h) / PIC.w);
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(function (this: HTMLElement) {
    return isStage(this) ? STAGE.w : 0;
  });
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockImplementation(function (this: HTMLElement) {
    return isStage(this) ? STAGE.h : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return isPic(this) ? picW(this) : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return isPic(this) ? picH(this) : 0;
  });
  // The canvas covers the picture; the stage is at the origin too.
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    if (this instanceof HTMLCanvasElement || isStage(this)) return rect(STAGE.w, STAGE.h);
    return rect(0, 0);
  });
});

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

async function loadPicture() {
  const img = document.querySelector("img.igen-mask-picture") as HTMLImageElement;
  Object.defineProperty(img, "naturalWidth", { value: PIC.w });
  Object.defineProperty(img, "naturalHeight", { value: PIC.h });
  await act(async () => {
    img.dispatchEvent(new Event("load"));
  });
}

const fakeImage = (w: number, h: number, px: Px): HTMLImageElement => {
  const img = new Image();
  Object.defineProperty(img, "naturalWidth", { value: w });
  Object.defineProperty(img, "naturalHeight", { value: h });
  (img as HTMLImageElement & { px: Px }).px = px;
  return img;
};

interface PainterOpts {
  mask?: string;
  loadImage?: ImageLoader;
  pictureCurrent?: () => boolean;
  onSaved?: (p: string) => boolean | void;
}

async function renderPainter({ mask = "", loadImage, pictureCurrent, onSaved }: PainterOpts = {}) {
  const saved = vi.fn(onSaved ?? (() => undefined));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(
      <MaskPainter picture="photo.jpg" mask={mask} onSaved={saved} onCancel={() => {}} loadImage={loadImage} pictureCurrent={pictureCurrent} />,
    );
  });
  return saved;
}

const canvas = () => document.querySelector("canvas.igen-mask-view") as HTMLCanvasElement;
const btn = (label: string) =>
  [...document.querySelectorAll("button")].find((b) => (b.textContent?.trim() || b.getAttribute("aria-label")) === label) as HTMLButtonElement;
const click = async (label: string) => {
  const b = btn(label);
  if (!b) throw new Error(`no button ${label}`);
  await act(async () => {
    b.click();
  });
};

function pointer(type: string, x: number, y: number, init: { id?: number; kind?: string } = {}) {
  const e = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: x, clientY: y, button: 0 });
  Object.assign(e, { pointerId: init.id ?? 1, pointerType: init.kind ?? "mouse" });
  canvas().dispatchEvent(e);
}

/** A horizontal stroke along y = 1/8 of the picture. */
async function strokeTop(id = 1, kind = "mouse") {
  await act(async () => {
    pointer("pointerdown", 40, 25, { id, kind });
    pointer("pointermove", 200, 25, { id, kind });
    pointer("pointermove", 360, 25, { id, kind });
    pointer("pointerup", 360, 25, { id, kind });
  });
}

const red = (b: Buf, x: number, y: number) => b.d[(Math.floor(y) * b.w + Math.floor(x)) * 4];
const err = () => document.querySelector(".igen-err")?.textContent ?? "";
const frameTransform = () => (document.querySelector(".igen-mask-frame") as HTMLElement).style.transform;

// Labels come from the default (ja) catalogue, as in the other dom tests.
const SAVE = "マスクを保存";
const UNDO = "取り消し";

describe("MaskPainter", () => {
  it("refuses to save a blank mask and uploads nothing", async () => {
    const saved = await renderPainter();
    await loadPicture();
    await click(SAVE);
    expect(err()).toContain("まだ何も塗られていません");
    expect(upload).not.toHaveBeenCalled();
    expect(saved).not.toHaveBeenCalled();
  });

  it("exports the native size into masks/: black, opaque, grey, white where the stroke went", async () => {
    const saved = await renderPainter();
    await loadPicture();
    await strokeTop();
    await click(SAVE);
    expect(upload).toHaveBeenCalledTimes(1);
    const [dir, files] = upload.mock.calls[0] as [string, File[]];
    expect(dir).toBe(MASK_DIR);
    expect(files[0].name).toMatch(/^mask-\d{8}-\d{6}-\d{3}-[0-9a-z]{4}\.png$/);
    expect(saved).toHaveBeenCalledWith(`${MASK_DIR}/${files[0].name}`);
    const out = exported!;
    expect([out.w, out.h]).toEqual([PIC.w, PIC.h]);
    expect(red(out, 200, 25)).toBe(255);
    expect(red(out, 200, 150)).toBe(0);
    for (let i = 3; i < out.d.length; i += 4) if (out.d[i] !== 255) throw new Error(`transparent pixel at ${i}`);
    for (let i = 0; i < out.d.length; i += 4)
      if (out.d[i] !== out.d[i + 1] || out.d[i] !== out.d[i + 2]) throw new Error(`not grey at ${i}`);
  });

  it("saving with a finger still down saves that stroke, once", async () => {
    await renderPainter();
    await loadPicture();
    await act(async () => {
      pointer("pointerdown", 40, 25, { id: 1, kind: "touch" });
      pointer("pointermove", 360, 25, { id: 1, kind: "touch" });
    });
    await click(SAVE);
    expect(upload).toHaveBeenCalledTimes(1);
    expect(red(exported!, 200, 25), "the stroke on screen is the stroke exported").toBe(255);
    // Its pointerup arrives after the save started: it must not add the stroke a second time.
    await act(async () => pointer("pointerup", 360, 25, { id: 1, kind: "touch" }));
    await click(UNDO);
    expect(btn(UNDO).disabled, "one stroke in the history, not two").toBe(true);
  });

  it("shows painted areas as an opaque tint over any picture, transparent elsewhere", async () => {
    await renderPainter();
    await loadPicture();
    await strokeTop();
    const v = bufOf(canvas());
    const at = (x: number, y: number) => [...v.d.slice((y * v.w + x) * 4, (y * v.w + x) * 4 + 4)];
    expect(at(200, 25), "tint with full coverage: visible on white").toEqual([255, 43, 214, 255]);
    expect(at(200, 150)[3], "unpainted: nothing over the picture").toBe(0);
    const css = getComputedStyle(canvas());
    expect(css.mixBlendMode || "normal", "no blend that vanishes over white").toBe("normal");
  });

  it("a name that exists (409) is never overwritten: it asks for another name", async () => {
    upload.mockResolvedValueOnce({ status: 409 });
    const saved = await renderPainter();
    await loadPicture();
    await strokeTop();
    await click(SAVE);
    expect(upload).toHaveBeenCalledTimes(2);
    const names = upload.mock.calls.map((c) => (c[1] as File[])[0].name);
    expect(names[0]).not.toBe(names[1]);
    for (const c of upload.mock.calls) expect(c[2], "never with overwrite").toBeUndefined();
    expect(saved).toHaveBeenCalledWith(`${MASK_DIR}/${names[1]}`);
  });

  it("a failed upload keeps the drawing and can be retried", async () => {
    upload.mockResolvedValueOnce({ status: 500 });
    const saved = await renderPainter();
    await loadPicture();
    await strokeTop();
    await click(SAVE);
    expect(err()).toContain("保存できませんでした");
    expect(saved).not.toHaveBeenCalled();
    expect(btn(UNDO).disabled, "the stroke is still there").toBe(false);
    await click(SAVE);
    expect(saved).toHaveBeenCalledTimes(1);
    expect(red(exported!, 200, 25)).toBe(255);
  });

  it("refuses when the picture is no longer the one it was opened on — before uploading, and if the owner refuses after", async () => {
    let current = false;
    const saved = await renderPainter({ pictureCurrent: () => current });
    await loadPicture();
    await strokeTop();
    await click(SAVE);
    expect(err()).toContain("参照画像が変わった");
    expect(upload).not.toHaveBeenCalled();
    current = true;
    await click(SAVE);
    expect(saved).toHaveBeenCalledTimes(1);
  });

  it("undo takes the stroke back and redo returns it", async () => {
    await renderPainter();
    await loadPicture();
    expect(btn(UNDO).disabled).toBe(true);
    await strokeTop();
    await click(UNDO);
    await click(SAVE);
    expect(err(), "after undo there is nothing painted").toContain("まだ何も塗られていません");
    await click("やり直し");
    await click(SAVE);
    expect(upload).toHaveBeenCalledTimes(1);
    expect(red(exported!, 200, 25)).toBe(255);
  });

  it("the eraser and clear take paint away", async () => {
    await renderPainter();
    await loadPicture();
    await strokeTop();
    await click("消しゴム");
    await strokeTop();
    await click(SAVE);
    expect(upload, "erased along the same line").not.toHaveBeenCalled();
    await click("筆");
    await strokeTop();
    await click("クリア");
    await click(SAVE);
    expect(upload).not.toHaveBeenCalled();
  });

  it("invert of a blank mask warns at once, and saving asks once more", async () => {
    await renderPainter();
    await loadPicture();
    await click("反転");
    expect(document.body.textContent).toContain("反転したら絵のほぼ全面");
    await click(SAVE);
    expect(err()).toContain("ほぼ全面");
    expect(upload).not.toHaveBeenCalled();
    await click("このまま保存");
    expect(upload).toHaveBeenCalledTimes(1);
    expect(red(exported!, 10, 10)).toBe(255);
  });

  it("a second finger stops the stroke and draws none of it; the finger left behind paints nothing", async () => {
    await renderPainter();
    await loadPicture();
    await act(async () => {
      pointer("pointerdown", 40, 25, { id: 1, kind: "touch" });
      pointer("pointermove", 200, 25, { id: 1, kind: "touch" });
      pointer("pointerdown", 300, 160, { id: 2, kind: "touch" });
      pointer("pointerup", 300, 160, { id: 2, kind: "touch" });
      pointer("pointermove", 360, 25, { id: 1, kind: "touch" });
      pointer("pointerup", 360, 25, { id: 1, kind: "touch" });
    });
    expect(btn(UNDO).disabled, "nothing was committed").toBe(true);
    await click(SAVE);
    expect(upload).not.toHaveBeenCalled();
    // The positive control: one finger alone paints.
    await strokeTop(3, "touch");
    expect(btn(UNDO).disabled).toBe(false);
  });

  it("two fingers pinch-zoom the picture", async () => {
    await renderPainter();
    await loadPicture();
    expect(frameTransform()).toContain("scale(1)");
    await act(async () => {
      pointer("pointerdown", 150, 100, { id: 1, kind: "touch" });
      pointer("pointerdown", 250, 100, { id: 2, kind: "touch" });
      pointer("pointermove", 100, 100, { id: 1, kind: "touch" });
      pointer("pointermove", 300, 100, { id: 2, kind: "touch" });
      pointer("pointerup", 100, 100, { id: 1, kind: "touch" });
      pointer("pointerup", 300, 100, { id: 2, kind: "touch" });
    });
    expect(frameTransform()).toContain("scale(2)");
    expect(btn(UNDO).disabled, "a pinch paints nothing").toBe(true);
  });

  it("zoom buttons, move mode and fit", async () => {
    await renderPainter();
    await loadPicture();
    await click("拡大");
    expect(frameTransform()).toContain("scale(1.5)");
    const before = frameTransform();
    await click("動かす");
    await act(async () => {
      pointer("pointerdown", 200, 100);
      pointer("pointermove", 230, 110);
      pointer("pointerup", 230, 110);
    });
    expect(frameTransform(), "a drag in move mode pans").not.toBe(before);
    expect(btn(UNDO).disabled, "and paints nothing").toBe(true);
    await click("全体を表示");
    expect(frameTransform()).toBe("translate(0px, 0px) scale(1)");
  });

  it("a pointer cancel drops the stroke", async () => {
    await renderPainter();
    await loadPicture();
    await act(async () => {
      pointer("pointerdown", 40, 25);
      pointer("pointermove", 200, 25);
      pointer("pointercancel", 200, 25);
    });
    expect(btn(UNDO).disabled).toBe(true);
  });

  it("warns on the thin brush only", async () => {
    await renderPainter();
    await loadPicture();
    const warn = () => document.querySelector(".igen-warn")?.textContent ?? "";
    expect(warn()).toBe("");
    await click("細");
    expect(warn()).toContain("細い筆");
    await click("太");
    expect(warn()).toBe("");
  });

  it("refuses to paint a picture displayed against its orientation", async () => {
    sideways = true;
    await renderPainter();
    await loadPicture();
    expect(document.querySelector("[role=alert]")?.textContent).toContain("向き");
    expect(btn(SAVE).disabled).toBe(true);
  });
});

describe("MaskPainter underlay", () => {
  const statuses = () => [...document.querySelectorAll("[role=status]")].map((n) => n.textContent ?? "").join(" | ");

  it("an unreadable mask opens blank with a notice", async () => {
    await renderPainter({ mask: "gone.png", loadImage: () => Promise.reject(new Error("404")) });
    await loadPicture();
    expect(statuses()).toContain("読めなかった");
    await click(SAVE);
    expect(upload).not.toHaveBeenCalled();
  });

  it("a mask of another aspect ratio is stretched over the picture, with a notice", async () => {
    // 100×100 (1:1) against a 2:1 picture; left half red=255 (and blue 0: only red counts).
    const loadImage = () => Promise.resolve(fakeImage(100, 100, (x) => (x < 0.5 ? [255, 0, 0, 255] : [0, 0, 0, 255])));
    await renderPainter({ mask: "old.png", loadImage });
    await loadPicture();
    expect(statuses()).toContain("縦横比が違います");
    await click(SAVE);
    expect(upload).toHaveBeenCalledTimes(1);
    const out = exported!;
    expect([out.w, out.h]).toEqual([PIC.w, PIC.h]);
    expect(red(out, 100, 100), "left half, stretched").toBe(255);
    expect(out.d[(100 * out.w + 100) * 4 + 2], "the red channel became grey").toBe(255);
    expect(red(out, 300, 100)).toBe(0);
  });

  it("a mask of the same aspect ratio overlays, saying per-stroke undo did not survive", async () => {
    const loadImage = () => Promise.resolve(fakeImage(800, 400, (_x, y) => (y < 0.5 ? [255, 255, 255, 255] : [0, 0, 0, 255])));
    await renderPainter({ mask: "same.png", loadImage });
    await loadPicture();
    expect(statuses()).toContain("前回の 1 本ずつの取り消しは残っていません");
    expect(statuses()).not.toContain("縦横比");
    await click(SAVE);
    expect(red(exported!, 200, 50)).toBe(255);
    expect(red(exported!, 200, 150)).toBe(0);
  });

  it("back to initial mask returns to the reopened mask, unlike clear", async () => {
    const loadImage = () => Promise.resolve(fakeImage(400, 200, (_x, y) => (y < 0.5 ? [255, 255, 255, 255] : [0, 0, 0, 255])));
    await renderPainter({ mask: "same.png", loadImage });
    await loadPicture();
    await act(async () => {
      pointer("pointerdown", 40, 175);
      pointer("pointermove", 360, 175);
      pointer("pointerup", 360, 175);
    });
    await click("クリア");
    await click("最初のマスクに戻す");
    await click(SAVE);
    const out = exported!;
    expect(red(out, 200, 50), "the reopened top half").toBe(255);
    expect(red(out, 200, 175), "the stroke before the reset is gone").toBe(0);
  });

  it("a mask with transparency is not overlaid", async () => {
    const loadImage = () => Promise.resolve(fakeImage(400, 200, () => [255, 255, 255, 128]));
    await renderPainter({ mask: "alpha.png", loadImage });
    await loadPicture();
    expect(statuses()).toContain("透過");
    await click(SAVE);
    expect(upload, "the transparent underlay was dropped, so the mask is blank").not.toHaveBeenCalled();
  });
});

describe("MaskCanvasModal closing", () => {
  async function renderModal() {
    // useBackClose counts the popstates its own history.back() will cause (module scope); let
    // the previous test's ones arrive before this test presses back, or they eat its presses.
    await act(async () => new Promise((r) => setTimeout(r, 30)));
    const closed = vi.fn();
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => {
      root.render(<MaskCanvasModal picture="photo.jpg" mask="" onSaved={() => {}} onClose={closed} />);
    });
    await loadPicture();
    return closed;
  }
  const esc = async () =>
    act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
  const ask = () => document.querySelector(".igen-mask-ask[role=alertdialog]");

  it("closes at once with nothing painted", async () => {
    const closed = await renderModal();
    await click("キャンセル");
    expect(closed).toHaveBeenCalledTimes(1);
  });

  it("asks before dropping unsaved strokes: keep editing, then discard", async () => {
    const closed = await renderModal();
    await strokeTop();
    await click("閉じる");
    expect(ask(), "× asks").not.toBeNull();
    expect(closed).not.toHaveBeenCalled();
    await click("編集を続ける");
    expect(ask()).toBeNull();
    expect(btn(UNDO).disabled, "the drawing is intact").toBe(false);
    await esc();
    expect(ask(), "Esc asks too").not.toBeNull();
    await click("破棄");
    expect(closed).toHaveBeenCalledTimes(1);
  });

  it("Esc in the middle of the first stroke asks instead of dropping it", async () => {
    const closed = await renderModal();
    await act(async () => {
      pointer("pointerdown", 40, 25);
      pointer("pointermove", 200, 25);
    });
    await esc();
    expect(ask()).not.toBeNull();
    expect(closed).not.toHaveBeenCalled();
  });

  it("the browser's back closes a clean canvas, asks on a dirty one, and never leaves during an upload", async () => {
    const back = async () => act(async () => window.dispatchEvent(new PopStateEvent("popstate", { state: null })));
    const push = vi.spyOn(history, "pushState");
    const closed = await renderModal();
    const armed = push.mock.calls.length;
    expect(armed, "the guard entry is pushed on open").toBeGreaterThan(0);
    await strokeTop();
    await back();
    expect(ask(), "dirty: back asks").not.toBeNull();
    expect(push.mock.calls.length, "and re-arms the guard").toBe(armed + 1);
    await click("編集を続ける");
    // An upload that does not finish yet.
    let finish: (v: { status: number }) => void = () => {};
    upload.mockImplementationOnce(() => new Promise((r) => (finish = r)));
    await click(SAVE);
    expect(upload).toHaveBeenCalledTimes(1);
    await back();
    await back();
    expect(closed, "two backs during the upload").not.toHaveBeenCalled();
    expect(document.querySelector(".igen-mask-modal"), "still on screen").not.toBeNull();
    expect(push.mock.calls.length, "a fresh guard after each back").toBe(armed + 3);
    await act(async () => finish({ status: 200 }));
  });

  it("back on a clean canvas closes it", async () => {
    const closed = await renderModal();
    await act(async () => window.dispatchEvent(new PopStateEvent("popstate", { state: null })));
    expect(closed).toHaveBeenCalledTimes(1);
  });

  it("save from the question uploads", async () => {
    await renderModal();
    await strokeTop();
    await click("キャンセル");
    await click("保存");
    expect(upload).toHaveBeenCalledTimes(1);
  });
});

// ---- where the canvas is offered -------------------------------------------------------------

const SDXL: ImagegenModel = {
  id: "sdxl-base",
  family: "sdxl",
  knobs: ["steps", "cfg", "sampler", "scheduler", "negative", "strength"],
  ops: ["generate", "edit", "inpaint"],
};

let patchForm: (p: Partial<ImagegenDraft>) => void = () => {};
let draftNow: ImagegenDraft | null = null;

function FormHarness({
  provider,
  model = SDXL,
  initial,
  onPaint,
}: {
  provider: ImagegenProvider | null;
  model?: ImagegenModel;
  initial: Partial<ImagegenDraft>;
  onPaint?: (picture: string, mask: string) => void;
}) {
  const [draft, setDraft] = useState<ImagegenDraft>({ ...emptyDraft(), model: model.id, ...initial });
  patchForm = (p) => setDraft((d) => ({ ...d, ...p }));
  draftNow = draft;
  return (
    <GenerateForm
      draft={draft}
      patch={patchForm}
      fleetProviders={provider ? [provider] : []}
      provider={provider}
      models={[model]}
      loras={[]}
      model={model}
      samplers={[]}
      schedulers={[]}
      loraWeightMax={2}
      alwaysNegative=""
      busy={false}
      trialFull={false}
      queueFull={false}
      onTrial={() => {}}
      onEnqueue={() => {}}
      onPaintMask={onPaint}
    />
  );
}

async function renderForm(
  provider: ImagegenProvider | null,
  initial: Partial<ImagegenDraft>,
  { model, onPaint = () => {} }: { model?: ImagegenModel; onPaint?: (p: string, m: string) => void } = {},
) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<FormHarness provider={provider} initial={initial} model={model} onPaint={onPaint} />);
  });
}
const remount = async (...a: Parameters<typeof renderForm>) => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
  await renderForm(...a);
};

const COMFY: ImagegenProvider = { id: "image", fleet: true, kind: "comfy" };
const SDSERVER: ImagegenProvider = { id: "sd", fleet: true, kind: "openai-compat" };
const OPEN = "マスクを塗る";
const entry = () => document.querySelector(".igen-mask-entry");

describe("the mask row", () => {
  it("is in view without opening Advanced, and only on an inpaint", async () => {
    await renderForm(COMFY, { op: "inpaint", inputs: ["photo.jpg"] });
    expect(entry()).not.toBeNull();
    expect(entry()!.closest("details"), "outside the folded section").toBeNull();
    await remount(COMFY, { op: "edit", inputs: ["photo.jpg"] });
    expect(entry()).toBeNull();
  });

  it("offers the canvas on a ComfyUI model that declares inpaint, and says why not elsewhere", async () => {
    const painted: string[] = [];
    await renderForm(COMFY, { op: "inpaint", inputs: ["photo.jpg", "second.jpg"] }, { onPaint: (p) => painted.push(p) });
    await click(OPEN);
    expect(painted, "the FIRST reference is the one painted over").toEqual(["photo.jpg"]);
    await remount(SDSERVER, { op: "inpaint", inputs: ["photo.jpg"] });
    expect(btn(OPEN), "openai_compat: the mapping is unmeasured (log 111 §10.8)").toBeUndefined();
    expect(entry()!.textContent).toContain("パス");
    await remount(null, { op: "inpaint", inputs: ["photo.jpg"] });
    expect(btn(OPEN), "no provider known: nothing is promised").toBeUndefined();
    await remount(COMFY, { op: "inpaint", inputs: ["photo.jpg"] }, { model: { ...SDXL, ops: ["generate", "edit"] } });
    expect(btn(OPEN), "a model without inpaint").toBeUndefined();
    expect(entry()!.textContent).toContain("inpaint を宣言していない");
  });

  it("needs a picture to paint over", async () => {
    await renderForm(COMFY, { op: "inpaint", inputs: [] });
    expect(btn(OPEN).disabled).toBe(true);
  });

  it("when the first reference changes under a mask, holds it out of the draft until answered", async () => {
    const M = "generated/console/masks/m.png";
    const painted: [string, string][] = [];
    await renderForm(COMFY, { op: "inpaint", inputs: ["a.jpg"], mask: M }, { onPaint: (p, m) => painted.push([p, m]) });
    const question = () => document.querySelector(".igen-mask-ask");
    const pressable = () =>
      [...document.querySelectorAll<HTMLButtonElement>(".igen-actions button")].filter((b) => !b.disabled).length;
    expect(question(), "nothing changed yet").toBeNull();
    expect(pressable(), "with a mask the presses are live").toBeGreaterThan(0);
    await act(async () => patchForm({ inputs: ["a.jpg", "b.jpg"] }));
    expect(question(), "a second reference does not move the frame").toBeNull();

    await act(async () => patchForm({ inputs: ["b.jpg"] }));
    expect(question()).not.toBeNull();
    expect(draftNow!.mask, "held: the draft has no mask, so no press path can run the old one").toBe("");
    expect(pressable(), "trial and enqueue are held").toBe(0);
    await click("マスクをそのまま使う");
    expect(question()).toBeNull();
    expect(draftNow!.mask).toBe(M);

    await act(async () => patchForm({ inputs: ["c.jpg"] }));
    await click("塗り直す");
    expect(painted.at(-1), "repaint opens on the new picture over the held mask").toEqual(["c.jpg", M]);
    expect(question(), "cancelling the canvas leaves the question").not.toBeNull();
    expect(draftNow!.mask).toBe("");
    await act(async () => patchForm({ mask: "generated/console/masks/new.png" }));
    expect(question(), "a saved mask answers it").toBeNull();

    await act(async () => patchForm({ inputs: ["d.jpg"] }));
    await click("マスクを外す");
    expect(draftNow!.mask).toBe("");
    expect(question()).toBeNull();
  });

  it("does not ask when there is no mask, or when the mask is cleared with the picture", async () => {
    await renderForm(COMFY, { op: "inpaint", inputs: ["a.jpg"], mask: "" });
    await act(async () => patchForm({ inputs: ["b.jpg"] }));
    expect(document.querySelector(".igen-mask-ask")).toBeNull();
    await act(async () => patchForm({ mask: "m.png" }));
    // "Fix this part" swaps the picture and empties the mask in one patch.
    await act(async () => patchForm({ inputs: ["c.jpg"], mask: "" }));
    expect(document.querySelector(".igen-mask-ask")).toBeNull();
  });
});
