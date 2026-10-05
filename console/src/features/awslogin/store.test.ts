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

let useAwsLoginStore: typeof import("./store.ts")["useAwsLoginStore"];

beforeAll(async () => {
  ({ useAwsLoginStore } = await import("./store.ts"));
  ({ setTenant } = await import("../../core/api/client.ts"));
});

const request = { id: "r1", profile: "p", label: "P", accountId: "1", roleName: "r", waiters: [], firstAt: "", lastAt: "" };
const profile = { name: "p", label: "P", accountId: "1", roleName: "r", state: "signed_in", expiresAt: "", expiring: false };

describe("aws login store across a tenant switch", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    useAwsLoginStore.getState().reset();
  });

  it("reset() forgets requests, profiles, closed toasts and open modals", () => {
    useAwsLoginStore.setState({
      requests: [request],
      hidden: { r1: true },
      modal: "r1",
      profiles: [profile],
      expiring: [{ name: "p", label: "P", accountId: "1", roleName: "r", expiresAt: "x" }],
      hiddenExpiry: { k: true },
      profileModal: { name: "p", label: "P", accountId: "1", roleName: "r" },
    });
    useAwsLoginStore.getState().reset();
    const s = useAwsLoginStore.getState();
    expect([s.requests, s.hidden, s.modal, s.profiles, s.expiring, s.hiddenExpiry, s.profileModal]).toEqual([
      [], {}, null, null, [], {}, null,
    ]);
  });

  it("drops the login requests that land after a switch", async () => {
    setTenant("alpha");
    let answer!: (r: Response) => void;
    fetchMock.mockReturnValueOnce(new Promise<Response>((res) => (answer = res)));
    const pending = useAwsLoginStore.getState().refresh();
    setTenant("beta");
    answer(json({ requests: [request] }));
    await pending;
    expect(useAwsLoginStore.getState().requests).toEqual([]);
  });

  it("drops the profiles that land after a switch", async () => {
    setTenant("alpha");
    let answer!: (r: Response) => void;
    fetchMock.mockReturnValueOnce(new Promise<Response>((res) => (answer = res)));
    const pending = useAwsLoginStore.getState().refreshExpiry();
    setTenant("beta");
    answer(json({ profiles: [profile] }));
    await expect(pending).resolves.toBeNull();
    expect(useAwsLoginStore.getState().profiles).toBeNull();
  });
});
