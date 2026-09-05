// Contract test for the workspace store's push apply (traffic reduction P3). Pins that the
// same protection as the polling path — never clobber the state during an optimistic "…"
// transition — also holds for applyPush. Break it and a push frame arriving right after a
// stop makes the button clickable again, or closes the starting dialog.
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

// The store imports the api client, which binds window.fetch and reads document.baseURI, so
// the globals are stubbed before the import (the same style as repos/store.test.ts).
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
vi.stubGlobal("document", { baseURI: "http://localhost/", hidden: false });
const fetchMock = vi.fn<() => Promise<Response>>();
vi.stubGlobal("window", { fetch: fetchMock });
vi.stubGlobal("fetch", fetchMock);

let useWorkspaceStore: typeof import("./workspace.ts")["useWorkspaceStore"];
let wsBusy: typeof import("./workspace.ts")["wsBusy"];
let wsPowerStops: typeof import("./workspace.ts")["wsPowerStops"];
let wsStartBusy: typeof import("./workspace.ts")["wsStartBusy"];
beforeAll(async () => {
  ({ useWorkspaceStore, wsBusy, wsPowerStops, wsStartBusy } = await import("./workspace.ts"));
});

describe("workspace store applyPush", () => {
  beforeEach(() => {
    useWorkspaceStore.setState({ state: "running", bootPhase: "" });
  });

  it("adopts pushed state in steady state", () => {
    useWorkspaceStore.getState().applyPush({ state: "stopped" });
    expect(useWorkspaceStore.getState().state).toBe("stopped");
  });

  it("never clobbers an optimistic transition (settle refresh owns it)", () => {
    useWorkspaceStore.setState({ state: "stopping…" });
    useWorkspaceStore.getState().applyPush({ state: "running" });
    expect(useWorkspaceStore.getState().state).toBe("stopping…");
  });

  it("updates only bootPhase while starting (the starting dialog's live line)", () => {
    useWorkspaceStore.setState({ state: "starting…", bootPhase: "" });
    useWorkspaceStore.getState().applyPush({ state: "running", bootPhase: "boot-install: claude-code@1" });
    expect(useWorkspaceStore.getState().state).toBe("starting…");
    expect(useWorkspaceStore.getState().bootPhase).toBe("boot-install: claude-code@1");
  });

  it("folds a missing pushed state to unknown (poll parity)", () => {
    useWorkspaceStore.getState().applyPush({});
    expect(useWorkspaceStore.getState().state).toBe("unknown");
  });

  // stale (a backend update not yet picked up) is decided by the CP alone. Hold whatever is
  // pushed and clear it when it goes — remembering it client-side would leave the
  // restart-needed badge up even after a restart resolved it.
  it("adopts and clears the CP-detected stale flag", () => {
    useWorkspaceStore.getState().applyPush({ state: "running", stale: true });
    expect(useWorkspaceStore.getState().stale).toBe(true);
    useWorkspaceStore.getState().applyPush({ state: "running" });
    expect(useWorkspaceStore.getState().stale).toBe(false);
  });
});

// restart() is stop then start, nothing else. The URLs it calls pin that it never turns into
// recreate (which deletes ~/repos): losing uncommitted work to an update is unrecoverable.
describe("workspace store restart", () => {
  it("posts stop then start, never recreate", async () => {
    useWorkspaceStore.setState({ state: "running", bootPhase: "", stale: true });
    const calls: string[] = [];
    fetchMock.mockImplementation((...args: unknown[]) => {
      calls.push(String(args[0]));
      return Promise.resolve({
        ok: true,
        status: 200,
        headers: { get: () => "application/json" },
        json: () => Promise.resolve({ state: "running" }),
      } as unknown as Response);
    });
    await useWorkspaceStore.getState().restart();
    const posts = calls.filter((u) => u.includes("workspace/"));
    expect(posts[0]).toContain("api/workspace/stop");
    expect(posts[1]).toContain("api/workspace/start");
    expect(calls.some((u) => u.includes("recreate") || u.includes("clean-home"))).toBe(false);
  });
});

// Pins the way out of a `starting` state that never converges. When ECS cannot place a task
// it sits at desired=1/running=0 and State() reports "starting" forever (measured,
// docs/log/70 §70.14.6). If the power toggle only sent stop while running, the only action
// the UI offered from that state was Start, and the CP no-ops a Start for something it
// already considers starting — leaving no way at all to stop it from the Console.
describe("wsPowerStops / wsStartBusy", () => {
  it("stops — not starts — on the server-reported starting", () => {
    expect(wsPowerStops("starting")).toBe(true);
    expect(wsPowerStops("running")).toBe(true);
    expect(wsPowerStops("stopped")).toBe(false);
    expect(wsPowerStops("none")).toBe(false);
  });

  it("leaves the power button clickable while the server says starting", () => {
    // Only the optimistic "…" may disable the button. Disabling on "starting" would make
    // the stop path above unclickable and put us back in the same dead end.
    expect(wsBusy("starting")).toBe(false);
    expect(wsBusy("starting…")).toBe(true);
    expect(wsBusy("stopping…")).toBe(true);
  });

  it("still refuses to fire a second START while starting", () => {
    // Making stop reachable must not bring double-start back.
    expect(wsStartBusy("starting")).toBe(true);
    expect(wsStartBusy("starting…")).toBe(true);
    expect(wsStartBusy("stopped")).toBe(false);
  });
});
