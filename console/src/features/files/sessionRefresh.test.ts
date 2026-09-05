// How the "turn ended" edge is detected (features/files/sessionRefresh.ts).
//
// A wrong detector fails in two directions: miss an edge and the tree stops updating
// again, fire too often and the whole working copy is re-read over and over. Neither is
// visible on screen, so the definition of the edge is pinned here.
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { COALESCE_MS, MIN_GAP_MS, WORKING_TICK_MS } from "./refreshPolicy.ts";
import type { Session } from "../../types/session.ts";

// The wiring drags in the sessions store and through it the api client (localStorage,
// document.baseURI, fetch), so the globals are stubbed before importing, the same way
// store.test.ts does it. window.setTimeout delegates to whatever the global is at call
// time; without that, fake timers have no effect.
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
let hidden = false; // whether the tab is in the background (gates the low-frequency refresh)
vi.stubGlobal("document", {
  baseURI: "http://localhost/",
  get hidden() {
    return hidden;
  },
});
vi.stubGlobal("window", {
  fetch: vi.fn(),
  setTimeout: (...a: Parameters<typeof setTimeout>) => setTimeout(...a),
  clearTimeout: (...a: Parameters<typeof clearTimeout>) => clearTimeout(...a),
  setInterval: (...a: Parameters<typeof setInterval>) => setInterval(...a),
  clearInterval: (...a: Parameters<typeof clearInterval>) => clearInterval(...a),
});

let useWorkspaceStore: typeof import("../../core/store/workspace.ts")["useWorkspaceStore"];
let createTurnEndDetector: typeof import("./sessionRefresh.ts")["createTurnEndDetector"];
let isBusySession: typeof import("./sessionRefresh.ts")["isBusySession"];
let sessionPrefix: typeof import("./sessionRefresh.ts")["sessionPrefix"];
let wireFilesSessionRefresh: typeof import("./sessionRefresh.ts")["wireFilesSessionRefresh"];
let useSessionsStore: typeof import("../sessions/store.ts")["useSessionsStore"];
let useFilesStore: typeof import("./store.ts")["useFilesStore"];

beforeAll(async () => {
  ({ createTurnEndDetector, isBusySession, sessionPrefix, wireFilesSessionRefresh } = await import(
    "./sessionRefresh.ts"
  ));
  ({ useSessionsStore } = await import("../sessions/store.ts"));
  ({ useFilesStore } = await import("./store.ts"));
  ({ useWorkspaceStore } = await import("../../core/store/workspace.ts"));
});

const s = (over: Partial<Session>): Session => ({
  name: "s1",
  kind: "claude",
  alive: true,
  repo: "agent-fleet",
  ...over,
});

describe("isBusySession", () => {
  it("counts only working / compacting / background work as running", () => {
    expect(isBusySession(s({ state: "working" }))).toBe(true);
    expect(isBusySession(s({ state: "compacting" }))).toBe(true);
    // Idle as far as the hook can tell, but run_in_background may still be writing.
    expect(isBusySession(s({ state: "idle", backgroundBusy: true }))).toBe(true);
    expect(isBusySession(s({ state: "idle" }))).toBe(false);
    expect(isBusySession(s({ state: "question" }))).toBe(false);
    expect(isBusySession(s({ state: "" }))).toBe(false);
    // A stopped row is not running whatever its state says.
    expect(isBusySession(s({ state: "working", alive: false }))).toBe(false);
  });
});

describe("sessionPrefix", () => {
  it("turns the working copy folder into a home-relative path, worktree name and all", () => {
    expect(sessionPrefix({ repo: "agent-fleet" })).toBe("repos/agent-fleet");
    expect(sessionPrefix({ repo: "agent-fleet@wip-a" })).toBe("repos/agent-fleet@wip-a");
    expect(sessionPrefix({ repo: "" })).toBe("");
    expect(sessionPrefix({})).toBe("");
  });
});

describe("createTurnEndDetector", () => {
  it("does not fire on a first observation, so a reload does not refresh every session", () => {
    const detect = createTurnEndDetector();
    expect(detect([s({ state: "idle" })])).toEqual([]);
    expect(detect([s({ state: "question" })])).toEqual([]);
  });

  it("returns that one working copy on a running -> not-running transition", () => {
    const detect = createTurnEndDetector();
    detect([s({ state: "working" })]);
    expect(detect([s({ state: "idle" })])).toEqual(["repos/agent-fleet"]);
  });

  it("treats a turn paused on a human (question / plan / permission) as ended", () => {
    const detect = createTurnEndDetector();
    detect([s({ state: "working" })]);
    expect(detect([s({ state: "question" })])).toEqual(["repos/agent-fleet"]);
  });

  it("treats a stopped session (alive dropped) as ended", () => {
    const detect = createTurnEndDetector();
    detect([s({ state: "working" })]);
    expect(detect([s({ state: "working", alive: false })])).toEqual(["repos/agent-fleet"]);
  });

  it("does not fire for a row that stays idle", () => {
    const detect = createTurnEndDetector();
    detect([s({ state: "idle" })]);
    expect(detect([s({ state: "idle" })])).toEqual([]);
    expect(detect([s({ state: "question" })])).toEqual([]);
  });

  it("waits while background work remains and fires once it clears", () => {
    const detect = createTurnEndDetector();
    detect([s({ state: "working" })]);
    expect(detect([s({ state: "idle", backgroundBusy: true })])).toEqual([]);
    expect(detect([s({ state: "idle" })])).toEqual(["repos/agent-fleet"]);
  });

  it("folds two sessions ending in the same working copy into one entry", () => {
    const detect = createTurnEndDetector();
    detect([s({ name: "a", state: "working" }), s({ name: "b", state: "working" })]);
    expect(detect([s({ name: "a", state: "idle" }), s({ name: "b", state: "idle" })])).toEqual([
      "repos/agent-fleet",
    ]);
  });

  it("returns different working copies separately", () => {
    const detect = createTurnEndDetector();
    detect([s({ name: "a", state: "working" }), s({ name: "b", repo: "other", state: "working" })]);
    expect(
      detect([s({ name: "a", state: "idle" }), s({ name: "b", repo: "other", state: "idle" })]).sort(),
    ).toEqual(["repos/agent-fleet", "repos/other"]);
  });

  it("does not fire for a session with no working copy (a shell in home), having no range", () => {
    const detect = createTurnEndDetector();
    detect([s({ repo: null, state: "working" })]);
    expect(detect([s({ repo: null, state: "idle" })])).toEqual([]);
  });

  it("does not fire for a row that vanished from the list (deleted, archived)", () => {
    const detect = createTurnEndDetector();
    detect([s({ state: "working" })]);
    expect(detect([])).toEqual([]);
    // It is dropped from the ledger too, so the same name returning idle counts as a
    // first observation.
    expect(detect([s({ state: "idle" })])).toEqual([]);
  });
});

// The wiring: session list update -> coalescing and minimum gap -> a scoped signal on the
// FILES store.
describe("wireFilesSessionRefresh", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    useSessionsStore.setState({ sessions: [] });
    useFilesStore.setState({ scoped: { prefix: "", n: 0 } });
    useWorkspaceStore.setState({ state: "running" });
    hidden = false;
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const publish = (list: Session[]) => useSessionsStore.setState({ sessions: list });

  it("signals exactly once, naming that working copy, when a turn ends", () => {
    const un = wireFilesSessionRefresh();
    publish([s({ state: "working" })]);
    publish([s({ state: "idle" })]);
    expect(useFilesStore.getState().scoped.n).toBe(0); // still coalescing
    vi.advanceTimersByTime(500);
    expect(useFilesStore.getState().scoped).toEqual({ prefix: "repos/agent-fleet", n: 1 });
    un();
  });

  it("waits out the minimum gap and folds a burst on one working copy into one signal", () => {
    const un = wireFilesSessionRefresh();
    publish([s({ state: "working" })]);
    publish([s({ state: "idle" })]);
    vi.advanceTimersByTime(500);
    expect(useFilesStore.getState().scoped.n).toBe(1);
    // Even if the next turn ends immediately, nothing fires until the gap has elapsed.
    publish([s({ state: "working" })]);
    publish([s({ state: "idle" })]);
    vi.advanceTimersByTime(500);
    expect(useFilesStore.getState().scoped.n).toBe(1);
    vi.advanceTimersByTime(3000);
    expect(useFilesStore.getState().scoped.n).toBe(2);
    un();
  });

  it("fires nothing after unsubscribing, cancelling already-scheduled runs too", () => {
    const un = wireFilesSessionRefresh();
    publish([s({ state: "working" })]);
    publish([s({ state: "idle" })]);
    un();
    vi.advanceTimersByTime(5000);
    expect(useFilesStore.getState().scoped.n).toBe(0);
  });

  // The low-frequency refresh (WORKING_TICK_MS) for someone looking mid-turn.
  it("keeps firing at low frequency while running and folds the timer away when it stops", () => {
    const un = wireFilesSessionRefresh();
    publish([s({ state: "working" })]);
    vi.advanceTimersByTime(WORKING_TICK_MS + COALESCE_MS);
    expect(useFilesStore.getState().scoped).toEqual({ prefix: "repos/agent-fleet", n: 1 });
    vi.advanceTimersByTime(WORKING_TICK_MS + COALESCE_MS);
    expect(useFilesStore.getState().scoped.n).toBe(2);

    // The turn ends, firing once more. After that nothing is running, so it stays quiet.
    publish([s({ state: "idle" })]);
    vi.advanceTimersByTime(MIN_GAP_MS + COALESCE_MS);
    expect(useFilesStore.getState().scoped.n).toBe(3);
    vi.advanceTimersByTime(WORKING_TICK_MS * 3);
    expect(useFilesStore.getState().scoped.n).toBe(3);
    un();
  });

  it("skips the low-frequency refresh with the tab hidden or the workspace stopped, since it refreshes for someone looking", () => {
    const un = wireFilesSessionRefresh();
    publish([s({ state: "working" })]);

    hidden = true;
    vi.advanceTimersByTime(WORKING_TICK_MS * 2 + COALESCE_MS);
    expect(useFilesStore.getState().scoped.n).toBe(0);

    hidden = false;
    useWorkspaceStore.setState({ state: "stopped" });
    vi.advanceTimersByTime(WORKING_TICK_MS * 2 + COALESCE_MS);
    expect(useFilesStore.getState().scoped.n).toBe(0);

    // Back in the foreground with the workspace running, it resumes; the timer was never
    // stopped.
    useWorkspaceStore.setState({ state: "running" });
    vi.advanceTimersByTime(WORKING_TICK_MS + COALESCE_MS);
    expect(useFilesStore.getState().scoped.n).toBe(1);
    un();
  });
});
