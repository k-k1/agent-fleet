import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BrowserSurface, type BrowserSurfaceController } from "./BrowserSurface.tsx";

class FakeResizeObserver {
  observe() {}
  disconnect() {}
}

class FakeIntersectionObserver {
  observe() {}
  disconnect() {}
}

describe("BrowserSurface visibility", () => {
  let host: HTMLDivElement;
  let root: Root;
  let rect: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
    rect = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      x: 0, y: 0, width: 800, height: 600, top: 0, right: 800, bottom: 600, left: 0,
      toJSON: () => ({}),
    });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });

  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    rect.mockRestore();
    vi.unstubAllGlobals();
  });

  it("does not change lifecycle visibility when input becomes locked", async () => {
    const controller: BrowserSurfaceController = {
      mount: vi.fn(),
      unmount: vi.fn(),
      setVisible: vi.fn(),
      setViewport: vi.fn(),
      sendInput: vi.fn(),
    };
    const render = (inputEnabled: boolean) => (
      <BrowserSurface
        controller={controller}
        snapshot={{ title: "Review", width: 800, height: 600 }}
        canvasLabel="Browser"
        inputLabel="Input"
        inputEnabled={inputEnabled}
      />
    );

    await act(async () => root.render(render(true)));
    expect(controller.setVisible).toHaveBeenLastCalledWith(true);
    const callsBeforeLock = vi.mocked(controller.setVisible).mock.calls.length;

    await act(async () => root.render(render(false)));
    expect(controller.setVisible).toHaveBeenCalledTimes(callsBeforeLock);
    expect(controller.setVisible).not.toHaveBeenCalledWith(false);
  });
});

describe("BrowserSurface drag and clipboard", () => {
  let host: HTMLDivElement;
  let root: Root;
  let rect: ReturnType<typeof vi.spyOn>;
  let controller: BrowserSurfaceController;

  const sent = () => vi.mocked(controller.sendInput).mock.calls.map((call) => call[0]);
  const canvas = () => host.querySelector("canvas") as HTMLCanvasElement;
  const ime = () => host.querySelector("input") as HTMLInputElement;

  beforeEach(async () => {
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
    rect = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      x: 0, y: 0, width: 800, height: 600, top: 0, right: 800, bottom: 600, left: 0,
      toJSON: () => ({}),
    });
    // jsdom has no Pointer Capture; the surface uses it to keep a drag on the canvas.
    Object.assign(HTMLCanvasElement.prototype, {
      setPointerCapture() {},
      releasePointerCapture() {},
      hasPointerCapture: () => true,
    });
    controller = {
      mount: vi.fn(), unmount: vi.fn(), setVisible: vi.fn(), setViewport: vi.fn(),
      sendInput: vi.fn(), copySelection: vi.fn(async () => true),
    };
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root.render(
      <BrowserSurface
        controller={controller}
        snapshot={{ title: "Review", width: 800, height: 600 }}
        canvasLabel="Browser"
        inputLabel="Input"
      />,
    ));
  });

  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    rect.mockRestore();
    vi.unstubAllGlobals();
  });

  // Measured against a real Chromium: a move sent as button "none" is a hover,
  // so the scrollbar thumb (and every other drag) never moved.
  it("carries the held button on a move so drags reach the page", async () => {
    const pointer = (type: string, init: PointerEventInit) => act(async () => {
      canvas().dispatchEvent(new PointerEvent(type, { bubbles: true, cancelable: true, pointerId: 1, ...init }));
    });
    await pointer("pointerdown", { clientX: 100, clientY: 590, button: 0, buttons: 1 });
    await pointer("pointermove", { clientX: 300, clientY: 590, button: -1, buttons: 1 });
    await pointer("pointerup", { clientX: 300, clientY: 590, button: 0, buttons: 0 });

    const moves = sent().filter((m) => m.type === "mouse" && m.event === "move");
    expect(moves).toHaveLength(1);
    expect(moves[0]).toMatchObject({ button: "left", buttons: 1 });
    // A move with no button held is still a plain hover.
    await pointer("pointermove", { clientX: 320, clientY: 100, button: -1, buttons: 0 });
    expect(sent().filter((m) => m.type === "mouse" && m.event === "move").at(-1)).toMatchObject({ button: "none" });
  });

  // React registers `wheel` on the root with {passive: true}, so an onWheel prop
  // cannot preventDefault and the Console's own container scrolled along with
  // the page. The listener has to be a native, non-passive one on the canvas.
  it("cancels the wheel so only the remote page scrolls", async () => {
    const wheel = new WheelEvent("wheel", {
      bubbles: true, cancelable: true, clientX: 400, clientY: 300, deltaX: 0, deltaY: 3, deltaMode: 1,
    });
    await act(async () => { canvas().dispatchEvent(wheel); });
    expect(wheel.defaultPrevented).toBe(true);
    // deltaMode 1 is LINES; forwarding it raw scrolls the page by 3 pixels.
    expect(sent().at(-1)).toMatchObject({ type: "wheel", deltaY: 48 });
  });

  // Keys are delivered by the hidden IME input, and focus used to reach it only
  // via a canvas pointerdown — so typing did nothing until the user happened to
  // click the page first.
  it("moves focus to the input when the canvas is focused without a click", async () => {
    expect(canvas().tabIndex).toBe(0);
    await act(async () => { canvas().focus(); });
    expect(document.activeElement).toBe(ime());

    const key = new KeyboardEvent("keydown", { key: "ArrowDown", code: "ArrowDown", bubbles: true, cancelable: true });
    await act(async () => { ime().dispatchEvent(key); });
    expect(sent().at(-1)).toMatchObject({ type: "key", event: "down", key: "ArrowDown" });
  });

  // The remote Chromium's clipboard lives in the container. Ctrl+C must ask the
  // page for its selection; Ctrl+V must reach the hidden input natively so the
  // browser's own paste event carries the USER's clipboard.
  it("routes Ctrl+C to the page selection and lets Ctrl+V paste through", async () => {
    const copy = new KeyboardEvent("keydown", { key: "c", ctrlKey: true, bubbles: true, cancelable: true });
    await act(async () => { ime().dispatchEvent(copy); });
    expect(controller.copySelection).toHaveBeenCalled();
    expect(copy.defaultPrevented).toBe(true);
    expect(sent().some((m) => m.type === "key")).toBe(false);

    const paste = new KeyboardEvent("keydown", { key: "v", ctrlKey: true, bubbles: true, cancelable: true });
    await act(async () => { ime().dispatchEvent(paste); });
    // NOT prevented: the native paste is what puts the user's clipboard in reach.
    expect(paste.defaultPrevented).toBe(false);
    expect(sent().some((m) => m.type === "key")).toBe(false);

    const event = new Event("paste", { bubbles: true, cancelable: true }) as Event & { clipboardData: unknown };
    event.clipboardData = { getData: () => "貼り付けたい文字" };
    await act(async () => { ime().dispatchEvent(event); });
    expect(sent().at(-1)).toEqual({ type: "text", text: "貼り付けたい文字" });
  });
});

describe("BrowserSurface touch", () => {
  let host: HTMLDivElement;
  let root: Root;
  let rect: ReturnType<typeof vi.spyOn>;
  let controller: BrowserSurfaceController;

  const sent = () => vi.mocked(controller.sendInput).mock.calls.map((call) => call[0]);
  const canvas = () => host.querySelector("canvas") as HTMLCanvasElement;
  const touch = (type: string, init: PointerEventInit) => act(async () => {
    canvas().dispatchEvent(new PointerEvent(type, { bubbles: true, cancelable: true, pointerType: "touch", ...init }));
  });

  const render = async (inputEnabled: boolean) => {
    await act(async () => root.render(
      <BrowserSurface
        controller={controller}
        snapshot={{ title: "Review", width: 800, height: 600 }}
        canvasLabel="Browser"
        inputLabel="Input"
        inputEnabled={inputEnabled}
      />,
    ));
  };

  beforeEach(() => {
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
    rect = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      x: 0, y: 0, width: 800, height: 600, top: 0, right: 800, bottom: 600, left: 0,
      toJSON: () => ({}),
    });
    Object.assign(HTMLCanvasElement.prototype, {
      setPointerCapture() {}, releasePointerCapture() {}, hasPointerCapture: () => true,
    });
    controller = {
      mount: vi.fn(), unmount: vi.fn(), setVisible: vi.fn(), setViewport: vi.fn(),
      sendInput: vi.fn(), zoomBy: vi.fn(),
    };
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });

  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    rect.mockRestore();
    vi.unstubAllGlobals();
  });

  // Forwarding a finger as a mouse turned every swipe into a button-down drag:
  // the page selected text instead of scrolling, which is what made the pane
  // unusable on a phone.
  it("scrolls on a swipe instead of dragging the page", async () => {
    await render(true);
    await touch("pointerdown", { pointerId: 1, clientX: 400, clientY: 500 });
    await touch("pointermove", { pointerId: 1, clientX: 400, clientY: 460 });
    await touch("pointermove", { pointerId: 1, clientX: 400, clientY: 430 });

    expect(sent().some((m) => m.type === "mouse")).toBe(false);
    expect(sent()[0]).toMatchObject({ type: "wheel", deltaY: 40 });
    expect(sent()[1]).toMatchObject({ type: "wheel", deltaY: 30 });
  });

  it("clicks on a tap and hands focus to the input", async () => {
    await render(true);
    await touch("pointerdown", { pointerId: 1, clientX: 120, clientY: 90 });
    await touch("pointerup", { pointerId: 1, clientX: 121, clientY: 90 });

    expect(sent().map((m) => (m.type === "mouse" ? m.event : m.type))).toEqual(["move", "down", "up"]);
    expect(document.activeElement).toBe(host.querySelector("input"));
  });

  // Zoom is the viewer's own layout viewport, not page input, so it must survive
  // the view-only default — the state a fresh attachment actually starts in.
  it("still pinches to zoom while the page refuses input", async () => {
    await render(false);
    await touch("pointerdown", { pointerId: 1, clientX: 300, clientY: 300 });
    await touch("pointerdown", { pointerId: 2, clientX: 400, clientY: 300 });
    await touch("pointermove", { pointerId: 2, clientX: 500, clientY: 300 });
    await touch("pointerup", { pointerId: 2, clientX: 500, clientY: 300 });

    expect(controller.zoomBy).toHaveBeenCalledWith(2);
    expect(sent()).toEqual([]);
  });
});
