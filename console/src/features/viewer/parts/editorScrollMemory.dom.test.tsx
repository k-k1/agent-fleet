import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { attachEditorScrollMemory } from "./editorScrollMemory.ts";
import { clearScrollPos, loadScrollPos, saveScrollPos } from "../scrollMemory.ts";

let frames: FrameRequestCallback[] = [];
const flush = (n = 1) => {
  for (let i = 0; i < n; i++) {
    const run = frames;
    frames = [];
    run.forEach((cb) => cb(0));
  }
};

/** A stand-in editor: `line` is where it is; `drift` moves it once after a scroll, the way
 *  CodeMirror's first measurement corrects its estimated heights. */
function fakeEditor(visible = true, lines = 1000) {
  const host = document.createElement("div");
  const scroller = document.createElement("div");
  const content = document.createElement("div");
  host.appendChild(scroller);
  scroller.appendChild(content);
  const state = { line: 1, drift: 0, scrolls: 0, visible };
  Object.defineProperty(scroller, "clientHeight", { get: () => (state.visible ? 400 : 0) });
  // 20px per line; the scroller cannot go past the last line.
  Object.defineProperty(scroller, "scrollTop", { get: () => Math.round(state.line * 20) });
  const editor = {
    host,
    scroller,
    content,
    topLine: () => state.line,
    scrollToLine: (line: number) => {
      state.scrolls++;
      state.line = Math.min(lines, line + state.drift);
      state.drift = 0;
    },
  };
  return { editor, state };
}

/** The observers attached so far; `resize(el)` tells the ones watching `el` it changed. */
let observers: { el: Element; cb: () => void }[] = [];
const resize = (el: Element) => observers.filter((o) => o.el === el).forEach((o) => o.cb());

beforeEach(() => {
  frames = [];
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => frames.push(cb));
  vi.stubGlobal("cancelAnimationFrame", () => {
    frames = [];
  });
  observers = [];
  vi.stubGlobal(
    "ResizeObserver",
    class {
      cb: () => void;
      constructor(cb: () => void) {
        this.cb = cb;
      }
      observe(el: Element) {
        observers.push({ el, cb: this.cb });
      }
      disconnect() {
        observers = observers.filter((o) => o.cb !== this.cb);
      }
    },
  );
});
afterEach(() => {
  vi.unstubAllGlobals();
  clearScrollPos();
});

describe("attachEditorScrollMemory", () => {
  it("records the top line on scroll", () => {
    const { editor, state } = fakeEditor();
    const detach = attachEditorScrollMemory(editor, "k", true);
    state.line = 42.5;
    editor.scroller.dispatchEvent(new Event("scroll"));
    expect(loadScrollPos("k")).toBe(42.5);
    detach();
  });

  it("does not record while the editor is hidden", () => {
    const { editor, state } = fakeEditor(false);
    saveScrollPos("k", 10);
    const detach = attachEditorScrollMemory(editor, "k", true);
    state.line = 1;
    editor.scroller.dispatchEvent(new Event("scroll"));
    expect(loadScrollPos("k")).toBe(10);
    detach();
  });

  it("restores the recorded line and re-applies it after the editor re-measures", () => {
    saveScrollPos("k", 120.25);
    const { editor, state } = fakeEditor();
    state.drift = 7; // the first write lands off target
    const detach = attachEditorScrollMemory(editor, "k", true);
    flush(); // first write, lands at 127.25
    expect(state.line).toBeCloseTo(127.25);
    flush(); // corrected
    expect(state.line).toBeCloseTo(120.25);
    flush(3);
    expect(frames).toHaveLength(0); // settled and stopped
    expect(state.scrolls).toBe(2);
    expect(loadScrollPos("k")).toBeCloseTo(120.25);
    detach();
  });

  it("does not record its own restore writes as the position", () => {
    saveScrollPos("k", 50);
    const { editor, state } = fakeEditor();
    state.drift = 3;
    const detach = attachEditorScrollMemory(editor, "k", true);
    flush();
    editor.scroller.dispatchEvent(new Event("scroll")); // our own write's echo, at 53
    expect(loadScrollPos("k")).toBe(50);
    detach();
  });

  it("stops restoring on reader input", () => {
    saveScrollPos("k", 80);
    const { editor, state } = fakeEditor();
    state.drift = 5;
    const detach = attachEditorScrollMemory(editor, "k", true);
    flush();
    editor.scroller.dispatchEvent(new WheelEvent("wheel", { bubbles: true }));
    flush(3);
    expect(state.scrolls).toBe(1);
    expect(loadScrollPos("k")).toBe(85); // where the reader took over
    detach();
  });

  it("leaves the editor alone when restore is off", () => {
    saveScrollPos("k", 80);
    const { editor, state } = fakeEditor();
    const detach = attachEditorScrollMemory(editor, "k", false);
    flush(3);
    expect(state.scrolls).toBe(0);
    detach();
  });

  it("waits while the editor has no box and restores once it gets one", () => {
    saveScrollPos("k", 300);
    const { editor, state } = fakeEditor(false);
    const detach = attachEditorScrollMemory(editor, "k", true);
    flush(3); // hidden: no frames kept running, no budget spent, nothing written
    expect(frames).toHaveLength(0);
    expect(state.scrolls).toBe(0);
    state.visible = true;
    resize(editor.scroller);
    flush(4);
    expect(state.line).toBe(300);
    detach();
  });

  it("restores again when a hidden editor comes back after settling", () => {
    saveScrollPos("k", 300);
    const { editor, state } = fakeEditor();
    const detach = attachEditorScrollMemory(editor, "k", true);
    flush(4);
    expect(state.line).toBe(300);
    // Hidden (display:none resets scrollTop), then shown again.
    state.visible = false;
    resize(editor.scroller);
    state.line = 1;
    state.visible = true;
    resize(editor.scroller);
    flush(4);
    expect(state.line).toBe(300);
    detach();
  });

  it("gives up early when the recorded line is past the end of a shorter document", () => {
    saveScrollPos("k", 900);
    const { editor, state } = fakeEditor(true, 200);
    const detach = attachEditorScrollMemory(editor, "k", true);
    flush(10);
    expect(frames).toHaveLength(0);
    expect(state.scrolls).toBeLessThanOrEqual(3);
    expect(loadScrollPos("k")).toBe(200); // where it could get to
    detach();
  });

  it("restores again when a late re-measure moves the editor after it settled", () => {
    saveScrollPos("k", 337);
    const { editor, state } = fakeEditor();
    const detach = attachEditorScrollMemory(editor, "k", true);
    flush(4);
    expect(frames).toHaveLength(0); // settled
    // CodeMirror measures lines it had estimated: the document grows under a still scrollTop.
    state.line = 321.5;
    editor.scroller.dispatchEvent(new Event("scroll"));
    resize(editor.content);
    flush(4);
    expect(state.line).toBe(337);
    expect(loadScrollPos("k")).toBe(337);
    detach();
  });

  it("leaves a re-measure alone once the reader has touched the pane", () => {
    saveScrollPos("k", 337);
    const { editor, state } = fakeEditor();
    const detach = attachEditorScrollMemory(editor, "k", true);
    flush(4);
    editor.scroller.dispatchEvent(new WheelEvent("wheel", { bubbles: true }));
    state.line = 321.5;
    resize(editor.content);
    expect(frames).toHaveLength(0);
    expect(state.line).toBe(321.5);
    detach();
  });

  it("stops re-restoring after a bounded number of re-measures", () => {
    saveScrollPos("k", 337);
    const { editor, state } = fakeEditor();
    const detach = attachEditorScrollMemory(editor, "k", true);
    flush(4);
    const moves: number[] = [];
    for (let i = 0; i < 7; i++) {
      state.line = 300;
      resize(editor.content);
      flush(4);
      moves.push(state.line);
    }
    expect(moves).toEqual([337, 337, 337, 337, 337, 300, 300]);
    detach();
  });

  it("does not chase a line it gave up on", () => {
    saveScrollPos("k", 900);
    const { editor, state } = fakeEditor(true, 200);
    const detach = attachEditorScrollMemory(editor, "k", true);
    flush(10);
    const scrolls = state.scrolls;
    resize(editor.content);
    flush(4);
    expect(state.scrolls).toBe(scrolls);
    detach();
  });

  it("ends the restore on input anywhere in the given root", () => {
    saveScrollPos("k", 80);
    const { editor, state } = fakeEditor();
    const pane = document.createElement("div");
    const preview = document.createElement("div");
    pane.append(editor.host, preview);
    state.drift = 5;
    const detach = attachEditorScrollMemory(editor, "k", true, pane);
    flush();
    preview.dispatchEvent(new WheelEvent("wheel", { bubbles: true }));
    flush(3);
    expect(state.scrolls).toBe(1);
    detach();
  });

  it("stops after detach", () => {
    saveScrollPos("k", 80);
    const { editor, state } = fakeEditor();
    const detach = attachEditorScrollMemory(editor, "k", true);
    detach();
    flush(3);
    expect(state.scrolls).toBe(0);
    state.line = 9;
    editor.scroller.dispatchEvent(new Event("scroll"));
    expect(loadScrollPos("k")).toBe(80);
  });
});
