// Chip menu for reply suggestions (pin / remove). It is the only way in since the per-chip
// close button was dropped, so if either the open paths (right-click, long-press, Menu key)
// or "lifting after a long-press does not fire the chip" breaks, pinning and removing become
// unreachable. Only those two are pinned here.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useChipMenu, SuggestChipMenu } from "./SuggestChipMenu.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const applied: string[] = [];
const pinned: string[] = [];
const forgotten: string[] = [];

function Harness() {
  const chipMenu = useChipMenu();
  return (
    <div>
      <button
        type="button"
        data-testid="chip"
        onClick={() => {
          if (chipMenu.clickSwallowed()) return;
          applied.push("進めて");
        }}
        onKeyDown={(e) => chipMenu.onKeyDown(e, "進めて", false)}
        {...chipMenu.chipProps("進めて", false)}
      >
        進めて
      </button>
      {chipMenu.menu && (
        <SuggestChipMenu
          menu={chipMenu.menu}
          pinned={pinned.includes(chipMenu.menu.text)}
          onClose={chipMenu.close}
          onTogglePin={(t) => pinned.push(t)}
          onForget={(t) => forgotten.push(t)}
        />
      )}
    </div>
  );
}

function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<Harness />));
}

function chip() {
  const el = host!.querySelector<HTMLButtonElement>('[data-testid="chip"]');
  if (!el) throw new Error("chip not rendered");
  return el;
}

function menuItems() {
  return Array.from(document.querySelectorAll<HTMLButtonElement>(".suggest-menu .ui-menu-item"));
}

// jsdom has no TouchEvent constructor, so build the bare minimum needed: the touch coordinates.
function touch(el: HTMLElement, type: string, x = 10, y = 10) {
  const e = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(e, "touches", { value: type === "touchend" ? [] : [{ clientX: x, clientY: y }] });
  act(() => {
    el.dispatchEvent(e);
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  applied.length = 0;
  pinned.length = 0;
  forgotten.length = 0;
  vi.useRealTimers();
});

describe("useChipMenu / SuggestChipMenu", () => {
  it("opens on right-click and pins from the menu", () => {
    mount();
    act(() => {
      chip().dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 40, clientY: 60 }));
    });
    const items = menuItems();
    expect(items).toHaveLength(2); // pin / remove
    act(() => items[0].click());
    expect(pinned).toEqual(["進めて"]);
    expect(menuItems()).toHaveLength(0); // closes once an item runs
  });

  it("removes the suggestion from the menu", () => {
    mount();
    act(() => {
      chip().dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    act(() => menuItems()[1].click());
    expect(forgotten).toEqual(["進めて"]);
  });

  it("opens with the Menu key / Shift+F10 (keyboard-only)", () => {
    mount();
    act(() => {
      chip().dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true }));
    });
    expect(menuItems()).toHaveLength(2);
  });

  it("opens on a 500ms long-press and swallows the click that follows the lift", () => {
    vi.useFakeTimers();
    mount();
    touch(chip(), "touchstart");
    act(() => {
      vi.advanceTimersByTime(500);
    });
    expect(menuItems()).toHaveLength(2);
    touch(chip(), "touchend");
    act(() => chip().click()); // a synthesized click must not insert the suggestion
    expect(applied).toEqual([]);
    // The next ordinary tap inserts as usual — the swallow does not linger.
    touch(chip(), "touchstart");
    touch(chip(), "touchend");
    act(() => chip().click());
    expect(applied).toEqual(["進めて"]);
  });

  it("treats a swipe (chip row scroll) as not a long-press", () => {
    vi.useFakeTimers();
    mount();
    touch(chip(), "touchstart", 10, 10);
    touch(chip(), "touchmove", 60, 12); // the finger moved = a horizontal scroll
    act(() => {
      vi.advanceTimersByTime(500);
    });
    expect(menuItems()).toHaveLength(0);
    act(() => chip().click());
    expect(applied).toEqual(["進めて"]);
  });
});
