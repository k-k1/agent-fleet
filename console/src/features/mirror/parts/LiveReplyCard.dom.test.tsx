// The reply claude is still writing (#1250). It is a transient block: the Agent stops sending it
// once the real turn lands, so what matters here is that it reads as the agent's reply (same turn
// shell as the other pending cards, Markdown rendered) and offers nothing to act on.
//
// Typewriter mode (#1274) types a poll's new text out between polls. The rule itself is tested in
// typewriter.test.ts; here it is the card: frames show growing prefixes and end on the whole text,
// a text that does not extend the shown one lands at once, and reduced motion means no frames.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { LiveReplyCard } from "./pendingCards.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";
import { REDUCED_MOTION_QUERY } from "../../../lib/device.ts";
import { TYPEWRITER_TICK_MS } from "../useTypewriter.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function mount(text: string, mode?: "lines" | "typewriter") {
  if (!host) {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
  }
  act(() => {
    root!.render(
      <ToastProvider>
        <LiveReplyCard agentName="Claude" text={text} repo={null} onOpenFile={vi.fn()} mode={mode} />
      </ToastProvider>,
    );
  });
  return host;
}

// The rendered prose, without the streaming caret MarkdownView appends and the newline marked
// leaves after a paragraph.
const body = () => (host!.querySelector(".mirror-turn-body")?.textContent ?? "").replace(/▍/g, "").trimEnd();

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("LiveReplyCard", () => {
  it("renders the streamed text as the agent's turn, as Markdown", () => {
    const el = mount("1. **first**\n2. second");
    const turn = el.querySelector(".mirror-turn.assistant");
    expect(turn).not.toBeNull();
    expect(turn!.querySelector(".mt-who")?.textContent).toBe("Claude");
    expect(turn!.querySelectorAll("li")).toHaveLength(2);
    expect(turn!.querySelector("strong")?.textContent).toBe("first");
  });

  it("carries no controls of its own", () => {
    const el = mount("still writing");
    expect(el.querySelectorAll("button")).toHaveLength(0);
  });

  it("shows the whole text at once in line-by-line mode", () => {
    mount("a whole line at once", "lines");
    expect(body()).toBe("a whole line at once");
    expect(host!.querySelector(".md-stream-caret")).toBeNull();
  });
});

describe("LiveReplyCard typewriter", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "requestAnimationFrame", "cancelAnimationFrame", "performance"] });
  });

  const frames = (ms: number) => {
    const seen: string[] = [];
    for (let t = 0; t < ms; t += TYPEWRITER_TICK_MS) {
      act(() => vi.advanceTimersByTime(TYPEWRITER_TICK_MS));
      seen.push(body());
    }
    return seen;
  };

  it("types a poll's text out as growing prefixes and ends on the whole text", () => {
    const text = "Reviewing the input validation rules one at a time, starting with the parser.";
    mount(text, "typewriter");
    expect(host!.querySelector(".md-stream-caret")).not.toBeNull();
    const seen = frames(1500);
    const distinct = [...new Set(seen)];
    expect(distinct.length).toBeGreaterThan(4);
    for (let i = 0; i < seen.length; i++) {
      expect(text.startsWith(seen[i])).toBe(true);
      if (i > 0) expect(seen[i].length).toBeGreaterThanOrEqual(seen[i - 1].length);
    }
    expect(seen[seen.length - 1]).toBe(text);
  });

  it("keeps its place when the next poll extends the text, and stops animating once caught up", () => {
    mount("first line\n", "typewriter");
    frames(200);
    const partial = body();
    expect(partial.length).toBeGreaterThan(0);
    expect(partial.length).toBeLessThan("first line".length);
    mount("first line\nsecond line\n", "typewriter");
    expect(body()).toBe(partial);
    frames(1500);
    expect(body()).toBe("first line\nsecond line");
    const raf = vi.spyOn(window, "requestAnimationFrame");
    frames(500);
    expect(raf).not.toHaveBeenCalled();
  });

  it("shows a text that does not extend the shown one at once", () => {
    mount("abcdefghij", "typewriter");
    frames(100);
    expect(body().length).toBeLessThan(10);
    mount("something else entirely", "typewriter");
    expect(body()).toBe("something else entirely");
  });

  it("shows the whole text at once under reduced motion", () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === REDUCED_MOTION_QUERY,
      media: query,
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent: () => false,
    }));
    const raf = vi.spyOn(window, "requestAnimationFrame");
    mount("no animation for this reader, please", "typewriter");
    expect(body()).toBe("no animation for this reader, please");
    expect(host!.querySelector(".md-stream-caret")).toBeNull();
    frames(200);
    expect(raf).not.toHaveBeenCalled();
  });

  it("shows the whole text at once while the block is off screen", () => {
    let cb: IntersectionObserverCallback | null = null;
    vi.stubGlobal("IntersectionObserver", class {
      constructor(fn: IntersectionObserverCallback) { cb = fn; }
      observe() {}
      disconnect() {}
      unobserve() {}
      takeRecords() { return []; }
    });
    mount("scrolled away from this block", "typewriter");
    frames(100);
    expect(body().length).toBeLessThan(10);
    act(() => cb!([{ isIntersecting: false } as IntersectionObserverEntry], {} as IntersectionObserver));
    expect(body()).toBe("scrolled away from this block");
    mount("scrolled away from this block\nand more", "typewriter");
    expect(body()).toBe("scrolled away from this block\nand more");
  });
});
