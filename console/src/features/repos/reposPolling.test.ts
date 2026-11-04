// startReposPolling must stand down while the push stream is paused for an unattended
// window (each tick makes the Agent run git status in every working copy) and read once
// on resume.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

let paused = false;
const pauseHandlers = new Set<() => void>();
vi.mock("../../core/push/events.ts", () => ({
  pushPaused: () => paused,
  onPushPause: (h: () => void) => {
    pauseHandlers.add(h);
    return () => pauseHandlers.delete(h);
  },
}));
vi.mock("../../core/store/workspace.ts", () => ({
  useWorkspaceStore: { getState: () => ({ state: "running" }) },
  wsRunning: (s: string) => s === "running",
}));
vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(),
  getTenant: () => "",
  isTransientErr: () => false,
}));

const { startReposPolling, useReposStore } = await import("./store.ts");

function setPaused(v: boolean) {
  paused = v;
  for (const h of pauseHandlers) h();
}

describe("startReposPolling and the idle pause", () => {
  const refresh = vi.fn(async () => {});
  let stop: () => void = () => {};

  beforeEach(() => {
    vi.useFakeTimers();
    paused = false;
    pauseHandlers.clear();
    refresh.mockClear();
    vi.stubGlobal("document", { hidden: false });
    useReposStore.setState({ refresh } as never);
  });
  afterEach(() => {
    stop();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("polls every 60 s while live", () => {
    stop = startReposPolling();
    expect(refresh).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(120_000);
    expect(refresh).toHaveBeenCalledTimes(3);
  });

  it("skips its ticks while paused and reads once on resume", () => {
    stop = startReposPolling();
    refresh.mockClear();
    setPaused(true);
    vi.advanceTimersByTime(180_000);
    expect(refresh).not.toHaveBeenCalled();
    setPaused(false);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("does not read on resume while the window is hidden", () => {
    stop = startReposPolling();
    setPaused(true);
    refresh.mockClear();
    vi.stubGlobal("document", { hidden: true });
    setPaused(false);
    expect(refresh).not.toHaveBeenCalled();
  });

  it("stops listening for pause edges after cleanup", () => {
    stop = startReposPolling();
    stop();
    stop = () => {};
    refresh.mockClear();
    setPaused(true);
    setPaused(false);
    vi.advanceTimersByTime(120_000);
    expect(refresh).not.toHaveBeenCalled();
    expect(pauseHandlers.size).toBe(0);
  });
});
