// An explicit jump (scrollMark.requestJump — a past-session search hit, ADR 0110) into the
// session a mirror ALREADY shows. A mirror reads its mark only on a session switch, and opening a
// session that is on screen switches nothing, so without the jump listener the view stays put.
// jsdom has no layout: the turns' rectangles are functions of scrollTop, as in scrollMark's tests.
import { describe, it, expect, afterEach } from "vitest";
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useMirrorScroll } from "./useMirrorScroll.ts";
import { clearMarks, requestJump } from "../scrollMark.ts";

let api: ReturnType<typeof useMirrorScroll> | null = null;

function Harness() {
  const s = useMirrorScroll();
  api = s;
  useEffect(() => {
    const el = s.bodyRef.current!;
    Object.defineProperty(el, "clientHeight", { value: 100, configurable: true });
    Object.defineProperty(el, "scrollHeight", { value: 1000, configurable: true });
    Object.defineProperty(el, "scrollTop", { value: 0, writable: true, configurable: true });
    el.getBoundingClientRect = () => new DOMRect(0, 0, 200, 100);
    for (const t of [{ idx: 1, top: 0 }, { idx: 2, top: 200 }, { idx: 5, top: 600 }]) {
      const d = document.createElement("div");
      d.setAttribute("data-turn-idx", String(t.idx));
      d.getBoundingClientRect = () => new DOMRect(0, t.top - el.scrollTop, 200, 100);
      el.appendChild(d);
    }
    s.resetForSession("shown");
    s.applyFollow({ groups: [], loaded: true, busy: false, pending: null, pendingPlan: null, pendingPerm: null }); // the first settle: lands at the end
  }, []);
  return <div ref={s.bodyRef} />;
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<Harness />));
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  clearMarks();
  api = null;
});

describe("useMirrorScroll explicit jump", () => {
  it("moves a mirror already showing the session to the hit's block", () => {
    mount();
    const el = api!.bodyRef.current!;
    const atEnd = el.scrollTop;
    act(() => requestJump("other", { atBottom: false, idx: 2, offset: 0 }));
    expect(el.scrollTop).toBe(atEnd); // another session's jump is not this mirror's
    act(() => requestJump("shown", { atBottom: false, idx: 4, offset: 0, near: true }));
    expect(el.scrollTop).toBe(200); // idx 4 has no block of its own: the one holding it (2)
    expect(api!.atBottomRef.current).toBe(false); // follow is off, so the tail does not pull it back
  });

  it("leaves the view alone for a turn outside the loaded window", () => {
    mount();
    const el = api!.bodyRef.current!;
    el.scrollTop = 300;
    act(() => requestJump("shown", { atBottom: false, idx: 0, offset: 0, near: true }));
    expect(el.scrollTop).toBe(300);
  });
});
