// The session list store's "resume never dies" contract. Every route to resuming a stopped
// session depends on that row being in the list, so blanking the list on a transient fetch
// failure takes the resume button with it (it happened in the 502 window right after a
// container restart). That a failed resume POST is not swallowed is pinned here too.
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

// The store imports the api client (which binds window.fetch and reads document.baseURI), so
// stub the globals before importing it (the same style as workspace.test.ts).
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

const toastMock = vi.fn();
vi.mock("../../ui/toast.ts", () => ({ toast: (...a: unknown[]) => toastMock(...a) }));

let useSessionsStore: typeof import("./store.ts")["useSessionsStore"];
beforeAll(async () => {
  ({ useSessionsStore } = await import("./store.ts"));
});

const jsonResponse = (body: unknown) =>
  new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });

const row = (name: string, alive: boolean) => ({ name, kind: "shell", alive, resumable: true });

describe("sessions store", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    toastMock.mockReset();
    useSessionsStore.setState({ sessions: [] });
  });

  it("keeps the last known list when a refresh fails", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", false)] }));
    await useSessionsStore.getState().refresh();
    expect(useSessionsStore.getState().sessions).toHaveLength(1);

    // The 502 window while the agent comes up. Blanking here removed the row, and
    // with it the only path back into a stopped session.
    fetchMock.mockRejectedValueOnce(new Error("502"));
    await useSessionsStore.getState().refresh();
    expect(useSessionsStore.getState().sessions.map((s) => s.name)).toEqual(["ssko6g5"]);
  });

  // The gap the case above did not cover: a REJECTED fetch is only half of "a refresh
  // failed". When the server answers, api() resolves an error body instead of throwing, so
  // the catch never runs and `d.sessions || []` published an empty list. Both shapes the CP
  // really produces are pinned here — the plain-text 502 it writes while the agent is
  // unreachable, and the JSON 500 it writes when its own store is unwell.
  // Each case seeds a DISTINCT name: applyList only publishes on a changed serialization and
  // that comparison is module state, so reusing one name would make the seeding refresh a
  // no-op and the case would fail before it reached what it is testing.
  it.each([
    ["plain-text 502 from the agent proxy", "sproxy52", () => new Response("workspace agent unreachable", { status: 502 })],
    [
      "JSON 500 from the control plane",
      "sinternal5",
      () =>
        new Response(JSON.stringify({ error: { code: "internal" } }), {
          status: 500,
          headers: { "Content-Type": "application/json" },
        }),
    ],
  ])("keeps the last known list on a %s", async (_label, name, res) => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ sessions: [row(name, false)] }));
    await useSessionsStore.getState().refresh();
    expect(useSessionsStore.getState().sessions).toHaveLength(1);

    fetchMock.mockResolvedValueOnce(res());
    await useSessionsStore.getState().refresh();
    expect(useSessionsStore.getState().sessions.map((s) => s.name)).toEqual([name]);
  });

  it("reports a failed resume instead of swallowing it", async () => {
    fetchMock
      .mockRejectedValueOnce(new Error("502")) // POST …/start
      .mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", false)] })); // trailing refresh

    await expect(useSessionsStore.getState().start("ssko6g5")).resolves.toBe(false);
    expect(toastMock).toHaveBeenCalledTimes(1);
    expect(toastMock.mock.calls[0][1]).toMatchObject({ kind: "error" });
  });

  // The accepted POST is authoritative (docs/log/85): the row has to say "stopping after
  // this turn" straight away, or the only feedback for an arm set from the ⋯ menu is a toast
  // that disappears, and the arm the session set on the user's word stays invisible until the
  // next poll lands.
  it("shows a stop-after-turn arm before the next list poll", () => {
    useSessionsStore.setState({ sessions: [{ ...row("ssko6g5", true), kind: "shell" as const }] });
    useSessionsStore.getState().setStopAfterTurn("ssko6g5", "2026-09-07T10:00:00Z");
    expect(useSessionsStore.getState().sessions[0].stopAfterTurnAt).toBe("2026-09-07T10:00:00Z");

    useSessionsStore.getState().setStopAfterTurn("ssko6g5", "");
    expect(useSessionsStore.getState().sessions[0].stopAfterTurnAt).toBe("");
  });

  it("reports a successful resume", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ ok: true })) // POST …/start
      .mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", true)] }));

    await expect(useSessionsStore.getState().start("ssko6g5")).resolves.toBe(true);
    expect(toastMock).not.toHaveBeenCalled();
  });
});
