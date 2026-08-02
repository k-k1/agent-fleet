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
