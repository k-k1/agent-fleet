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

let useGcpLoginStore: typeof import("./store.ts")["useGcpLoginStore"];

beforeAll(async () => {
  ({ useGcpLoginStore } = await import("./store.ts"));
  ({ setTenant } = await import("../../core/api/client.ts"));
});

describe("gcp login store across a tenant switch", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    useGcpLoginStore.getState().reset();
  });

  it("reset() forgets requests, profiles, closed toasts and open modals", () => {
    useGcpLoginStore.setState({
      requests: [{ id: "r1" } as never],
      hidden: { r1: true },
      modal: "r1",
      profiles: [{ name: "p", label: "P", project: "x", account: "", state: "signed_in" }],
      profileModal: { profile: { name: "p", label: "P", project: "x", account: "" }, force: false },
    });
    useGcpLoginStore.getState().reset();
    const s = useGcpLoginStore.getState();
    expect([s.requests, s.hidden, s.modal, s.profiles, s.profileModal]).toEqual([[], {}, null, null, null]);
  });

  it("drops the profiles that land after a switch", async () => {
    setTenant("alpha");
    let answer!: (r: Response) => void;
    fetchMock.mockReturnValueOnce(new Promise<Response>((res) => (answer = res)));
    const pending = useGcpLoginStore.getState().refreshProfiles();
    setTenant("beta");
    answer(json({ profiles: [{ name: "p", label: "P", project: "x", account: "", state: "signed_in" }] }));
    await pending;
    expect(useGcpLoginStore.getState().profiles).toBeNull();
  });

  it("drops the login requests that land after a switch", async () => {
    setTenant("alpha");
    let answer!: (r: Response) => void;
    fetchMock.mockReturnValueOnce(new Promise<Response>((res) => (answer = res)));
    const pending = useGcpLoginStore.getState().refresh();
    setTenant("beta");
    answer(json({ requests: [{ id: "r1", profile: "p", label: "P", project: "x", account: "", relogin: false, waiters: [], firstAt: "", lastAt: "" }] }));
    await pending;
    expect(useGcpLoginStore.getState().requests).toEqual([]);
  });
});
