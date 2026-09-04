// The reply-suggestion chip row is conditionally rendered: it leaves the DOM and comes back while
// streaming (chat) and while AUQ/plan lock the composer (mirror). An effect that only reads a ref
// object cannot see the remount (assigning to a ref triggers neither a re-render nor an effect),
// so the new element gets no listeners and wheel scrolling dies. These tests reproduce that
// leave-and-return and guarantee it still works afterwards.
import { describe, it, expect, afterEach } from "vitest";
import { useRef } from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useDragScroll } from "./dragScroll.ts";

// jsdom has no layout, so build an overflowing row by hand (900px of content in a 300px window).
function makeOverflowing(el: HTMLElement) {
  Object.defineProperty(el, "scrollWidth", { value: 900, configurable: true });
  Object.defineProperty(el, "clientWidth", { value: 300, configurable: true });
  let left = 0;
  Object.defineProperty(el, "scrollLeft", {
    configurable: true,
    get: () => left,
    set: (v: number) => {
      left = v;
    },
  });
}

function Harness({ show }: { show: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const attach = useDragScroll(ref);
  return show ? <div data-testid="row" ref={attach} /> : null;
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

function mount(show: boolean) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<Harness show={show} />));
}

function render(show: boolean) {
  act(() => root!.render(<Harness show={show} />));
}

function row() {
  const el = host!.querySelector<HTMLDivElement>('[data-testid="row"]');
  if (!el) throw new Error("row not rendered");
  return el;
}

function wheel(el: HTMLElement, deltaY: number) {
  el.dispatchEvent(new WheelEvent("wheel", { deltaY, bubbles: true, cancelable: true }));
}

describe("useDragScroll", () => {
  it("turns a vertical wheel into horizontal scroll", () => {
    mount(true);
    const el = row();
    makeOverflowing(el);
    wheel(el, 120);
    expect(el.scrollLeft).toBe(120);
  });

  it("still works after the row unmounts and comes back (streaming / composer lock)", () => {
    mount(true);
    makeOverflowing(row());
    render(false); // the row disappears, e.g. while streaming
    render(true); // and comes back, as a different DOM element
    const el = row();
    makeOverflowing(el);
    wheel(el, 120);
    expect(el.scrollLeft).toBe(120);
  });

  it("attaches even when the row is absent on first mount", () => {
    mount(false); // no row on the first pass (opened while locked, say)
    render(true);
    const el = row();
    makeOverflowing(el);
    wheel(el, 120);
    expect(el.scrollLeft).toBe(120);
  });
});
