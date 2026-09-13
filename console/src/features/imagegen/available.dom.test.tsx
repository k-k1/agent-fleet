// The "image generation" button of the ops bar and the layout map exists only while the
// fleet has an image engine (ADR 0081 decision 1). These check the store behind both buttons:
// hidden until a status names a fleet provider, hidden again when the status stops naming
// one, and unmoved by a transient failure.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { ImagegenStatus } from "./wire.ts";

const imagegenStatus = vi.fn();
vi.mock("./api.ts", () => ({ imagegenStatus: () => imagegenStatus() }));
vi.mock("../../core/api/client.ts", () => ({
  isTransientErr: (e: { error?: { code?: string } }) => e?.error?.code === "workspace_starting",
}));
let wsState = "running";
vi.mock("../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string }) => unknown) => sel({ state: wsState }),
  wsRunning: (s: string) => s === "running",
}));

const { useImagegenAvailable, _imagegenAvailability } = await import("./available.ts");

const comfy: ImagegenStatus = { enabled: true, ready: true, providers: [{ id: "comfy" } as never] };
const none: ImagegenStatus = { enabled: true, ready: true, providers: [{ id: "codex" } as never] };

function Probe() {
  return <span data-on={useImagegenAvailable() ? "1" : "0"} />;
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const on = () => host?.querySelector("span")?.getAttribute("data-on");
const mount = async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<Probe />);
  });
  await act(async () => {
    await Promise.resolve();
  });
};

beforeEach(() => {
  wsState = "running";
  _imagegenAvailability.reset();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  imagegenStatus.mockReset();
});

describe("imagegen availability", () => {
  it("is off until a status names a fleet provider, then on", async () => {
    imagegenStatus.mockResolvedValue(comfy);
    await mount();
    expect(on()).toBe("1");
    expect(imagegenStatus).toHaveBeenCalledTimes(1);
  });

  it("stays off when the only providers are CLI routes", async () => {
    imagegenStatus.mockResolvedValue(none);
    await mount();
    expect(on()).toBe("0");
  });

  it("does not ask while the workspace is not running", async () => {
    wsState = "stopped";
    imagegenStatus.mockResolvedValue(comfy);
    await mount();
    expect(on()).toBe("0");
    expect(imagegenStatus).not.toHaveBeenCalled();
  });

  it("keeps the last answer through a transient failure", async () => {
    imagegenStatus.mockResolvedValue(comfy);
    await mount();
    expect(on()).toBe("1");
    imagegenStatus.mockResolvedValue({ error: { code: "workspace_starting" } });
    await act(async () => {
      await _imagegenAvailability.refresh(true);
    });
    expect(on()).toBe("1");
  });
});
