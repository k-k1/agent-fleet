// Phase 3.5 acceptance (docs/log/44 §7.2): the probe runs only when the tab is
// visible, the pane is visible, the workspace is running, and no PUT is in
// flight; it fires on visibility/focus/pane-activation and a slow interval,
// and it swallows unavailable results.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { FileProbeResult } from "./api.ts";

vi.mock("./api.ts", () => ({ probeFileMeta: vi.fn() }));
let wsState = "running";
vi.mock("../../core/store/workspace.ts", () => ({
  useWorkspaceStore: { getState: () => ({ state: wsState }) },
  wsRunning: (state: string) => state === "running",
}));

const { probeFileMeta } = await import("./api.ts");
const { EXTERNAL_PROBE_INTERVAL_MS, useExternalChangeProbe } = await import("./probe.ts");

let paneVisible = true;
let saving = false;
let results: FileProbeResult[] = [];
let hidden = false;

function Harness({ path, paneActive }: { path: string | null; paneActive: boolean }) {
  useExternalChangeProbe({
    path,
    paneActive,
    isPaneVisible: () => paneVisible,
    isSaving: () => saving,
    onResult: (result) => results.push(result),
  });
  return null;
}

let root: Root | null = null;

async function render(path: string | null, paneActive = false) {
  await act(async () => {
    root!.render(<Harness path={path} paneActive={paneActive} />);
  });
}

async function tick() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(EXTERNAL_PROBE_INTERVAL_MS);
  });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  vi.mocked(probeFileMeta).mockReset();
  wsState = "running";
  paneVisible = true;
  saving = false;
  results = [];
  hidden = false;
  Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
  root = createRoot(document.createElement("div"));
});

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  vi.useRealTimers();
  delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
});

describe("useExternalChangeProbe", () => {
  it("probes on the interval and delivers observations", async () => {
    const result: FileProbeResult = { kind: "revision", revision: "sha256:" + "a".repeat(64) };
    vi.mocked(probeFileMeta).mockResolvedValue(result);
    await render("repos/a.txt");
    expect(probeFileMeta).not.toHaveBeenCalled(); // the open itself just read the file

    await tick();
    expect(probeFileMeta).toHaveBeenCalledTimes(1);
    expect(probeFileMeta).toHaveBeenCalledWith("repos/a.txt");
    expect(results).toEqual([result]);
  });

  it("stays quiet while hidden, invisible, saving, stopped, or disabled", async () => {
    vi.mocked(probeFileMeta).mockResolvedValue({ kind: "missing" });
    await render(null);
    await tick();

    await render("repos/a.txt");
    hidden = true;
    await tick();
    hidden = false;
    paneVisible = false;
    await tick();
    paneVisible = true;
    saving = true;
    await tick();
    saving = false;
    wsState = "stopped";
    await tick();

    expect(probeFileMeta).not.toHaveBeenCalled();

    wsState = "running";
    await tick();
    expect(probeFileMeta).toHaveBeenCalledTimes(1);
  });

  it("probes immediately on returning to the tab and on pane activation", async () => {
    vi.mocked(probeFileMeta).mockResolvedValue({ kind: "missing" });
    await render("repos/a.txt");

    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
      await Promise.resolve();
    });
    expect(probeFileMeta).toHaveBeenCalledTimes(1);

    await render("repos/a.txt", true);
    expect(probeFileMeta).toHaveBeenCalledTimes(2);
  });

  it("swallows unavailable results and drops one that raced a save", async () => {
    vi.mocked(probeFileMeta).mockResolvedValue({ kind: "unavailable" });
    await render("repos/a.txt");
    await tick();
    expect(probeFileMeta).toHaveBeenCalledTimes(1);
    expect(results).toEqual([]);

    // A save that starts while the probe is in flight settles the base itself.
    let resolveProbe!: (r: FileProbeResult) => void;
    vi.mocked(probeFileMeta).mockImplementation(
      () => new Promise<FileProbeResult>((done) => { resolveProbe = done; }),
    );
    await tick();
    saving = true;
    await act(async () => {
      resolveProbe({ kind: "missing" });
      await Promise.resolve();
    });
    expect(results).toEqual([]);
  });
});
