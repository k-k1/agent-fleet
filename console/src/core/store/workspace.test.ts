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
const toastMock = vi.fn();
vi.mock("../../ui/toast.ts", () => ({ toast: (...args: unknown[]) => toastMock(...args) }));
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

  // A Recreate / Clean home that ran in the background (ecs) and failed: the POST was
  // answered long ago, so the payload is the only carrier. Toasted once when it appears, not
  // on every frame that still holds it, and dropped when the CP drops it.
  it("toasts a background home wipe failure once and clears it with the CP", () => {
    toastMock.mockReset();
    useWorkspaceStore.setState({ state: "starting", homeWipeFailed: "" });
    useWorkspaceStore.getState().applyPush({ state: "stopped", homeWipeFailed: "exit 1" });
    useWorkspaceStore.getState().applyPush({ state: "stopped", homeWipeFailed: "exit 1" });
    expect(toastMock).toHaveBeenCalledTimes(1);
    expect(String(toastMock.mock.calls[0][0])).toContain("exit 1");
    expect(useWorkspaceStore.getState().homeWipeFailed).toBe("exit 1");
    useWorkspaceStore.getState().applyPush({ state: "running" });
    expect(useWorkspaceStore.getState().homeWipeFailed).toBe("");
  });

  // stale (a backend update not yet picked up) is decided by the CP alone. Hold whatever is
  // pushed and clear it when it goes — remembering it client-side would leave the
  // restart-needed badge up even after a restart resolved it.
  // ecs-ec2: the slot under this workspace is reserved for replacement (#1473). The CP says
  // so on running and stopped workspaces alike, and stops saying it once the move happened.
  it("adopts and clears the CP's slotReplace flag", () => {
    useWorkspaceStore.getState().applyPush({ state: "stopped", slotReplace: true });
    expect(useWorkspaceStore.getState().slotReplace).toBe(true);
    useWorkspaceStore.getState().applyPush({ state: "running" });
    expect(useWorkspaceStore.getState().slotReplace).toBe(false);
  });

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

// recreate / cleanHome report what the CP answered, and whether the workspace was left as it
// was. The Danger zone uses the second half to decide whether to reset the member's panes: a
// refusal (not available on this deployment, another operation holding the lease, a stop that
// failed) stopped nothing, so closing every pane for it would be damage for no reason.
describe("workspace store recreate / cleanHome failures", () => {
  const answer = (status: number, body: unknown) =>
    ({
      ok: status >= 200 && status < 300,
      status,
      statusText: "",
      headers: { get: () => "application/json" },
      text: () => Promise.resolve(JSON.stringify(body)),
    }) as unknown as Response;
  const serve = (lifecycle: Response) =>
    fetchMock.mockImplementation((...args: unknown[]) =>
      Promise.resolve(
        /recreate|clean-home/.test(String(args[0])) ? lifecycle : answer(200, { state: "running" }),
      ),
    );

  for (const op of ["recreate", "cleanHome"] as const) {
    it(`${op}: a refusal before anything stopped is localized and marked untouched`, async () => {
      serve(answer(501, { error: { code: "home_wipe_unsupported", message: "not available here" } }));
      const fail = await useWorkspaceStore.getState()[op](true);
      expect(fail?.untouched).toBe(true);
      expect(fail?.message).toContain("この配備では使えない操作です");
    });

    it(`${op}: a failure after the stop is not untouched`, async () => {
      serve(answer(500, { error: { code: "internal", message: "remove home/repos: permission denied" } }));
      const fail = await useWorkspaceStore.getState()[op](true);
      expect(fail).toEqual({ message: "remove home/repos: permission denied", untouched: false });
    });

    // ecs: the wipe is a task still running elsewhere; nothing was stopped.
    it(`${op}: a home task still running is untouched`, async () => {
      serve(answer(409, { error: { code: "home_operation_in_progress", message: "still running" } }));
      const fail = await useWorkspaceStore.getState()[op](true);
      expect(fail?.untouched).toBe(true);
    });

    it(`${op}: success is null`, async () => {
      serve(answer(200, { name: "af-ws-x", state: "running" }));
      expect(await useWorkspaceStore.getState()[op](true)).toBeNull();
    });
  }
});
