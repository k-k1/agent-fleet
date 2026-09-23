// An outside press must only close the open popover: the page underneath must not also see the
// click (a send button, a session row, the terminal). These tests drive the event sequences a
// mouse, a tap and a lone synthetic mousedown produce, and check what reaches the page.
import { describe, it, expect, afterEach, vi } from "vitest";
import { useRef, useState } from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useDismiss } from "./useDismiss.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

function Harness({ onPage, onItem, second }: { onPage: () => void; onItem: () => void; second?: boolean }) {
  const [open, setOpen] = useState(true);
  const [open2, setOpen2] = useState(!!second);
  const ref = useRef<HTMLDivElement>(null);
  const ref2 = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  useDismiss(ref2, open2, () => setOpen2(false));
  return (
    <>
      <button data-testid="page" onClick={onPage} onMouseDown={onPage}>
        page
      </button>
      {open && (
        <div data-testid="menu" ref={ref}>
          <button data-testid="item" onClick={onItem}>
            item
          </button>
        </div>
      )}
      {open2 && <div data-testid="menu2" ref={ref2} />}
    </>
  );
}

function mount(props: Parameters<typeof Harness>[0]) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<Harness {...props} />));
}

const q = (id: string) => document.querySelector<HTMLElement>(`[data-testid="${id}"]`);

function pointer(type: string, el: HTMLElement, pointerType = "mouse") {
  const e = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0 });
  Object.assign(e, { pointerType, isPrimary: true });
  el.dispatchEvent(e);
}

function mouseClick(el: HTMLElement) {
  act(() => {
    pointer("pointerdown", el);
    el.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, button: 0 }));
    pointer("pointerup", el);
    el.dispatchEvent(new MouseEvent("mouseup", { bubbles: true, cancelable: true, button: 0 }));
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0, detail: 1 }));
  });
}

describe("useDismiss outside press", () => {
  it("closes the menu and keeps the whole click from the page", () => {
    const onPage = vi.fn();
    mount({ onPage, onItem: vi.fn() });
    mouseClick(q("page")!);
    expect(q("menu")).toBeNull();
    expect(onPage).not.toHaveBeenCalled();
    // The next click is an ordinary one again.
    mouseClick(q("page")!);
    expect(onPage).toHaveBeenCalledTimes(2); // mousedown + click
  });

  it("swallows a tap whose click arrives after touchend", () => {
    const onPage = vi.fn();
    mount({ onPage, onItem: vi.fn() });
    const el = q("page")!;
    act(() => {
      pointer("pointerdown", el, "touch");
      pointer("pointerup", el, "touch");
      el.dispatchEvent(new Event("touchend", { bubbles: true, cancelable: true }));
      el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail: 1 }));
    });
    expect(q("menu")).toBeNull();
    expect(onPage).not.toHaveBeenCalled();
  });

  it("treats a lone mousedown as the press", () => {
    const onPage = vi.fn();
    mount({ onPage, onItem: vi.fn() });
    const el = q("page")!;
    act(() => {
      el.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, button: 0 }));
      el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail: 1 }));
    });
    expect(q("menu")).toBeNull();
    expect(onPage).not.toHaveBeenCalled();
  });

  it("lets a click inside the menu through", () => {
    const onItem = vi.fn();
    mount({ onPage: vi.fn(), onItem });
    mouseClick(q("item")!);
    expect(onItem).toHaveBeenCalledTimes(1);
    expect(q("menu")).not.toBeNull();
  });

  it("a press inside one popover closes the other without being swallowed", () => {
    const onItem = vi.fn();
    mount({ onPage: vi.fn(), onItem, second: true });
    mouseClick(q("item")!);
    expect(q("menu2")).toBeNull();
    expect(q("menu")).not.toBeNull();
    expect(onItem).toHaveBeenCalledTimes(1);
  });

  it("does not swallow a keyboard click (detail 0) after an outside press", () => {
    const onPage = vi.fn();
    mount({ onPage, onItem: vi.fn() });
    const el = q("page")!;
    act(() => {
      pointer("pointerdown", el);
      el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail: 0 }));
    });
    expect(onPage).toHaveBeenCalledTimes(1);
  });

  it("does not swallow a secondary press", () => {
    const onPage = vi.fn();
    mount({ onPage, onItem: vi.fn() });
    const el = q("page")!;
    act(() => {
      el.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, button: 2 }));
    });
    expect(q("menu")).toBeNull();
    expect(onPage).toHaveBeenCalledTimes(1);
  });
});
