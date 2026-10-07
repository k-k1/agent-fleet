// useLongPressMenu's lifetime: a press must not outlive what was pressed.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useLongPressMenu } from "./useLongPressMenu.ts";

const open = vi.fn();
let root: Root;
let host: HTMLDivElement;
let hideRow: () => void = () => {};

// Two rows sharing ONE hook, like the Files tree; "b" can be removed while the hook stays.
function Rows() {
  const lp = useLongPressMenu();
  const [showB, setShowB] = useState(true);
  hideRow = () => setShowB(false);
  return (
    <div>
      <div id="a" {...lp.props(open)} />
      {showB && <div id="b" {...lp.props(open)} />}
    </div>
  );
}

const touch = (el: Element, type: string) => {
  const e = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(e, "touches", { value: type === "touchend" ? [] : [{ clientX: 20, clientY: 20 }] });
  el.dispatchEvent(e);
  return e;
};
const press = (el: Element) => {
  const p = new Event("pointerdown", { bubbles: true });
  Object.defineProperty(p, "pointerType", { value: "touch" });
  el.dispatchEvent(p);
  touch(el, "touchstart");
};

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  open.mockClear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  vi.useRealTimers();
  host.remove();
});

describe("useLongPressMenu lifetime", () => {
  it("opens after the hold (control)", async () => {
    await act(async () => root.render(<Rows />));
    await act(async () => {
      press(host.querySelector("#a")!);
      vi.advanceTimersByTime(600);
    });
    expect(open).toHaveBeenCalledTimes(1);
  });

  it("an unmount during the hold cancels it", async () => {
    await act(async () => root.render(<Rows />));
    await act(async () => press(host.querySelector("#a")!));
    await act(async () => root.unmount());
    await act(async () => vi.advanceTimersByTime(600));
    expect(open).not.toHaveBeenCalled();
  });

  it("a row that disappears during the hold opens nothing, though the hook lives on", async () => {
    await act(async () => root.render(<Rows />));
    await act(async () => press(host.querySelector("#b")!));
    await act(async () => hideRow());
    await act(async () => vi.advanceTimersByTime(600));
    expect(open).not.toHaveBeenCalled();
    // …and the other row still works afterwards.
    await act(async () => {
      press(host.querySelector("#a")!);
      vi.advanceTimersByTime(600);
    });
    expect(open).toHaveBeenCalledTimes(1);
  });

  it("an unmount right after a fired press leaves no timer behind", async () => {
    await act(async () => root.render(<Rows />));
    await act(async () => {
      press(host.querySelector("#a")!);
      vi.advanceTimersByTime(600);
      touch(host.querySelector("#a")!, "touchend"); // arms the swallow-window timer
    });
    expect(vi.getTimerCount()).toBeGreaterThan(0);
    await act(async () => root.unmount());
    expect(vi.getTimerCount()).toBe(0);
  });
});
