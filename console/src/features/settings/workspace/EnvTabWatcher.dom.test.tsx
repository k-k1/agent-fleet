// The upstream release-watcher row of the environment tab, in its four states.
//
// What it is for: on 2026-09-09 one unreadable release source aborted cli-release-watch.yml,
// so a new claude was detected and dropped and the `tested` state stood still — which is
// indistinguishable from an upstream that released nothing. The row exists to make those
// two distinguishable, so the states that matter are "warned" and "not warned":
//   1. a recent clean run  → the line, no warning
//   2. rows that could not be read → the warning, naming them
//   3. no clean run for over 48h   → the warning
//   4. nothing known (no outbound network) → no row at all, and no warning either
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  getTenant: () => "default",
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
  raw: () => Promise.resolve(new Response("")),
}));
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) => sel({ state: "running", start: () => {} }),
  wsStartBusy: () => false,
}));
vi.mock("../../sessions/store.ts", () => ({
  useSessionsStore: (sel: (s: unknown) => unknown) => sel({ sessions: [] }),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));
vi.mock("../hostUpdate.ts", () => ({ useHostUpdate: () => null }));

import { EnvTab } from "./EnvTab.tsx";

const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const HOUR = 3600_000;

const toolchains = {
  node: "system",
  java: "",
  go: "system",
  timezone: "Asia/Tokyo",
  java_available: [],
  java_installed: [],
  node_options: ["system"],
  node_installed: [],
  go_options: ["system"],
  tz_options: ["Asia/Tokyo"],
};

// cliRelease as the CP serves it; `undefined` is the "never read" case, where the CP omits
// the key entirely.
function mockAPI(cliRelease?: Record<string, unknown>) {
  api.mockImplementation((path: string) => {
    if (path === "api/env/toolchains") return Promise.resolve(toolchains);
    if (path === "api/env/ws-settings") {
      return Promise.resolve({ agentUpdate: false, allowAgentUpdate: false, ...(cliRelease ? { cliRelease } : {}) });
    }
    return Promise.resolve({ tools: [], checkedAt: ago(0) });
  });
}

const healthy = {
  watcherOkAt: ago(HOUR),
  watcherFailed: [],
  watcherAt: ago(HOUR),
  tested: { claude: "2.1.267" },
  fetchedAt: ago(0),
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<EnvTab />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  localStorage.clear();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.doUnmock("../cliRelease.ts");
});

const row = () => document.querySelector(".cli-watch-row");
const warn = () => document.querySelector(".cli-watch-warn");

describe("EnvTab upstream release-watch row", () => {
  it("shows the line without a warning after a recent clean run", async () => {
    mockAPI(healthy);
    await mount();
    expect(row()).not.toBeNull();
    expect(row()!.textContent).toContain("上流のリリース監視");
    expect(warn()).toBeNull();
  });

  it("warns and names the rows the watcher could not read", async () => {
    mockAPI({ ...healthy, watcherFailed: ["rtk", "kiro"] });
    await mount();
    expect(warn()).not.toBeNull();
    expect(warn()!.textContent).toContain("rtk, kiro");
    // The point of the sentence: it has to say that a standing-still `tested` is NOT an
    // upstream that went quiet.
    expect(warn()!.textContent).toContain("tested");
  });

  it("warns when the last clean run is older than 48 hours", async () => {
    mockAPI({ ...healthy, watcherOkAt: ago(72 * HOUR), watcherAt: ago(72 * HOUR) });
    await mount();
    expect(warn()).not.toBeNull();
  });

  it("draws no row at all when the CP never got an answer", async () => {
    mockAPI(undefined);
    await mount();
    // The tool-versions section itself is still there — only the watcher row is absent.
    expect(document.querySelector(".tool-ver")).not.toBeNull();
    expect(row()).toBeNull();
    expect(warn()).toBeNull();
  });
});

// Positive control for the three cases above: the warning is drawn from watcherHealth's
// verdict and nothing else. Neuter the verdict and the warning has to disappear — without
// this, a row that rendered the warning unconditionally would pass every test above.
describe("EnvTab upstream release-watch row (positive control)", () => {
  it("loses the warning when the verdict is forced to ok", async () => {
    vi.resetModules();
    vi.doMock("../cliRelease.ts", async () => {
      const real = await vi.importActual<typeof import("../cliRelease.ts")>("../cliRelease.ts");
      return { ...real, watcherHealth: () => ({ kind: "ok", okAt: Date.now() }) };
    });
    const { EnvTab: Patched } = await import("./EnvTab.tsx");
    mockAPI({ ...healthy, watcherFailed: ["rtk", "kiro"] });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(<Patched />);
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(row()).not.toBeNull(); // the row is still drawn…
    expect(warn()).toBeNull(); // …but the warning came from the verdict
    vi.resetModules();
  });
});
