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
let setTenant: typeof import("../../core/api/client.ts")["setTenant"];

beforeAll(async () => {
  ({ useReposStore } = await import("./store.ts"));
  ({ setTenant } = await import("../../core/api/client.ts"));
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
    useReposStore.setState({ repos: [], loadState: "loading" });
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

  // The regression: committing the 502's empty body wedged the rail on the
  // "no repositories" empty state until the 60s poll came round.
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

  // A refused request (4xx) or a non-JSON 2xx body (an auth gateway's HTML page, which api()
  // resolves as {}) carries no list: it must not blank the rail. It is terminal — retrying a
  // refusal changes nothing — but the outcome is recorded as failed.
  it("keeps the list on a coded 4xx error and records failed", async () => {
    useReposStore.setState({ repos: [repo("agent-fleet")], loadState: "ok" });
    fetchMock.mockResolvedValue(json({ error: { code: "forbidden_tenant" } }, 403));
    await expect(useReposStore.getState().refresh()).resolves.toBe(true);
    expect(useReposStore.getState().repos).toEqual([repo("agent-fleet")]);
    // Terminal, not "unreachable": nothing retries a 4xx, so the rail must not say it will.
    expect(useReposStore.getState().loadState).toBe("failed");
  });

  it("keeps the list on a 2xx body without a repos array ({} from a non-JSON page)", async () => {
    useReposStore.setState({ repos: [repo("agent-fleet")], loadState: "ok" });
    fetchMock.mockResolvedValue(new Response("<html>sign in</html>", { status: 200 }));
    await useReposStore.getState().refresh();
    expect(useReposStore.getState().repos).toEqual([repo("agent-fleet")]);
    expect(useReposStore.getState().loadState).toBe("failed");
    fetchMock.mockResolvedValue(json({ repos: null }));
    await useReposStore.getState().refresh();
    expect(useReposStore.getState().repos).toEqual([repo("agent-fleet")]);
  });

  it("records ok after a failed load once a real list lands", async () => {
    fetchMock.mockResolvedValue(agentUnreachable());
    await useReposStore.getState().refresh();
    expect(useReposStore.getState().loadState).toBe("unreachable");
    fetchMock.mockResolvedValue(json({ repos: [] }));
    await useReposStore.getState().refresh();
    expect(useReposStore.getState().loadState).toBe("ok");
  });

  // A JSON body does NOT make a 500 an app error: the CP answers a database failure
  // with {"error":{"code":"internal"}} and status 500. Reading that as terminal blanks
  // the rail and stops the retry — the same misreading that silently emptied the account
  // menu's admin items for a whole session (core/api isTransientErr).
  it("keeps them on a JSON-bodied 500 and asks to retry", async () => {
    useReposStore.setState({ repos: [repo("agent-fleet")] });
    fetchMock.mockResolvedValue(json({ error: { code: "internal", message: "db down" } }, 500));
    await expect(useReposStore.getState().refresh()).resolves.toBe(false);
    expect(useReposStore.getState().repos).toEqual([repo("agent-fleet")]);
  });

  // Switching tenants while GET /api/repos is in flight: its answer is the previous tenant's
  // workspace and must not land on the rail the switch just cleared.
  it("drops a response that lands after a tenant switch", async () => {
    setTenant("alpha");
    let answer!: (r: Response) => void;
    fetchMock.mockReturnValue(new Promise<Response>((res) => (answer = res)));
    const pending = useReposStore.getState().refresh();
    setTenant("beta");
    useReposStore.getState().clear();
    answer(json({ repos: [repo("alpha-only")] }));
    await expect(pending).resolves.toBe(false);
    expect(useReposStore.getState().repos).toEqual([]);
  });

  // A failure from an older request must not overwrite a newer success, whether it lands
  // after a tenant round trip or just after a concurrent refresh finished.
  it("ignores a stale rejection after a tenant switch", async () => {
    setTenant("alpha");
    let reject!: (e: Error) => void;
    fetchMock.mockReturnValueOnce(new Promise<Response>((_, rej) => (reject = rej)));
    const stale = useReposStore.getState().refresh();
    setTenant("beta");
    useReposStore.getState().clear();
    fetchMock.mockResolvedValueOnce(json({ repos: [repo("b")] }));
    await useReposStore.getState().refresh();
    reject(new TypeError("late drop"));
    await expect(stale).resolves.toBe(false);
    expect(useReposStore.getState().loadState).toBe("ok");
    expect(useReposStore.getState().repos).toEqual([repo("b")]);
  });

  it("lets the later-started refresh win over a slow earlier failure", async () => {
    setTenant("gamma");
    let reject!: (e: Error) => void;
    fetchMock.mockReturnValueOnce(new Promise<Response>((_, rej) => (reject = rej)));
    const first = useReposStore.getState().refresh();
    fetchMock.mockResolvedValueOnce(json({ repos: [repo("new")] }));
    await useReposStore.getState().refresh();
    reject(new TypeError("slow failure"));
    await first;
    expect(useReposStore.getState().loadState).toBe("ok");
    expect(useReposStore.getState().repos).toEqual([repo("new")]);
  });

  it("ignores an earlier success that lands after a newer refresh started", async () => {
    setTenant("delta");
    let answer!: (r: Response) => void;
    fetchMock.mockReturnValueOnce(new Promise<Response>((res) => (answer = res)));
    const first = useReposStore.getState().refresh();
    fetchMock.mockResolvedValueOnce(json({ repos: [repo("new")] }));
    await useReposStore.getState().refresh();
    answer(json({ repos: [repo("old")] }));
    await first;
    expect(useReposStore.getState().repos).toEqual([repo("new")]);
  });

  it("clear() forgets the list and goes back to loading (tenant switch)", () => {
    useReposStore.setState({ repos: [repo("agent-fleet")], loadState: "ok" });
    useReposStore.getState().clear();
    expect(useReposStore.getState().repos).toEqual([]);
    expect(useReposStore.getState().loadState).toBe("loading");
  });
});
