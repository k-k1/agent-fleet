import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

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

let setTenant: typeof import("../../core/api/client.ts")["setTenant"];
const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200 });

let useRepoJobsStore: typeof import("./jobs.ts")["useRepoJobsStore"];

beforeAll(async () => {
  ({ useRepoJobsStore } = await import("./jobs.ts"));
  ({ setTenant } = await import("../../core/api/client.ts"));
});

const job = (id: string) => ({ id, kind: "git" as const, name: id, state: "running" as const, startedAt: "" });

describe("repo jobs across a tenant switch", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    useRepoJobsStore.getState().reset();
  });

  it("drops a list that lands after the switch", async () => {
    setTenant("alpha");
    let answer!: (r: Response) => void;
    fetchMock.mockReturnValueOnce(new Promise<Response>((res) => (answer = res)));
    const pending = useRepoJobsStore.getState().refresh();
    setTenant("beta");
    useRepoJobsStore.getState().reset();
    answer(json({ jobs: [job("alpha-job")] }));
    await pending;
    expect(useRepoJobsStore.getState().jobs).toEqual([]);
  });

  it("does not hand the old tenant's in-flight request to a refresh after the switch", async () => {
    setTenant("alpha");
    fetchMock.mockReturnValueOnce(new Promise<Response>(() => {})); // never answers
    void useRepoJobsStore.getState().refresh();
    setTenant("beta");
    useRepoJobsStore.getState().reset();
    fetchMock.mockResolvedValueOnce(json({ jobs: [job("beta-job")] }));
    await useRepoJobsStore.getState().refresh();
    expect(useRepoJobsStore.getState().jobs.map((j) => j.id)).toEqual(["beta-job"]);
  });
});
