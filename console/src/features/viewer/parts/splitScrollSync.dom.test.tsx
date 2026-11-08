import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installSplitScrollSync, type SyncEditor } from "./splitScrollSync.ts";

// jsdom has no layout: scroll geometry is stubbed per element, and the preview's anchors are
// left out (only the start/end sentinels), so the mapping is the plain line ratio.
function scrollable(height: number, client = 100): HTMLElement {
  const el = document.createElement("div");
  let top = 0;
  Object.defineProperty(el, "scrollTop", { get: () => top, set: (v: number) => (top = Math.max(0, Math.min(v, height - client))), configurable: true });
  Object.defineProperty(el, "scrollHeight", { get: () => height, configurable: true });
  Object.defineProperty(el, "clientHeight", { get: () => client, configurable: true });
  return el;
}

let frames: FrameRequestCallback[] = [];
const flush = () => {
  for (let i = 0; i < 5 && frames.length; i++) {
    const run = frames;
    frames = [];
    run.forEach((f) => f(0));
  }
};

beforeEach(() => {
  frames = [];
  vi.stubGlobal("requestAnimationFrame", (f: FrameRequestCallback) => frames.push(f));
  vi.stubGlobal("cancelAnimationFrame", () => {});
});
afterEach(() => vi.unstubAllGlobals());

function setup() {
  const box = document.createElement("div");
  const host = document.createElement("div");
  const panel = document.createElement("input"); // a search panel: in the host, not the scroller
  const scroller = scrollable(1000);
  host.append(scroller, panel);
  const preview = scrollable(2001);
  preview.append(document.createElement("div"));
  box.append(host, preview);
  document.body.append(box);
  // 100 lines, 10px each: the top line is scrollTop / 10 + 1.
  const editor: SyncEditor = {
    scroller,
    host,
    lineCount: () => 100,
    topLine: () => scroller.scrollTop / 10 + 1,
    scrollToLine: (line) => (scroller.scrollTop = (line - 1) * 10),
  };
  const sync = installSplitScrollSync(box, editor, () => preview, "editor");
  flush();
  return { box, panel, scroller, preview, sync };
}

const scrollBy = (el: HTMLElement, top: number, input = true) => {
  if (input) el.dispatchEvent(new WheelEvent("wheel", { bubbles: true }));
  el.scrollTop = top;
  el.dispatchEvent(new Event("scroll"));
  flush();
};

describe("installSplitScrollSync", () => {
  it("moves the preview with the editor and the editor with the preview", () => {
    const { scroller, preview, sync } = setup();
    scrollBy(scroller, 500); // line 51 of 101 → half of the preview
    expect(preview.scrollTop).toBeCloseTo(1000, -1);
    scrollBy(preview, 400); // a fifth of the preview → line 21
    expect(scroller.scrollTop).toBeCloseTo(200, -1);
    sync.dispose();
  });

  it("ignores the follower's own scroll events", () => {
    const { scroller, preview, sync } = setup();
    scrollBy(scroller, 300);
    const followed = preview.scrollTop;
    scrollBy(preview, followed + 37, false); // no input: an echo, not the reader
    expect(scroller.scrollTop).toBe(300);
    sync.dispose();
  });

  it("hands the driver to the editor for input in its panels", () => {
    const { panel, scroller, preview, sync } = setup();
    scrollBy(preview, 400);
    panel.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true }));
    scrollBy(scroller, 600, false); // the search jumped to a match
    expect(preview.scrollTop).toBeCloseTo(1200, -1);
    sync.dispose();
  });

  it("lead() hands the driver over without input", () => {
    const { scroller, preview, sync } = setup();
    scrollBy(preview, 400);
    scroller.scrollTop = 700; // an accepted suggestion scrolled to the change
    sync.lead("editor");
    flush();
    expect(preview.scrollTop).toBeCloseTo(1400, -1);
    sync.dispose();
  });

  it("stops following once disposed", () => {
    const { scroller, preview, sync } = setup();
    sync.dispose();
    scrollBy(scroller, 500);
    expect(preview.scrollTop).toBe(0);
  });
});
