// A band above the transcript (the title suggestion) changes the body's own height. jsdom has no
// layout, so the body's geometry is a set of mutable numbers, scrollTop clamps like a browser's, and
// the ResizeObserver is driven by hand.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useMirrorScroll } from "./useMirrorScroll.ts";
import { clearMarks } from "../scrollMark.ts";
import type { Group } from "../transcript/types.ts";

let api: ReturnType<typeof useMirrorScroll> | null = null;
let height = 500;
let top = 0;
let scroll = 0;
let shift = 0; // content that grew above the reply heads
const SCROLL_HEIGHT = 2000;
const clampScroll = (v: number) => Math.max(0, Math.min(v, SCROLL_HEIGHT - height));
let observers: (() => void)[] = [];

function Harness() {
  const s = useMirrorScroll();
  api = s;
  return <div ref={s.mirrorRef}><Body s={s} /></div>;
}

function Body({ s }: { s: ReturnType<typeof useMirrorScroll> }) {
  useEffect(() => {
    s.resetForSession("shown");
    s.applyFollow({ groups: [], loaded: true, busy: false, pending: null, pendingPlan: null, pendingPerm: null });
  }, []);
  return (
    <div
      ref={(el) => {
        if (!el || s.bodyRef.current === el) return;
        Object.defineProperty(el, "clientHeight", { get: () => height, configurable: true });
        Object.defineProperty(el, "scrollHeight", { value: SCROLL_HEIGHT, configurable: true });
        Object.defineProperty(el, "scrollTop", {
          get: () => scroll,
          set: (v: number) => (scroll = clampScroll(v)),
          configurable: true,
        });
        el.getBoundingClientRect = () => new DOMRect(0, top, 200, height);
        for (const [idx, at] of [[3, 1008], [7, 1538]]) { // reply heads, this far into the content
          const reply = document.createElement("div");
          reply.setAttribute("data-turn-idx", String(idx));
          reply.getBoundingClientRect = () => new DOMRect(0, top + at + shift - scroll, 200, 100);
          el.appendChild(reply);
        }
        s.bodyRef.current = el;
      }}
    />
  );
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;

// A band above moves the body's top edge by what it takes; a resize below leaves the top alone.
// Like a browser, a body that grows past the end clamps the offset (with its scroll event) first.
const resize = (h: number, fromTop: boolean) => {
  if (fromTop) top += height - h;
  height = h;
  const clamped = clampScroll(scroll);
  act(() => {
    if (clamped !== scroll) {
      scroll = clamped;
      api!.onBodyScroll();
    }
    observers.forEach((cb) => cb());
  });
};

// Frames for the glide (glide.ts), run by hand. Only the "glide" block below steps them; the rest
// asks for reduced motion, which keeps every automatic move the instant pin it asserts on.
let frames: FrameRequestCallback[] = [];
let reduceMotion = true;
let clock = 0;
const step = (n = 1) => {
  for (let i = 0; i < n; i++) {
    const due = frames;
    frames = [];
    clock += 16;
    act(() => due.forEach((cb) => cb(clock)));
  }
};

beforeEach(() => {
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => frames.push(cb));
  vi.stubGlobal("cancelAnimationFrame", () => {});
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: reduceMotion && q.includes("reduce") }));
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(cb: () => void) {
        observers.push(cb);
      }
      observe() {}
      disconnect() {}
    },
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<Harness />));
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  vi.unstubAllGlobals();
  clearMarks();
  api = null;
  height = 500;
  top = 0;
  scroll = 0;
  shift = 0;
  observers = [];
  frames = [];
  reduceMotion = true;
});

describe("useMirrorScroll body resize", () => {
  it("keeps the bottom edge in place off follow when a band above appears and goes", () => {
    const el = api!.bodyRef.current!;
    el.scrollTop = 300; // reading mid-transcript: bottom edge at 800
    api!.atBottomRef.current = false;
    resize(464, true);
    expect(el.scrollTop).toBe(336);
    resize(500, true);
    expect(el.scrollTop).toBe(300);
    expect(api!.atBottomRef.current).toBe(false); // holding the edge is not following
  });

  it("does not add the move to the browser's clamp when a band above goes near the end", () => {
    const el = api!.bodyRef.current!;
    api!.atBottomRef.current = false;
    resize(380, true);
    el.scrollTop = 1530; // 90px above the end, outside the follow zone: bottom edge at 1910
    api!.onBodyScroll();
    resize(500, true); // the browser clamps to 1500 (and scrolls) before the observer runs
    expect(el.scrollTop).toBe(1410);
    expect(api!.atBottomRef.current).toBe(false); // the clamp is not the reader coming back down
  });

  it("measures from our own last write when its scroll event has not arrived yet", () => {
    const el = api!.bodyRef.current!;
    resize(380, true); // following: pinned to the end, 1620
    expect(el.scrollTop).toBe(1620);
    api!.lastReplyIdxRef.current = 7;
    act(() => api!.jumpToReplyTop()); // our write to 1530, follow off, its scroll event still pending
    resize(500, true); // the band goes in the same task: the browser clamps to 1500
    expect(el.scrollTop).toBe(1410);
    expect(api!.atBottomRef.current).toBe(false);
  });

  it("does not shift a write that already chose its place after the band went", () => {
    const el = api!.bodyRef.current!;
    api!.atBottomRef.current = false;
    resize(380, true);
    top -= 120; // the band goes, the observer has not run yet
    height = 500;
    api!.lastReplyIdxRef.current = 3;
    act(() => api!.jumpToReplyTop()); // measured in the new layout: 1000
    act(() => observers.forEach((cb) => cb()));
    expect(el.scrollTop).toBe(1000);
  });

  it("keeps the top in place when the body shrinks from below (the composer growing)", () => {
    const el = api!.bodyRef.current!;
    el.scrollTop = 300;
    api!.atBottomRef.current = false;
    resize(380, false);
    expect(el.scrollTop).toBe(300);
  });

  it("still pins to the end while following", () => {
    const el = api!.bodyRef.current!;
    el.scrollTop = 1500;
    api!.atBottomRef.current = true;
    resize(380, true);
    expect(el.scrollTop).toBe(1620);
  });

  it("does not shift on a hidden pane coming back", () => {
    const el = api!.bodyRef.current!;
    api!.atBottomRef.current = false;
    resize(0, false);
    el.scrollTop = 300; // placed while hidden (a position restore)
    top = 40; // and the layout above changed meanwhile
    resize(500, false);
    expect(el.scrollTop).toBe(300);
  });
});

describe("useMirrorScroll glide", () => {
  // Lets every queued frame run, delivering each frame's scroll event the way a browser does.
  const settle = () => {
    for (let i = 0; i < 200 && frames.length; i++) {
      step();
      act(() => api!.onBodyScroll());
    }
  };

  it("glides up to the reply top without its first frames re-arming follow", () => {
    reduceMotion = false;
    const el = api!.bodyRef.current!;
    el.scrollTop = SCROLL_HEIGHT - height; // following, at the end: 1500
    api!.lastReplyIdxRef.current = 3;
    act(() => api!.jumpToReplyTop()); // 1500 → 1000
    expect(el.scrollTop).toBe(1500); // nothing moves until the first frame
    step();
    expect(el.scrollTop).toBeLessThan(1500);
    expect(el.scrollTop).toBeGreaterThan(1450); // accelerating from rest, still within the follow zone
    settle();
    expect(el.scrollTop).toBe(1000);
    expect(api!.atBottomRef.current).toBe(false);
  });

  const newContent = () =>
    act(() => api!.applyFollow({ groups: [], loaded: true, busy: false, pending: null, pendingPlan: null, pendingPerm: null }));

  it("chases the end while following, and a wheel stops it where it is", () => {
    reduceMotion = false;
    const el = api!.bodyRef.current!;
    el.scrollTop = 800;
    api!.atBottomRef.current = true;
    newContent(); // a poll brought more: glide to the end
    step(3);
    const mid = el.scrollTop;
    expect(mid).toBeGreaterThan(800);
    expect(mid).toBeLessThan(1500);
    act(() => api!.endRestoreOnInput()); // the reader's wheel
    step(3);
    expect(el.scrollTop).toBe(mid);
  });

  it("pins late layout under a view at the end at once", () => {
    reduceMotion = false;
    const el = api!.bodyRef.current!;
    el.scrollTop = 800;
    api!.atBottomRef.current = true;
    act(() => observers.forEach((cb) => cb())); // an image landing, no new content
    expect(el.scrollTop).toBe(1500);
    expect(frames).toHaveLength(1); // only the reply-top pill's sync, no glide
  });

  it("keeps a glide far behind the end within reach before the frame is painted", () => {
    reduceMotion = false;
    const el = api!.bodyRef.current!;
    el.scrollTop = 0;
    api!.atBottomRef.current = true;
    newContent(); // glide queued, its first frame not run yet
    act(() => observers.forEach((cb) => cb())); // the content's late layout lands first
    expect(el.scrollTop).toBe(1500 - 500 * 1.5);
    settle();
    expect(el.scrollTop).toBe(1500);
  });

  it("lands the opening at the end at once, glide or no glide", () => {
    reduceMotion = false;
    const el = api!.bodyRef.current!;
    act(() => {
      api!.resetForSession("another");
      api!.applyFollow({ groups: [], loaded: true, busy: false, pending: null, pendingPlan: null, pendingPerm: null });
    });
    expect(el.scrollTop).toBe(1500); // before any frame
  });

  it("yields to a scrollbar drag, which raises no pointerdown", () => {
    reduceMotion = false;
    const el = api!.bodyRef.current!;
    el.scrollTop = 800;
    api!.atBottomRef.current = true;
    newContent();
    step(2);
    el.scrollTop = 300; // the reader drags the thumb up
    act(() => api!.onBodyScroll());
    expect(api!.atBottomRef.current).toBe(false);
    expect(api!.showJump).toBe(true);
    step(5);
    expect(el.scrollTop).toBe(300); // not pulled back down
  });

  it("yields to a scrollbar drag back down to the end during a glide up", () => {
    reduceMotion = false;
    const el = api!.bodyRef.current!;
    el.scrollTop = 1500;
    api!.lastReplyIdxRef.current = 3;
    act(() => api!.jumpToReplyTop()); // glide up to 1000
    for (let i = 0; i < 4; i++) {
      step();
      act(() => api!.onBodyScroll());
    }
    expect(el.scrollTop).toBeLessThan(1500);
    el.scrollTop = 1500; // the reader drags the thumb back to the end
    act(() => api!.onBodyScroll());
    expect(api!.atBottomRef.current).toBe(true);
    expect(api!.showJump).toBe(false);
    step(5);
    expect(el.scrollTop).toBe(1500); // not pulled back up
  });

  it("does not take a move along with a glide's target for the reader", () => {
    reduceMotion = false;
    const el = api!.bodyRef.current!;
    el.scrollTop = 1300;
    api!.atBottomRef.current = false;
    api!.lastReplyIdxRef.current = 3;
    act(() => api!.jumpToReplyTop()); // glide up to 1000
    step(2);
    act(() => api!.onBodyScroll());
    shift = 40; // content above grew and scroll anchoring moved the view down with it
    el.scrollTop += 40;
    act(() => api!.onBodyScroll());
    settle();
    expect(el.scrollTop).toBe(1040); // still glided onto the reply head, wherever it went
  });

  it("shows jump-to-latest when the reader stops a completion glide after its last scroll event", () => {
    reduceMotion = false;
    const el = api!.bodyRef.current!;
    const turn = el.querySelector<HTMLElement>('[data-turn-idx="3"]')!;
    const body = document.createElement("div");
    body.className = "mirror-turn-body";
    const work = document.createElement("div");
    work.className = "mt-work";
    const answer = document.createElement("div");
    answer.getBoundingClientRect = () => new DOMRect(0, top + 1100 - scroll, 200, 100);
    body.append(work, answer);
    turn.append(body);
    const groups = [
      { role: "user", idx: 1, parts: [] },
      { role: "assistant", idx: 3, parts: [] },
    ] as unknown as Group[];
    const follow = (busy: boolean) =>
      act(() => api!.applyFollow({ groups, loaded: true, busy, pending: null, pendingPlan: null, pendingPerm: null }));
    el.scrollTop = 1500;
    api!.atBottomRef.current = true;
    follow(true); // the reply streams at the end
    settle();
    follow(false); // it completes: glide up to the answer (1088)
    expect(api!.atBottomRef.current).toBe(false);
    for (let i = 0; i < 8; i++) {
      step();
      act(() => api!.onBodyScroll());
    }
    expect(SCROLL_HEIGHT - height - el.scrollTop).toBeGreaterThan(80); // out of the follow zone
    expect(api!.showJump).toBe(false); // every scroll event so far was the glide's own
    act(() => api!.noteInteraction()); // a click in the transcript stops it
    step();
    expect(api!.atBottomRef.current).toBe(false);
    expect(api!.showJump).toBe(true);
  });
});
