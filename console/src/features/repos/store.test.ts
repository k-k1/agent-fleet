import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { Repo } from "./store.ts";

// The store imports the api client, which binds window.fetch, reads localStorage and
// resolves URLs against document.baseURI at module load — stub all three before importing.
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
vi.stubGlobal("document", { baseURI: "http://localhost/" });
const fetchMock = vi.fn<() => Promise<Response>>();
vi.stubGlobal("window", { fetch: fetchMock });
vi.stubGlobal("fetch", fetchMock);

let useReposStore: typeof import("./store.ts")["useReposStore"];

beforeAll(async () => {
  ({ useReposStore } = await import("./store.ts"));
});

// The exact response the CP writes while the workspace agent is still booting after a
// WS start — plain text, not JSON (control-plane/proxy.go). A *stopped* workspace gets
// the same one, which is why refresh() leaves the verdict to its caller.
const agentUnreachable = () =>
  new Response("workspace agent unreachable (is the workspace running?)", { status: 502 });

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

const repo = (name: string): Repo => ({ name });

describe("repos store refresh", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    useReposStore.setState({ repos: [] });
  });

  it("commits the list and reports terminal on a real response", async () => {
    fetchMock.mockResolvedValue(json({ repos: [repo("agent-fleet")] }));
    await expect(useReposStore.getState().refresh()).resolves.toBe(true);
    expect(useReposStore.getState().repos).toEqual([repo("agent-fleet")]);
  });

  it("commits an empty list when the workspace genuinely has no repos", async () => {
    useReposStore.setState({ repos: [repo("gone")] });
    fetchMock.mockResolvedValue(json({ repos: [] }));
    await expect(useReposStore.getState().refresh()).resolves.toBe(true);
    expect(useReposStore.getState().repos).toEqual([]);
  });

  // The regression: committing the 502's empty body wedged the rail on
  // リポジトリがありません until the 60s poll came round.
  it("keeps the rail's repos on the agent-still-booting 502 and asks to retry", async () => {
    useReposStore.setState({ repos: [repo("agent-fleet")] });
    fetchMock.mockResolvedValue(agentUnreachable());
    const settled = await useReposStore.getState().refresh();
    expect(useReposStore.getState().repos).toEqual([repo("agent-fleet")]); // not blanked
    expect(settled).toBe(false); // caller retries
  });

  it("keeps them on a dropped fetch too", async () => {
    useReposStore.setState({ repos: [repo("agent-fleet")] });
    fetchMock.mockRejectedValue(new TypeError("network drop"));
    const settled = await useReposStore.getState().refresh();
    expect(useReposStore.getState().repos).toEqual([repo("agent-fleet")]);
    expect(settled).toBe(false);
  });

  // An app-level failure always carries its own JSON code, so it is NOT the boot window
  // and must settle rather than retry forever.
  it("treats a coded app error as terminal", async () => {
    useReposStore.setState({ repos: [repo("agent-fleet")] });
    fetchMock.mockResolvedValue(json({ error: { code: "repos_unavailable" } }, 500));
    await expect(useReposStore.getState().refresh()).resolves.toBe(true);
    expect(useReposStore.getState().repos).toEqual([]);
  });

  it("clear() settles to empty for a caller that knows the repos are unreachable", () => {
    useReposStore.setState({ repos: [repo("agent-fleet")] });
    useReposStore.getState().clear();
    expect(useReposStore.getState().repos).toEqual([]);
  });
});
