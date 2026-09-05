// Unit tests for scrollMark (remembering the mirror's scroll position per session).
//
// jsdom has no layout, so the rectangles of the scroll container and the turns are built here as
// functions of the scroll position (as domSetup.ts notes, real measurements are only possible in a
// real browser - what is verified here is the arithmetic that turns rectangles into a scrollTop).
import { describe, it, expect, beforeEach } from "vitest";
import { applyMark, captureMark, clearMarks, saveMark, scrollTopForTurn, loadMark } from "./scrollMark.ts";

/** A scroll container holding a row of turns. Rectangles are content coordinates minus
 * el.scrollTop, exactly as a browser computes them. */
function fixture(turns: { idx: number; top: number; h: number }[], view = 100, content = 1000): HTMLElement {
  const el = document.createElement("div");
  Object.defineProperty(el, "clientHeight", { value: view, configurable: true });
  Object.defineProperty(el, "scrollHeight", { value: content, configurable: true });
  Object.defineProperty(el, "scrollTop", { value: 0, writable: true, configurable: true });
  el.getBoundingClientRect = () => new DOMRect(0, 0, 200, view);
  for (const t of turns) {
    const d = document.createElement("div");
    d.setAttribute("data-turn-idx", String(t.idx));
    d.getBoundingClientRect = () => new DOMRect(0, t.top - el.scrollTop, 200, t.h);
    el.appendChild(d);
  }
  document.body.appendChild(el);
  return el;
}

const TURNS = [
  { idx: 1, top: 0, h: 200 },
  { idx: 2, top: 200, h: 400 },
  { idx: 3, top: 600, h: 300 },
];

beforeEach(() => {
  document.body.innerHTML = "";
  clearMarks();
});

describe("captureMark", () => {
  it("captures the turn overlapping the top edge and its offset", () => {
    const el = fixture(TURNS);
    el.scrollTop = 250; // 50px into turn 2
    expect(captureMark(el, false)).toEqual({ atBottom: false, idx: 2, offset: -50 });
  });

  it("picks the lower turn exactly on a boundary, since not one px of the upper one is visible", () => {
    const el = fixture(TURNS);
    el.scrollTop = 200;
    expect(captureMark(el, false)).toEqual({ atBottom: false, idx: 2, offset: 0 });
  });

  it("carries whether the view was tail-following; whether to restore is the caller's call", () => {
    const el = fixture(TURNS);
    expect(captureMark(el, true)?.atBottom).toBe(true);
  });

  it("never anchors on a synthetic turn (optimistic echo / queued prompt)", () => {
    const el = fixture([{ idx: 1e9 + 3, top: 0, h: 100 }]);
    expect(captureMark(el, false)).toBeNull();
  });

  it("returns null when there are no turns, and when there is no container", () => {
    expect(captureMark(fixture([]), false)).toBeNull();
    expect(captureMark(null, false)).toBeNull();
  });
});

describe("applyMark", () => {
  it("restores the captured position: a round trip gives the same scrollTop", () => {
    const el = fixture(TURNS);
    el.scrollTop = 250;
    const mark = captureMark(el, false)!;
    el.scrollTop = 0; // the state right after switching away and coming back
    expect(applyMark(el, mark)).toBe(true);
    expect(el.scrollTop).toBe(250);
  });

  it("returns false when the anchor turn is not mounted, so the caller falls back to the tail", () => {
    const el = fixture(TURNS);
    expect(applyMark(el, { atBottom: false, idx: 99, offset: 0 })).toBe(false);
    expect(el.scrollTop).toBe(0);
  });

  it("never goes outside the scrollable range", () => {
    const el = fixture(TURNS, 100, 1000);
    expect(applyMark(el, { atBottom: false, idx: 3, offset: -5000 })).toBe(true);
    expect(el.scrollTop).toBe(900); // scrollHeight - clientHeight
    expect(applyMark(el, { atBottom: false, idx: 1, offset: 5000 })).toBe(true);
    expect(el.scrollTop).toBe(0);
  });
});

describe("scrollTopForTurn", () => {
  it("returns the position that puts the turn's top edge at the top of the view, less any offset", () => {
    const el = fixture(TURNS);
    el.scrollTop = 850; // scrolling back up from near the tail
    expect(scrollTopForTurn(el, 3)).toBe(600);
    expect(scrollTopForTurn(el, 3, 8)).toBe(592);
  });

  it("returns null for a turn that is not mounted", () => {
    expect(scrollTopForTurn(fixture(TURNS), 42)).toBeNull();
    expect(scrollTopForTurn(null, 1)).toBeNull();
  });
});

describe("carrying marks around", () => {
  it("remembers per session independently, and null clears it", () => {
    const a = { atBottom: false, idx: 2, offset: -50 };
    saveMark("s-a", a);
    expect(loadMark("s-a")).toEqual(a);
    expect(loadMark("s-b")).toBeNull(); // never leaks into another session
    saveMark("s-a", null);
    expect(loadMark("s-a")).toBeNull();
  });

  it("does nothing when the session name is empty (a pane right after launch)", () => {
    saveMark("", { atBottom: false, idx: 1, offset: 0 });
    expect(loadMark("")).toBeNull();
  });
});
