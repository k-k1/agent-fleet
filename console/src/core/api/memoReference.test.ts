// The three meanings of `reference` on a memo write (docs/build/03-control-plane.md §3.6): on a
// PATCH, absent leaves it, `{}` clears it and a full object sets it. The client must send exactly
// what the caller built, so a "leave it" never turns into a clear (null reads as absent, `{}` clears).
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
vi.stubGlobal("sessionStorage", { getItem: () => null, setItem: () => {}, removeItem: () => {} });
vi.stubGlobal("document", { baseURI: "http://localhost/" });
vi.stubGlobal("window", { fetch: vi.fn(), addEventListener: () => {}, removeEventListener: () => {} });
const answers: Response[] = [];
const fetchSpy = vi.fn(async (..._a: unknown[]) => answers.shift() ?? new Response("{}"));
vi.stubGlobal("fetch", fetchSpy);

let client: typeof import("./client.ts");
beforeAll(async () => {
  client = await import("./client.ts");
});
beforeEach(() => fetchSpy.mockClear());

const ref = { provider: "github", key: "acme/app#12", url: "https://github.com/acme/app/issues/12" };
const sent = () => {
  const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
  return { url: String(url).replace("http://localhost/", ""), method: init.method, body: JSON.parse(String(init.body)) };
};

describe("memoUpdate reference", () => {
  it("leaves the reference out when the caller does not touch it", async () => {
    await client.memoUpdate("m1", { body: "x", category: "c" });
    expect(sent().body).toEqual({ body: "x", category: "c" });
    expect("reference" in sent().body).toBe(false);
  });

  it("sends {} to clear it", async () => {
    await client.memoUpdate("m1", { reference: {} });
    expect(sent()).toMatchObject({ url: "api/memos/m1", method: "PATCH", body: { reference: {} } });
  });

  it("sends the full object to set it", async () => {
    await client.memoUpdate("m1", { reference: ref });
    expect(sent().body).toEqual({ reference: ref });
  });
});

describe("memoCreate reference", () => {
  it("sends the reference on create and keeps `existing` of the answer", async () => {
    answers.push(new Response(JSON.stringify({ id: "m9", existing: true, reference: ref }), { status: 200 }));
    const res = await client.memoCreate({ kind: "text", body: "", repo: "app", category: "Bugs", reference: ref });
    expect(sent()).toMatchObject({ url: "api/memos", method: "POST", body: { kind: "text", body: "", reference: ref } });
    expect(res.existing).toBe(true);
  });

  it("has no reference key when there is none", async () => {
    await client.memoCreate({ kind: "text", body: "plain" });
    expect("reference" in sent().body).toBe(false);
  });
});
