// A view with a find of its own (lib/ownFind.ts) gets Ctrl-F from PaneFind instead of PaneFind
// opening its DOM-scanning bar over it: the PDF pane searches pages that are not in the DOM.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneFind } from "./PaneFind.tsx";
import { OWN_FIND_ATTR, OWN_FIND_EVENT } from "../../lib/ownFind.ts";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

function Pane({ ownFind }: { ownFind: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  return (
    <div ref={ref}>
      <div className="view" tabIndex={0} {...(ownFind ? { [OWN_FIND_ATTR]: "" } : {})}>
        text to find
      </div>
      <PaneFind rootRef={ref} active enabled />
    </div>
  );
}

function mount(ownFind: boolean) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<Pane ownFind={ownFind} />));
  return host.querySelector<HTMLElement>(".view")!;
}

const ctrlF = (target: EventTarget) => {
  const e = new KeyboardEvent("keydown", { key: "f", ctrlKey: true, bubbles: true, cancelable: true });
  act(() => {
    target.dispatchEvent(e);
  });
  return e;
};

describe("PaneFind with a view that has its own find", () => {
  it("hands Ctrl-F to the view instead of opening its own bar", () => {
    const view = mount(true);
    const asked = vi.fn();
    view.addEventListener(OWN_FIND_EVENT, asked);
    const e = ctrlF(view);
    expect(asked).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
    expect(host!.querySelector(".pane-find")).toBeNull();
  });

  it("hands it over when nothing in particular has focus too", () => {
    const view = mount(true);
    const asked = vi.fn();
    view.addEventListener(OWN_FIND_EVENT, asked);
    ctrlF(document.body);
    expect(asked).toHaveBeenCalledTimes(1);
  });

  it("opens its own bar for an ordinary view", () => {
    const view = mount(false);
    ctrlF(view);
    expect(host!.querySelector(".pane-find")).not.toBeNull();
  });

  it("closes a bar opened before the view took over, then hands over", () => {
    // Ctrl-F while the file is still loading opens the generic bar; once the PDF view is there,
    // the next Ctrl-F must leave only the view's own bar.
    mount(false);
    ctrlF(document.body);
    expect(host!.querySelector(".pane-find")).not.toBeNull();
    act(() => root!.render(<Pane ownFind />));
    const asked = vi.fn();
    host!.querySelector(".view")!.addEventListener(OWN_FIND_EVENT, asked);
    ctrlF(document.body);
    expect(asked).toHaveBeenCalledTimes(1);
    expect(host!.querySelector(".pane-find")).toBeNull();
  });
});
