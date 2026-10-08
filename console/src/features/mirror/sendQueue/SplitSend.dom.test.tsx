// The split Send: the chevron exists only while a turn runs, and its menu holds the draft in the
// queue instead of sending it.
import { act } from "react";
import type React from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SendColumn } from "../parts/SendColumn.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
function render(el: React.ReactElement) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(el));
}
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const more = () => host!.querySelector<HTMLButtonElement>(".mirror-send-more");
const item = () => document.body.querySelector<HTMLButtonElement>('[role="menuitem"]');
const click = (el: Element) => act(() => { (el as HTMLElement).click(); });
const col = (over: Partial<React.ComponentProps<typeof SendColumn>> = {}) => {
  const p = {
    showMode: true, isPlan: false, modeLabel: "Bypass", modeDisabled: false, sendDisabled: false,
    onToggleMode: vi.fn(), onSend: vi.fn(), onQueue: vi.fn(), ...over,
  };
  render(<SendColumn {...p} />);
  return p;
};

describe("split Send", () => {
  it("shows the chevron only while a turn runs, and keeps the column to the chip and one Send", () => {
    col({ onQueue: undefined });
    expect(more()).toBeNull();
    expect(host!.querySelectorAll(".mirror-send").length).toBe(1);
    act(() => root!.unmount());
    host!.remove();
    col();
    expect(more()).not.toBeNull();
    expect(more()!.getAttribute("aria-haspopup")).toBe("menu");
    expect(host!.querySelector(".mirror-send-col")!.children.length).toBe(2); // mode chip + split Send
  });

  it("queues the draft from the menu without sending, and returns focus to the chevron", () => {
    const p = col();
    click(more()!);
    expect(more()!.getAttribute("aria-expanded")).toBe("true");
    click(item()!);
    expect(p.onQueue).toHaveBeenCalledTimes(1);
    expect(p.onSend).not.toHaveBeenCalled();
    expect(item()).toBeNull();
    expect(document.activeElement).toBe(more());
  });

  it("closes on Esc and on an outside press without queueing", () => {
    const p = col();
    click(more()!);
    act(() => { item()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
    expect(item()).toBeNull();
    expect(document.activeElement).toBe(more());
    click(more()!);
    expect(item()).not.toBeNull();
    act(() => { document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 })); });
    expect(item()).toBeNull();
    expect(p.onQueue).not.toHaveBeenCalled();
  });

  it("opens from the keyboard (ArrowDown)", () => {
    col();
    more()!.focus();
    act(() => { more()!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, cancelable: true })); });
    expect(item()).not.toBeNull(); // focus on the item is useMenuRoving's (jsdom has no offsetParent)
  });

  it("disables the chevron together with Send, and closes an open menu when it disables", () => {
    col({ sendDisabled: true });
    expect(more()!.disabled).toBe(true);
    expect(host!.querySelector<HTMLButtonElement>(".mirror-send")!.disabled).toBe(true);
    act(() => root!.unmount());
    host!.remove();
    const p = { showMode: false, isPlan: false, modeLabel: "", modeDisabled: false, onToggleMode: vi.fn(), onSend: vi.fn(), onQueue: vi.fn() };
    render(<SendColumn {...p} sendDisabled={false} />);
    click(more()!);
    expect(item()).not.toBeNull();
    act(() => root!.render(<SendColumn {...p} sendDisabled={true} />));
    expect(item()).toBeNull();
  });
});
