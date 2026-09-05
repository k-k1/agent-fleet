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

  it("reports a failed resume instead of swallowing it", async () => {
    fetchMock
      .mockRejectedValueOnce(new Error("502")) // POST …/start
      .mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", false)] })); // trailing refresh

    await expect(useSessionsStore.getState().start("ssko6g5")).resolves.toBe(false);
    expect(toastMock).toHaveBeenCalledTimes(1);
    expect(toastMock.mock.calls[0][1]).toMatchObject({ kind: "error" });
  });

  it("reports a successful resume", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ ok: true })) // POST …/start
      .mockResolvedValueOnce(jsonResponse({ sessions: [row("ssko6g5", true)] }));

    await expect(useSessionsStore.getState().start("ssko6g5")).resolves.toBe(true);
    expect(toastMock).not.toHaveBeenCalled();
  });
});
