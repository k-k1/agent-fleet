// The view-nav is always a tablist (no select at any width): roving tabindex, arrow/Home/End
// keys that move the selection and the focus, and the active tab kept in view in a scrolling row.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ViewNav } from "./viewNav.tsx";

const views = [
  { key: "a", label: "Alpha" },
  { key: "b", label: "Beta" },
  { key: "c", label: "Gamma" },
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function Harness({ onPick }: { onPick?: (v: string) => void }) {
  const [v, setV] = useState("a");
  return (
    <ViewNav
      label="Views"
      views={views}
      value={v}
      onChange={(k) => {
        onPick?.(k);
        setV(k);
      }}
      prefix="t"
    />
  );
}
async function mount(onPick?: (v: string) => void) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<Harness onPick={onPick} />));
}
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

const tabs = () => [...host!.querySelectorAll<HTMLButtonElement>("[role=tab]")];
const press = (el: Element, key: string) =>
  act(async () => {
    el.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
  });

describe("ViewNav", () => {
  it("renders only a tablist, never a select", async () => {
    await mount();
    expect(host!.querySelector("select")).toBeNull();
    expect(host!.querySelector("[role=combobox]")).toBeNull();
    expect(host!.querySelector("[role=tablist]")).not.toBeNull();
    expect(tabs().map((b) => b.textContent)).toEqual(["Alpha", "Beta", "Gamma"]);
  });

  it("keeps aria-controls and a roving tabindex", async () => {
    await mount();
    expect(tabs().map((b) => b.tabIndex)).toEqual([0, -1, -1]);
    expect(tabs().every((b) => b.getAttribute("aria-controls") === "t-panel")).toBe(true);
    expect(tabs().map((b) => b.getAttribute("aria-selected"))).toEqual(["true", "false", "false"]);
  });

  it("moves selection and focus with the arrow keys, wrapping at the ends", async () => {
    await mount();
    await press(tabs()[0], "ArrowRight");
    expect(tabs()[1].getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(tabs()[1]);
    expect(tabs().map((b) => b.tabIndex)).toEqual([-1, 0, -1]);
    await press(tabs()[1], "ArrowLeft");
    expect(document.activeElement).toBe(tabs()[0]);
    await press(tabs()[0], "ArrowLeft");
    expect(tabs()[2].getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(tabs()[2]);
    await press(tabs()[2], "ArrowRight");
    expect(tabs()[0].getAttribute("aria-selected")).toBe("true");
  });

  it("jumps with Home and End", async () => {
    await mount();
    await press(tabs()[0], "End");
    expect(tabs()[2].getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(tabs()[2]);
    await press(tabs()[2], "Home");
    expect(tabs()[0].getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(tabs()[0]);
  });

  it("ignores other keys", async () => {
    const onPick = vi.fn();
    await mount(onPick);
    await press(tabs()[0], "ArrowDown");
    await press(tabs()[0], "a");
    expect(onPick).not.toHaveBeenCalled();
  });

  it("ignores modified keys and IME composition", async () => {
    const onPick = vi.fn();
    await mount(onPick);
    const first = tabs()[0];
    first.focus();
    for (const mod of [{ ctrlKey: true }, { altKey: true }, { metaKey: true }, { shiftKey: true }, { isComposing: true }]) {
      for (const key of ["ArrowRight", "End"]) {
        const ev = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...mod });
        await act(async () => {
          first.dispatchEvent(ev);
        });
        expect(ev.defaultPrevented).toBe(false);
      }
    }
    const ime = new KeyboardEvent("keydown", { key: "ArrowRight", keyCode: 229, bubbles: true, cancelable: true });
    await act(async () => {
      first.dispatchEvent(ime);
    });
    expect(ime.defaultPrevented).toBe(false);
    expect(onPick).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(first);
    expect(tabs()[0].getAttribute("aria-selected")).toBe("true");
  });

  it("scrolls only the tab row, never an ancestor, and focuses without scrolling", async () => {
    const spy = vi.fn();
    Element.prototype.scrollIntoView = spy;
    try {
      await mount();
      const row = host!.querySelector<HTMLElement>("[role=tablist]")!;
      row.getBoundingClientRect = () => ({ left: 0, right: 100 }) as DOMRect;
      tabs()[1].getBoundingClientRect = () => ({ left: 80, right: 160 }) as DOMRect;
      const focus = vi.spyOn(tabs()[1], "focus");
      await press(tabs()[0], "ArrowRight");
      expect(row.scrollLeft).toBe(60);
      expect(spy).not.toHaveBeenCalled();
      expect(focus).toHaveBeenCalledWith({ preventScroll: true });
    } finally {
      delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
    }
  });

  it("does not throw where scrollIntoView is missing (jsdom)", async () => {
    await expect(mount()).resolves.toBeUndefined();
  });

  it("shows a count beside the label and names the tab with it, only while it is above 0", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () =>
      root!.render(
        <ViewNav
          label="Views"
          views={[
            { key: "a", label: "Alpha", count: 3, ariaLabel: "Alpha, 3 proposed" },
            { key: "b", label: "Beta", count: 0, ariaLabel: "Beta, 0 proposed" },
            { key: "c", label: "Gamma" },
          ]}
          value="a"
          onChange={() => {}}
          prefix="t"
        />,
      ),
    );
    const [a, b, c] = tabs();
    expect(a.querySelector(".mem-viewnav-count")?.textContent).toBe("3");
    expect(a.getAttribute("aria-label")).toBe("Alpha, 3 proposed");
    expect(b.querySelector(".mem-viewnav-count")).toBeNull();
    expect(b.hasAttribute("aria-label")).toBe(false);
    expect(c.textContent).toBe("Gamma");
    expect(c.hasAttribute("aria-label")).toBe(false);
  });
});
