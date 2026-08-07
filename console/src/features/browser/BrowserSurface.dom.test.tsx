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
