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

let useChatStore: typeof import("./store.ts")["useChatStore"];
let ensureConvs: typeof import("./store.ts")["ensureConvs"];

beforeAll(async () => {
  ({ useChatStore, ensureConvs } = await import("./store.ts"));
  ({ setTenant } = await import("../../core/api/client.ts"));
});

const meta = (id: string) => ({ id, title: id }) as never;

describe("chat store across a tenant switch", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    useChatStore.getState().resetConvs();
  });

  it("resetConvs() forgets the list and the titles", () => {
    useChatStore.setState({ convs: [meta("a1")], titles: { a1: "x" } });
    useChatStore.getState().resetConvs();
    expect([useChatStore.getState().convs, useChatStore.getState().titles]).toEqual([null, {}]);
  });

  it("ensureConvs drops a list that lands after the switch", async () => {
    setTenant("alpha");
    let answer!: (r: Response) => void;
    fetchMock.mockReturnValueOnce(new Promise<Response>((res) => (answer = res)));
    const pending = ensureConvs();
    setTenant("beta");
    useChatStore.getState().resetConvs();
    answer(json({ conversations: [meta("alpha-chat")] }));
    await pending;
    expect(useChatStore.getState().convs).toBeNull();
  });

  it("ensureConvs after a reset asks again instead of riding the old request", async () => {
    setTenant("alpha");
    fetchMock.mockReturnValueOnce(new Promise<Response>(() => {})); // never answers
    void ensureConvs();
    setTenant("beta");
    useChatStore.getState().resetConvs();
    fetchMock.mockResolvedValueOnce(json({ conversations: [meta("beta-chat")] }));
    await ensureConvs();
    expect(useChatStore.getState().convs).toEqual([meta("beta-chat")]);
  });
});
